#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# scripts/deploy.sh — ledger-query deployment
#
# USAGE
#   ./scripts/deploy.sh [options]
#
# OPTIONS
#   --env          prod|staging       (default: prod)
#   --tag          image tag          (default: git short sha)
#   --plan-only    terraform plan only, no apply
#   --dry-run      print steps, execute nothing
#   --auto-approve skip confirmation prompts
#   --rollback     roll back K8s Deployment to previous revision
#   -h, --help
#
# PREREQUISITE
#   ledger-command must be deployed first so its Terraform state
#   (MSK broker endpoints) is readable by this plan.
#
# EXAMPLES
#   ./scripts/deploy.sh --tag v1.2.3
#   ./scripts/deploy.sh --plan-only
#   ./scripts/deploy.sh --rollback
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

ENV="prod"
TAG="$(git rev-parse --short HEAD 2>/dev/null || echo latest)"
PLAN_ONLY=false
DRY_RUN=false
AUTO_APPROVE=""
ROLLBACK=false
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
TF_DIR="$ROOT_DIR/terraform/envs"
LOG="$ROOT_DIR/.deploy-$(date +%Y%m%d-%H%M%S).log"

RED='\033[0;31m';GREEN='\033[0;32m';YELLOW='\033[1;33m';BLUE='\033[0;34m';BOLD='\033[1m';NC='\033[0m'
log()  { echo -e "${BLUE}[$(date +%H:%M:%S)]${NC} $*" | tee -a "$LOG"; }
ok()   { echo -e "${GREEN}[$(date +%H:%M:%S)] ✓ $*${NC}" | tee -a "$LOG"; }
warn() { echo -e "${YELLOW}[$(date +%H:%M:%S)] ⚠ $*${NC}" | tee -a "$LOG"; }
err()  { echo -e "${RED}[$(date +%H:%M:%S)] ✗ $*${NC}" | tee -a "$LOG" >&2; }
dry()  { echo -e "${YELLOW}[DRY-RUN]${NC} $*"; }
step() { echo -e "\n${BOLD}${BLUE}═══ $* ═══${NC}\n" | tee -a "$LOG"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env)          ENV="$2";  shift 2 ;;
    --tag)          TAG="$2";  shift 2 ;;
    --plan-only)    PLAN_ONLY=true;  shift ;;
    --dry-run)      DRY_RUN=true;    shift ;;
    --auto-approve) AUTO_APPROVE="-auto-approve"; shift ;;
    --rollback)     ROLLBACK=true;   shift ;;
    -h|--help)      grep '^#' "$0" | sed 's/^# \{0,2\}//'; exit 0 ;;
    *) err "Unknown: $1"; exit 1 ;;
  esac
done

TF_ENV_DIR="$TF_DIR/$ENV"

