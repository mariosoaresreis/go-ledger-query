# terraform/modules/rds/main.tf
terraform {
  required_providers {
    aws    = { source = "hashicorp/aws",    version = "~> 5.0" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}

variable "identifier"        { type = string }
variable "vpc_id"            { type = string }
variable "db_subnets"        { type = list(string) }
variable "allowed_sg_ids"    { type = list(string) }
variable "instance_class"    { type = string,  default = "db.t3.medium" }
variable "allocated_storage" { type = number,  default = 100 }
variable "multi_az"          { type = bool,    default = true }
variable "backup_retention"  { type = number,  default = 7 }
variable "db_name"           { type = string }
variable "tags"              { type = map(string), default = {} }

resource "random_password" "this" {
  length  = 32
  special = false
}

resource "aws_db_subnet_group" "this" {
  name       = var.identifier
  subnet_ids = var.db_subnets
  tags       = var.tags
}

resource "aws_security_group" "this" {
  name   = "${var.identifier}-rds"
  vpc_id = var.vpc_id
  ingress {
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = var.allowed_sg_ids
  }
  egress { from_port = 0; to_port = 0; protocol = "-1"; cidr_blocks = ["0.0.0.0/0"] }
  tags = merge(var.tags, { Name = "${var.identifier}-rds" })
}

resource "aws_db_parameter_group" "this" {
  name   = var.identifier
  family = "postgres16"
  parameter { name = "log_min_duration_statement", value = "1000" }
  parameter { name = "shared_preload_libraries",   value = "pg_stat_statements" }
  tags = var.tags
}

resource "aws_db_instance" "this" {
  identifier              = var.identifier
  engine                  = "postgres"
  engine_version          = "16.3"
  instance_class          = var.instance_class
  allocated_storage       = var.allocated_storage
  max_allocated_storage   = var.allocated_storage * 3
  storage_type            = "gp3"
  storage_encrypted       = true
  db_name                 = var.db_name
  username                = "ledger"
  password                = random_password.this.result
  db_subnet_group_name    = aws_db_subnet_group.this.name
  vpc_security_group_ids  = [aws_security_group.this.id]
  parameter_group_name    = aws_db_parameter_group.this.name
  multi_az                = var.multi_az
  backup_retention_period = var.backup_retention
  deletion_protection     = true
  skip_final_snapshot     = false
  final_snapshot_identifier = "${var.identifier}-final"
  performance_insights_enabled = true
  monitoring_interval     = 60
  enabled_cloudwatch_logs_exports = ["postgresql"]
  tags = merge(var.tags, { Name = var.identifier })
}

resource "aws_secretsmanager_secret" "this" {
  name                    = "/${var.identifier}/db"
  recovery_window_in_days = 7
  tags                    = var.tags
}
resource "aws_secretsmanager_secret_version" "this" {
  secret_id = aws_secretsmanager_secret.this.id
  secret_string = jsonencode({
    username = "ledger"
    password = random_password.this.result
    host     = aws_db_instance.this.address
    port     = 5432
    dbname   = var.db_name
    url      = "postgres://ledger:${random_password.this.result}@${aws_db_instance.this.address}:5432/${var.db_name}"
  })
}

output "endpoint"          { value = aws_db_instance.this.address }
output "secret_arn"        { value = aws_secretsmanager_secret.this.arn }
output "security_group_id" { value = aws_security_group.this.id }
