# terraform/envs/prod/main.tf
# Root module for the ledger-query production environment.
#
# Key difference from ledger-command:
#   - Reads the MSK bootstrap endpoint from ledger-command's remote state.
#     The query service does NOT own Kafka — it only consumes from it.
#   - Creates its own VPC, EKS cluster, and RDS (read-projection database).
#   - Does NOT create an MSK cluster.

terraform {
  required_version = ">= 1.7"
  required_providers {
    aws        = { source = "hashicorp/aws",        version = "~> 5.0" }
    kubernetes = { source = "hashicorp/kubernetes",  version = "~> 2.30" }
    helm       = { source = "hashicorp/helm",        version = "~> 2.14" }
    tls        = { source = "hashicorp/tls",         version = "~> 4.0" }
    random     = { source = "hashicorp/random",      version = "~> 3.6" }
  }
  backend "s3" {
    bucket         = "ledger-query-tfstate"
    key            = "prod/terraform.tfstate"
    region         = "us-east-1"
    encrypt        = true
    dynamodb_table = "ledger-query-tflock"
  }
}

# ── Variables ─────────────────────────────────────────────────────────────────

variable "aws_region"    { type = string, default = "us-east-1" }
variable "project"       { type = string, default = "ledger-query" }
variable "env"           { type = string, default = "prod" }
variable "vpc_cidr"      { type = string, default = "10.1.0.0/16" }
variable "image_tag"     { type = string, default = "latest" }
variable "replicas"      { type = number, default = 3 }

variable "k8s_version"   { type = string, default = "1.30" }
variable "instance_type" { type = string, default = "t3.medium" }
variable "node_min"      { type = number, default = 2 }
variable "node_max"      { type = number, default = 6 }
variable "node_desired"  { type = number, default = 3 }

variable "db_instance_class"   { type = string, default = "db.t3.medium" }
variable "db_storage"          { type = number, default = 100 }
variable "db_multi_az"         { type = bool,   default = true }
variable "db_backup_retention" { type = number, default = 7 }

# State bucket for ledger-command — used to read the MSK secret ARN and
# bootstrap brokers without hard-coding them here.
variable "command_tfstate_bucket" { type = string, default = "ledger-command-tfstate" }
variable "command_tfstate_key"    { type = string, default = "prod/terraform.tfstate" }

locals {
  name = "${var.project}-${var.env}"
  azs  = ["${var.aws_region}a", "${var.aws_region}b", "${var.aws_region}c"]
  tags = {
    Project     = var.project
    Environment = var.env
    ManagedBy   = "terraform"
  }
}

# ── Providers ─────────────────────────────────────────────────────────────────

provider "aws" {
  region = var.aws_region
  default_tags { tags = local.tags }
}

provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_ca)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.aws_region]
  }
}

provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_ca)
    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.aws_region]
    }
  }
}

# ── Read MSK outputs from ledger-command state ────────────────────────────────
# This avoids duplicating Kafka infrastructure. The query service is a consumer
# of the same MSK cluster that the command service writes to.

data "terraform_remote_state" "command" {
  backend = "s3"
  config = {
    bucket  = var.command_tfstate_bucket
    key     = var.command_tfstate_key
    region  = var.aws_region
  }
}

locals {
  # Falls back to a placeholder if command state doesn't exist yet.
  # In that case, deploy command first, then query.
  msk_brokers    = try(data.terraform_remote_state.command.outputs.msk_brokers, "localhost:9092")
  msk_secret_arn = try(data.terraform_remote_state.command.outputs.msk_secret_arn, "")
}

# ── Remote state backend ──────────────────────────────────────────────────────

resource "aws_s3_bucket" "tfstate" {
  bucket        = "ledger-query-tfstate"
  force_destroy = false
}
resource "aws_s3_bucket_versioning" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  versioning_configuration { status = "Enabled" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  rule { apply_server_side_encryption_by_default { sse_algorithm = "AES256" } }
}
resource "aws_dynamodb_table" "tflock" {
  name         = "ledger-query-tflock"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"
  attribute { name = "LockID"; type = "S" }
}

# ── ECR ───────────────────────────────────────────────────────────────────────