check_deps() {
  local missing=()
  for t in terraform aws kubectl docker; do
    command -v "$t" &>/dev/null || missing+=("$t")
  done
  [[ ${#missing[@]} -gt 0 ]] && { err "Missing: ${missing[*]}"; exit 1; }
  ok "All tools found"
}

check_aws() {
  local acct
  acct=$(aws sts get-caller-identity --query Account --output text 2>/dev/null) \
    || { err "AWS credentials not valid"; exit 1; }
  ok "AWS account: $acct"
}

check_command_state() {
  step "CHECK ledger-command STATE"
  local bucket key region
  bucket=$(grep command_tfstate_bucket "$TF_ENV_DIR/terraform.tfvars" | awk -F'"' '{print $2}')
  key=$(grep command_tfstate_key "$TF_ENV_DIR/terraform.tfvars"    | awk -F'"' '{print $2}')
  region=$(grep aws_region "$TF_ENV_DIR/terraform.tfvars" | awk -F'"' '{print $2}')

  if ! aws s3 ls "s3://${bucket}/${key}" --region "${region}" &>/dev/null; then
    err "ledger-command state not found at s3://${bucket}/${key}"
    err "Deploy ledger-command first: cd ../ledger-command && bash scripts/deploy.sh"
    exit 1
  fi
  ok "ledger-command state found — MSK endpoints will be read from it"
}

push_image() {
  step "BUILD + PUSH IMAGE  (tag: $TAG)"
  local ecr_url
  ecr_url=$(terraform -chdir="$TF_ENV_DIR" output -raw ecr_url 2>/dev/null || echo "")
  if [[ -z "$ecr_url" ]]; then
    warn "ECR URL not in state yet — skipping image push"
    return
  fi

  [[ "$DRY_RUN" == true ]] && { dry "docker buildx build ... $ecr_url:$TAG"; return; }

  local region
  region=$(echo "$ecr_url" | cut -d. -f4)
  aws ecr get-login-password --region "$region" \
    | docker login --username AWS --password-stdin "$(echo "$ecr_url" | cut -d/ -f1)"

  docker buildx build \
    --platform linux/amd64 \
    --file "$ROOT_DIR/Dockerfile" \
    --tag "$ecr_url:$TAG" \
    --tag "$ecr_url:latest" \
    --push \
    "$ROOT_DIR"
  ok "Image pushed: $ecr_url:$TAG"
}

tf_init() {
  step "TERRAFORM INIT"
  [[ "$DRY_RUN" == true ]] && { dry "terraform init"; return; }
  terraform -chdir="$TF_ENV_DIR" init -reconfigure 2>&1 | tee -a "$LOG"
  ok "init done"
}

tf_plan() {
  step "TERRAFORM PLAN"
  [[ "$DRY_RUN" == true ]] && { dry "terraform plan -var=image_tag=$TAG"; return; }
  terraform -chdir="$TF_ENV_DIR" plan \
    -var="image_tag=$TAG" \
    -var-file="terraform.tfvars" \
    -out="$TF_ENV_DIR/.tfplan" \
    2>&1 | tee -a "$LOG"
  ok "plan saved"
}

tf_apply() {
  [[ "$PLAN_ONLY" == true ]] && { warn "plan-only — skipping apply"; return; }
  step "TERRAFORM APPLY"
  [[ "$DRY_RUN" == true ]] && { dry "terraform apply .tfplan"; return; }
  terraform -chdir="$TF_ENV_DIR" apply $AUTO_APPROVE "$TF_ENV_DIR/.tfplan" \
    2>&1 | tee -a "$LOG"
  ok "apply done"
}

update_kubeconfig() {
  step "UPDATE KUBECONFIG"
  local cluster
  cluster=$(terraform -chdir="$TF_ENV_DIR" output -raw eks_cluster_name 2>/dev/null || echo "")
  [[ -z "$cluster" ]] && { warn "EKS cluster not in state yet"; return; }
  [[ "$DRY_RUN" == true ]] && { dry "aws eks update-kubeconfig --name $cluster"; return; }
  aws eks update-kubeconfig --name "$cluster" --region "$ENV" 2>&1 | tee -a "$LOG" || true
  ok "kubeconfig updated for $cluster"
}

run_migrations() {
  step "DATABASE MIGRATIONS"
  [[ "$DRY_RUN" == true || "$PLAN_ONLY" == true ]] && { dry "kubectl migration job"; return; }

  cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: ledger-query-migrate-$(date +%s)
  namespace: ledger
  labels: { app: ledger-query-migrate }
spec:
  ttlSecondsAfterFinished: 600
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: ledger-query
      containers:
        - name: migrate
          image: migrate/migrate:v4.17.0
          command:
            - /migrate
            - -database
            - \$(DATABASE_URL)
            - -path
            - /migrations
            - up
          envFrom:
            - secretRef: { name: ledger-query-secrets }
          volumeMounts:
            - { name: migrations, mountPath: /migrations }
      volumes:
        - name: migrations
          configMap: { name: ledger-query-migrations }
EOF

  kubectl wait --for=condition=complete --timeout=120s \
    job -l app=ledger-query-migrate -n ledger 2>&1 | tee -a "$LOG" || {
    err "Migration failed"
    kubectl logs -l app=ledger-query-migrate -n ledger --tail=50
    exit 1
  }
  ok "migrations complete"
}

wait_rollout() {
  step "WAIT FOR ROLLOUT"
  [[ "$DRY_RUN" == true || "$PLAN_ONLY" == true ]] && { dry "kubectl rollout status"; return; }
  kubectl rollout status deployment/ledger-query -n ledger --timeout=300s \
    2>&1 | tee -a "$LOG"
  ok "rollout complete"
}

do_rollback() {
  step "ROLLBACK"
  warn "Rolling back ledger-query to previous revision..."
  [[ "$DRY_RUN" == true ]] && { dry "kubectl rollout undo deployment/ledger-query -n ledger"; return; }
  kubectl rollout undo deployment/ledger-query -n ledger
  kubectl rollout status deployment/ledger-query -n ledger --timeout=300s
  ok "rollback complete"
}

banner() {
  echo -e "${BOLD}${BLUE}"
  echo "  ╔═════════════════════════════════════╗"
  echo "  ║   ledger-query  ·  deploy.sh        ║"
  echo "  ╚═════════════════════════════════════╝${NC}"
  log "env:       $ENV"
  log "image tag: $TAG"
  log "plan-only: $PLAN_ONLY"
  log "dry-run:   $DRY_RUN"
  log "rollback:  $ROLLBACK"
  log "log file:  $LOG"
}

main() {
  banner
  check_deps
  check_aws

  if [[ "$ROLLBACK" == true ]]; then
    update_kubeconfig
    do_rollback
    exit 0
  fi

  check_command_state   # ensures MSK state is readable before planning
  push_image
  tf_init
  tf_plan
  tf_apply
  update_kubeconfig
  run_migrations
  wait_rollout

  step "DEPLOY COMPLETE"
  ok "ledger-query is live  (tag: $TAG)"
  ok "Log: $LOG"
}

main "$@"
