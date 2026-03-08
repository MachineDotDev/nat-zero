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

# Use the default VPC and its subnets as public subnets — no VPC creation needed.
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
  filter {
    name   = "availability-zone"
    values = ["us-east-1a"]
  }
}

data "aws_subnet" "public" {
  id = data.aws_subnets.default.ids[0]
}

# Only create a private subnet + route table — the minimum needed.
resource "aws_subnet" "private" {
  vpc_id            = data.aws_vpc.default.id
  cidr_block        = "172.31.128.0/24"
  availability_zone = data.aws_subnet.public.availability_zone

  tags = {
    Name = "nat-zero-test-private"
  }
}

resource "aws_route_table" "private" {
  vpc_id = data.aws_vpc.default.id

  tags = {
    Name = "nat-zero-test-private"
  }
}

resource "aws_route_table_association" "private" {
  subnet_id      = aws_subnet.private.id
  route_table_id = aws_route_table.private.id
}

variable "nat_instance_type" {
  type    = string
  default = "t4g.nano"
}

variable "encrypt_root_volume" {
  type    = bool
  default = true
}

variable "nat_ami_id" {
  type    = string
  default = null
}

variable "name" {
  type    = string
  default = "nat-test"
}

module "nat_zero" {
  source = "../../../"

  name               = var.name
  vpc_id             = data.aws_vpc.default.id
  availability_zones = [data.aws_subnet.public.availability_zone]
  public_subnets     = [data.aws_subnet.public.id]
  private_subnets    = [aws_subnet.private.id]

  private_route_table_ids     = [aws_route_table.private.id]
  private_subnets_cidr_blocks = [aws_subnet.private.cidr_block]

  instance_type       = var.nat_instance_type
  market_type         = "on-demand"
  encrypt_root_volume = var.encrypt_root_volume
  ami_id              = var.nat_ami_id
  lambda_binary_path  = fileexists("${path.module}/../../.build/lambda.zip") ? abspath("${path.module}/../../.build/lambda.zip") : null
}

output "vpc_id" {
  value = data.aws_vpc.default.id
}

output "private_subnet_id" {
  value = aws_subnet.private.id
}

output "lambda_function_name" {
  value = module.nat_zero.lambda_function_name
}

output "nat_security_group_ids" {
  value = module.nat_zero.nat_security_group_ids
}

output "encrypt_root_volume" {
  value = var.encrypt_root_volume
}
