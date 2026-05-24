# terraform/modules/msk/main.tf
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

variable "name"            { type = string }
variable "kafka_version"   { type = string, default = "3.6.0" }
variable "instance_type"   { type = string, default = "kafka.m5.large" }
variable "broker_count"    { type = number, default = 3 }
variable "volume_gb"       { type = number, default = 100 }
variable "vpc_id"          { type = string }
variable "private_subnets" { type = list(string) }
variable "allowed_cidrs"   { type = list(string) }
variable "tags"            { type = map(string), default = {} }

resource "aws_security_group" "this" {
  name   = "${var.name}-msk"
  vpc_id = var.vpc_id
  ingress {
    from_port   = 9092
    to_port     = 9094
    protocol    = "tcp"
    cidr_blocks = var.allowed_cidrs
    description = "Kafka from EKS nodes"
  }
  egress { from_port = 0; to_port = 0; protocol = "-1"; cidr_blocks = ["0.0.0.0/0"] }
  tags = merge(var.tags, { Name = "${var.name}-msk" })
}

resource "aws_msk_configuration" "this" {
  name           = var.name
  kafka_versions = [var.kafka_version]
  server_properties = <<-PROPS
    auto.create.topics.enable=false
    default.replication.factor=3
    min.insync.replicas=2
    num.partitions=12
    log.retention.hours=168
    compression.type=lz4
  PROPS
}

resource "aws_cloudwatch_log_group" "msk" {
  name              = "/aws/msk/${var.name}"
  retention_in_days = 30
  tags              = var.tags
}

resource "aws_msk_cluster" "this" {
  cluster_name           = var.name
  kafka_version          = var.kafka_version
  number_of_broker_nodes = var.broker_count

  broker_node_group_info {
    instance_type  = var.instance_type
    client_subnets = slice(var.private_subnets, 0, var.broker_count)
    storage_info {
      ebs_storage_info { volume_size = var.volume_gb }
    }
    security_groups = [aws_security_group.this.id]
  }

  encryption_info {
    encryption_in_transit { client_broker = "TLS_PLAINTEXT"; in_cluster = true }
  }

  configuration_info {
    arn      = aws_msk_configuration.this.arn
    revision = aws_msk_configuration.this.latest_revision
  }

  open_monitoring {
    prometheus {
      jmx_exporter  { enabled_in_broker = true }
      node_exporter { enabled_in_broker = true }
    }
  }

  logging_info {
    broker_logs {
      cloudwatch_logs { enabled = true; log_group = aws_cloudwatch_log_group.msk.name }
    }
  }

  tags = var.tags
}

resource "aws_secretsmanager_secret" "this" {
  name                    = "/${var.name}/kafka"
  recovery_window_in_days = 7
  tags                    = var.tags
}
resource "aws_secretsmanager_secret_version" "this" {
  secret_id = aws_secretsmanager_secret.this.id
  secret_string = jsonencode({
    bootstrap_brokers     = aws_msk_cluster.this.bootstrap_brokers
    bootstrap_brokers_tls = aws_msk_cluster.this.bootstrap_brokers_tls
  })
  depends_on = [aws_msk_cluster.this]
}

output "bootstrap_brokers"     { value = aws_msk_cluster.this.bootstrap_brokers }
output "bootstrap_brokers_tls" { value = aws_msk_cluster.this.bootstrap_brokers_tls }
output "secret_arn"            { value = aws_secretsmanager_secret.this.arn }
output "security_group_id"     { value = aws_security_group.this.id }