resource "aws_ecr_repository" "this" {
  name                 = local.name
  image_tag_mutability = "MUTABLE"
  image_scanning_configuration { scan_on_push = true }
  encryption_configuration { encryption_type = "AES256" }
}
resource "aws_ecr_lifecycle_policy" "this" {
  repository = aws_ecr_repository.this.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep last 20 images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 20 }
      action       = { type = "expire" }
    }]
  })
}

# ── VPC ───────────────────────────────────────────────────────────────────────
# Uses a different CIDR (10.1.0.0/16) from the command VPC (10.0.0.0/16)
# so VPC peering or Transit Gateway can route traffic between them.

module "vpc" {
  source = "../../modules/vpc"
  name   = local.name
  cidr   = var.vpc_cidr
  azs    = local.azs
  tags   = local.tags
}

# ── EKS ───────────────────────────────────────────────────────────────────────

module "eks" {
  source           = "../../modules/eks"
  cluster_name     = local.name
  k8s_version      = var.k8s_version
  vpc_id           = module.vpc.vpc_id
  private_subnets  = module.vpc.private_subnets
  instance_type    = var.instance_type
  node_min         = var.node_min
  node_max         = var.node_max
  node_desired     = var.node_desired
  tags             = local.tags
}

# ── RDS (read-projection database) ───────────────────────────────────────────

module "rds" {
  source            = "../../modules/rds"
  identifier        = "${local.name}-postgres"
  vpc_id            = module.vpc.vpc_id
  db_subnets        = module.vpc.db_subnets
  allowed_sg_ids    = []
  instance_class    = var.db_instance_class
  allocated_storage = var.db_storage
  multi_az          = var.db_multi_az
  backup_retention  = var.db_backup_retention
  db_name           = "ledger_query"
  tags              = local.tags
}

# ── IRSA — allow pods to read Secrets Manager ────────────────────────────────

resource "aws_iam_policy" "app_secrets" {
  name = "${local.name}-secrets"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
      Resource = compact([module.rds.secret_arn, local.msk_secret_arn])
    }]
  })
}

resource "aws_iam_role" "app" {
  name = "${local.name}-irsa"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = module.eks.oidc_provider_arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(module.eks.oidc_provider_url, "https://", "")}:sub" = "system:serviceaccount:ledger:ledger-query"
        }
      }
    }]
  })
  tags = local.tags
}
resource "aws_iam_role_policy_attachment" "app" {
  role       = aws_iam_role.app.name
  policy_arn = aws_iam_policy.app_secrets.arn
}

# ── Kubernetes resources ──────────────────────────────────────────────────────

resource "kubernetes_namespace" "ledger" {
  metadata { name = "ledger" }
  depends_on = [module.eks]
}

resource "helm_release" "eso" {
  name             = "external-secrets"
  repository       = "https://charts.external-secrets.io"
  chart            = "external-secrets"
  namespace        = "external-secrets"
  create_namespace = true
  version          = "0.9.19"
  set { name = "installCRDs", value = "true" }
  depends_on = [module.eks]
}

resource "kubernetes_service_account" "app" {
  metadata {
    name      = "ledger-query"
    namespace = "ledger"
    annotations = { "eks.amazonaws.com/role-arn" = aws_iam_role.app.arn }
  }
  depends_on = [kubernetes_namespace.ledger]
}

resource "kubernetes_manifest" "secret_store" {
  manifest = {
    apiVersion = "external-secrets.io/v1beta1"
    kind       = "ClusterSecretStore"
    metadata   = { name = "aws-secretsmanager" }
    spec = {
      provider = {
        aws = {
          service = "SecretsManager"
          region  = var.aws_region
          auth    = { jwt = { serviceAccountRef = { name = "ledger-query", namespace = "ledger" } } }
        }
      }
    }
  }
  depends_on = [helm_release.eso, kubernetes_service_account.app]
}

resource "kubernetes_manifest" "app_secret" {
  manifest = {
    apiVersion = "external-secrets.io/v1beta1"
    kind       = "ExternalSecret"
    metadata   = { name = "ledger-query", namespace = "ledger" }
    spec = {
      refreshInterval = "5m"
      secretStoreRef  = { name = "aws-secretsmanager", kind = "ClusterSecretStore" }
      target          = { name = "ledger-query-secrets", creationPolicy = "Owner" }
      data = [
        { secretKey = "DATABASE_URL", remoteRef = { key = module.rds.secret_arn, property = "url" } },
        { secretKey = "KAFKA_BROKER", remoteRef = { key = local.msk_secret_arn, property = "bootstrap_brokers" } },
      ]
    }
  }
  depends_on = [kubernetes_manifest.secret_store]
}

