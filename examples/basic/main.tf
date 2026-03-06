terraform {
  required_version = ">= 1.3"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"
}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.0"

  name = "nat-zero-example"
  cidr = "10.0.0.0/16"

  azs             = local.azs
  public_subnets  = ["10.0.1.0/24", "10.0.2.0/24"]
  private_subnets = ["10.0.101.0/24", "10.0.102.0/24"]

  # Do NOT enable NAT gateway -- this module replaces it
  enable_nat_gateway = false
}

module "nat_zero" {
  source = "../../"

  name               = "example-nat"
  vpc_id             = module.vpc.vpc_id
  availability_zones = local.azs
  public_subnets     = module.vpc.public_subnets
  private_subnets    = module.vpc.private_subnets

  private_route_table_ids     = module.vpc.private_route_table_ids
  private_subnets_cidr_blocks = module.vpc.private_subnets_cidr_blocks

  # Defaults: t4g.nano, nat-zero AMI, on-demand
  # Uncomment for spot instances:
  # market_type = "spot"

  tags = {
    Environment = "example"
    ManagedBy   = "terraform"
  }
}

output "lambda_function_name" {
  value = module.nat_zero.lambda_function_name
}

output "nat_security_group_ids" {
  value = module.nat_zero.nat_security_group_ids
}
