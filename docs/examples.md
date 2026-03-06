# Examples

## Basic Usage

The simplest way to get started: create a VPC with public and private subnets, then drop in nat-zero. Your private subnets get internet access when workloads are running, and you pay nothing when they're not.

```hcl
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
  source = "github.com/MachineDotDev/nat-zero"

  name               = "example-nat"
  vpc_id             = module.vpc.vpc_id
  availability_zones = local.azs
  public_subnets     = module.vpc.public_subnets
  private_subnets    = module.vpc.private_subnets

  private_route_table_ids     = module.vpc.private_route_table_ids
  private_subnets_cidr_blocks = module.vpc.private_subnets_cidr_blocks

  # Defaults: t4g.nano, promoted public nat-zero AMI track, on-demand
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
```

The full source is available at [`examples/basic/main.tf`](https://github.com/MachineDotDev/nat-zero/blob/main/examples/basic/main.tf).

## Spot Instances

To use spot instances (typically 60-70% cheaper than on-demand):

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  # ... required variables ...

  market_type = "spot"
}
```

## Custom AMI

To use your own NAT Zero-compatible AMI instead of the default public nat-zero AMI:

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  # ... required variables ...

  ami_owner_account = "123456789012"
  ami_name_pattern  = "my-nat-ami-*"
}
```

Or specify an AMI ID directly:

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  # ... required variables ...

  ami_id = "ami-0123456789abcdef0"
}
```

Custom AMIs must preserve nat-zero's deterministic dual-ENI boot model. `fck-nat` AMIs are not compatible because they query IMDS/AWS during bootstrap to infer ENI attachment, while nat-zero relies on the launch template ENIs being known up front and the EIP being attached later by the reconciler.

## Disable Root Volume Encryption

The root EBS volume is encrypted by default. To disable encryption (e.g., for environments without compliance requirements):

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  # ... required variables ...

  encrypt_root_volume = false
}
```

## Building Lambda Locally

For development or if you want to build from source:

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  # ... required variables ...

  build_lambda_locally = true
}
```

Requires Go and `zip` installed locally.