resource "kubernetes_deployment" "app" {
  metadata {
    name      = "ledger-query"
    namespace = "ledger"
    labels    = { app = "ledger-query" }
  }
  spec {
    replicas = var.replicas
    selector { match_labels = { app = "ledger-query" } }
    strategy {
      type = "RollingUpdate"
      rolling_update { max_surge = "25%"; max_unavailable = "0" }
    }
    template {
      metadata { labels = { app = "ledger-query" } }
      spec {
        service_account_name = kubernetes_service_account.app.metadata[0].name
        container {
          name  = "ledger-query"
          image = "${aws_ecr_repository.this.repository_url}:${var.image_tag}"
          port { container_port = 8081 }
          env_from { secret_ref { name = "ledger-query-secrets" } }
          env { name = "PORT",     value = "8081" }
          env { name = "GIN_MODE", value = "release" }
          resources {
            requests = { cpu = "200m",  memory = "256Mi" }
            limits   = { cpu = "1000m", memory = "512Mi" }
          }
          liveness_probe {
            http_get { path = "/health"; port = 8081 }
            initial_delay_seconds = 15
            period_seconds        = 10
          }
          readiness_probe {
            http_get { path = "/ready"; port = 8081 }
            initial_delay_seconds = 5
            period_seconds        = 5
          }
        }
        topology_spread_constraint {
          max_skew           = 1
          topology_key       = "topology.kubernetes.io/zone"
          when_unsatisfiable = "DoNotSchedule"
          label_selector { match_labels = { app = "ledger-query" } }
        }
      }
    }
  }
  depends_on = [kubernetes_manifest.app_secret]
}

resource "kubernetes_service" "app" {
  metadata { name = "ledger-query", namespace = "ledger" }
  spec {
    selector = { app = "ledger-query" }
    port { port = 80; target_port = 8081; protocol = "TCP" }
    type = "ClusterIP"
  }
}

resource "kubernetes_ingress_v1" "app" {
  metadata {
    name      = "ledger-query"
    namespace = "ledger"
    annotations = {
      "kubernetes.io/ingress.class"                = "alb"
      "alb.ingress.kubernetes.io/scheme"           = "internet-facing"
      "alb.ingress.kubernetes.io/target-type"      = "ip"
      "alb.ingress.kubernetes.io/healthcheck-path" = "/health"
      "alb.ingress.kubernetes.io/listen-ports"     = "[{\"HTTP\":80},{\"HTTPS\":443}]"
      "alb.ingress.kubernetes.io/ssl-redirect"     = "443"
    }
  }
  spec {
    rule {
      http {
        path {
          path      = "/"
          path_type = "Prefix"
          backend { service { name = "ledger-query"; port { number = 80 } } }
        }
      }
    }
  }
  depends_on = [kubernetes_service.app]
}

resource "kubernetes_horizontal_pod_autoscaler_v2" "app" {
  metadata { name = "ledger-query", namespace = "ledger" }
  spec {
    scale_target_ref { api_version = "apps/v1"; kind = "Deployment"; name = "ledger-query" }
    min_replicas = 2
    max_replicas = 10
    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target { type = "Utilization"; average_utilization = 70 }
      }
    }
  }
}

resource "kubernetes_pod_disruption_budget_v1" "app" {
  metadata { name = "ledger-query", namespace = "ledger" }
  spec {
    max_unavailable = "25%"
    selector { match_labels = { app = "ledger-query" } }
  }
}

# ── Outputs ───────────────────────────────────────────────────────────────────

output "ecr_url"          { value = aws_ecr_repository.this.repository_url }
output "eks_cluster_name" { value = module.eks.cluster_name }
output "rds_endpoint"     { value = module.rds.endpoint }
output "msk_brokers"      { value = local.msk_brokers; description = "MSK brokers from command state" }
output "msk_secret_arn"   { value = local.msk_secret_arn }
