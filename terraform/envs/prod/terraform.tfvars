# terraform/envs/prod/terraform.tfvars
aws_region   = "us-east-1"
project      = "ledger-query"
env          = "prod"
vpc_cidr     = "10.1.0.0/16"   # must not overlap command VPC (10.0.0.0/16)

k8s_version   = "1.30"
instance_type = "t3.medium"
node_min      = 2
node_max      = 6
node_desired  = 3

db_instance_class   = "db.t3.medium"
db_storage          = 100
db_multi_az         = true
db_backup_retention = 7

replicas  = 3
image_tag = "latest"   # overridden by deploy.sh --tag

command_tfstate_bucket = "ledger-command-tfstate"
command_tfstate_key    = "prod/terraform.tfstate"
