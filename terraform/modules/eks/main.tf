# terraform/modules/eks/main.tf
terraform {
  required_providers {
    aws        = { source = "hashicorp/aws",       version = "~> 5.0" }
    helm       = { source = "hashicorp/helm",      version = "~> 2.14" }
    tls        = { source = "hashicorp/tls",       version = "~> 4.0" }
    kubernetes = { source = "hashicorp/kubernetes", version = "~> 2.30" }
  }
}

variable "cluster_name"      { type = string }
variable "k8s_version"       { type = string, default = "1.30" }
variable "vpc_id"            { type = string }
variable "private_subnets"   { type = list(string) }
variable "instance_type"     { type = string, default = "t3.medium" }
variable "node_min"          { type = number, default = 2 }
variable "node_max"          { type = number, default = 6 }
variable "node_desired"      { type = number, default = 3 }
variable "tags"              { type = map(string), default = {} }

# ── Cluster IAM ──────────────────────────────────────────────────────────────
resource "aws_iam_role" "cluster" {
  name = "${var.cluster_name}-cluster"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "eks.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
  tags = var.tags
}
resource "aws_iam_role_policy_attachment" "cluster" {
  role       = aws_iam_role.cluster.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

# ── EKS Cluster ───────────────────────────────────────────────────────────────
resource "aws_eks_cluster" "this" {
  name     = var.cluster_name
  version  = var.k8s_version
  role_arn = aws_iam_role.cluster.arn
  vpc_config {
    subnet_ids              = var.private_subnets
    endpoint_private_access = true
    endpoint_public_access  = true
  }
  enabled_cluster_log_types = ["api", "audit", "authenticator"]
  tags       = var.tags
  depends_on = [aws_iam_role_policy_attachment.cluster]
}

# ── OIDC provider (IRSA) ──────────────────────────────────────────────────────
data "tls_certificate" "oidc" {
  url = aws_eks_cluster.this.identity[0].oidc[0].issuer
}
resource "aws_iam_openid_connect_provider" "this" {
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.oidc.certificates[0].sha1_fingerprint]
  url             = aws_eks_cluster.this.identity[0].oidc[0].issuer
  tags            = var.tags
}

# ── Node group IAM ────────────────────────────────────────────────────────────
resource "aws_iam_role" "nodes" {
  name = "${var.cluster_name}-nodes"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
  tags = var.tags
}
locals {
  node_policies = [
    "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
    "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
    "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
  ]
}
resource "aws_iam_role_policy_attachment" "nodes" {
  count      = length(local.node_policies)
  role       = aws_iam_role.nodes.name
  policy_arn = local.node_policies[count.index]
}

resource "aws_eks_node_group" "this" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "${var.cluster_name}-ng"
  node_role_arn   = aws_iam_role.nodes.arn
  subnet_ids      = var.private_subnets
  instance_types  = [var.instance_type]
  scaling_config {
    min_size     = var.node_min
    max_size     = var.node_max
    desired_size = var.node_desired
  }
  update_config { max_unavailable = 1 }
  tags       = var.tags
  depends_on = [aws_iam_role_policy_attachment.nodes]
}

# ── ALB controller (IRSA + Helm) ─────────────────────────────────────────────
resource "aws_iam_policy" "alb" {
  name   = "${var.cluster_name}-alb-controller"
  policy = file("${path.module}/alb_policy.json")
}
resource "aws_iam_role" "alb" {
  name = "${var.cluster_name}-alb-controller"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.this.arn }
      Action = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:sub" = "system:serviceaccount:kube-system:aws-load-balancer-controller"
        }
      }
    }]
  })
  tags = var.tags
}
resource "aws_iam_role_policy_attachment" "alb" {
  role       = aws_iam_role.alb.name
  policy_arn = aws_iam_policy.alb.arn
}
resource "helm_release" "alb" {
  name       = "aws-load-balancer-controller"
  repository = "https://aws.github.io/eks-charts"
  chart      = "aws-load-balancer-controller"
  namespace  = "kube-system"
  version    = "1.8.1"
  set { name = "clusterName",  value = var.cluster_name }
  set { name = "serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn", value = aws_iam_role.alb.arn }
  depends_on = [aws_eks_node_group.this]
}

output "cluster_name"      { value = aws_eks_cluster.this.name }
output "cluster_endpoint"  { value = aws_eks_cluster.this.endpoint }
output "cluster_ca"        { value = aws_eks_cluster.this.certificate_authority[0].data }
output "oidc_provider_arn" { value = aws_iam_openid_connect_provider.this.arn }
output "oidc_provider_url" { value = aws_iam_openid_connect_provider.this.url }
