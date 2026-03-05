variable "name" {
  type        = string
  description = "Name prefix for all resources created by this module"
}

variable "tags" {
  type        = map(string)
  default     = {}
  description = "Additional tags to apply to all resources"
}

variable "vpc_id" {
  type        = string
  description = "The VPC ID where NAT instances will be deployed"
}

variable "availability_zones" {
  type        = list(string)
  description = "List of availability zones to deploy NAT instances in"
}

variable "public_subnets" {
  type        = list(string)
  description = "Public subnet IDs (one per AZ) for NAT instance public ENIs"
}

variable "private_subnets" {
  type        = list(string)
  description = "Private subnet IDs (one per AZ) for NAT instance private ENIs"
}

variable "private_route_table_ids" {
  type        = list(string)
  description = "Route table IDs for the private subnets (one per AZ)"
}

variable "private_subnets_cidr_blocks" {
  type        = list(string)
  description = "CIDR blocks for the private subnets (one per AZ, used in security group rules)"
}

variable "instance_type" {
  type        = string
  default     = "t4g.nano"
  description = "Instance type for the NAT instance"
}

variable "market_type" {
  type        = string
  default     = "on-demand"
  description = "Whether to use spot or on-demand instances"

  validation {
    condition     = contains(["spot", "on-demand"], var.market_type)
    error_message = "Must be 'spot' or 'on-demand'."
  }
}

variable "block_device_size" {
  type        = number
  default     = 10
  description = "Size in GB of the root EBS volume"
}

variable "encrypt_root_volume" {
  type        = bool
  default     = true
  description = "Encrypt the root EBS volume."
}

# AMI configuration
variable "use_fck_nat_ami" {
  type        = bool
  default     = false
  description = "DEPRECATED: fck-nat AMIs are unsupported. Leave false."

  validation {
    condition     = !var.use_fck_nat_ami
    error_message = "fck-nat AMIs are unsupported in this module. Set use_fck_nat_ami = false."
  }
}

variable "ami_id" {
  type        = string
  default     = null
  description = "Explicit AMI ID to use directly (skips owner/name-pattern AMI lookup)"
}

variable "use_first_party_ami" {
  type        = bool
  default     = true
  description = "Use nat-zero first-party AMI lookup (arm64, AL2023 minimal). Enabled by default."

  validation {
    condition = var.use_first_party_ami || (
      (var.ami_id == null ? "" : trimspace(var.ami_id)) != "" ||
      (
        (var.custom_ami_owner == null ? "" : trimspace(var.custom_ami_owner)) != "" &&
        (var.custom_ami_name_pattern == null ? "" : trimspace(var.custom_ami_name_pattern)) != ""
      )
    )
    error_message = "When use_first_party_ami is false, set ami_id or set both custom_ami_owner and custom_ami_name_pattern."
  }
}

variable "first_party_ami_owner" {
  type        = string
  default     = "self"
  description = "AMI owner account for first-party AMI lookup."

  validation {
    condition     = !var.use_first_party_ami || trimspace(var.first_party_ami_owner) != ""
    error_message = "first_party_ami_owner must be non-empty when use_first_party_ami = true."
  }
}

variable "first_party_ami_name_pattern" {
  type        = string
  default     = "nat-zero-al2023-minimal-arm64-20260305-135430"
  description = "AMI name pattern for first-party AMI lookup."

  validation {
    condition     = !var.use_first_party_ami || trimspace(var.first_party_ami_name_pattern) != ""
    error_message = "first_party_ami_name_pattern must be non-empty when use_first_party_ami = true."
  }
}

variable "custom_ami_owner" {
  type        = string
  default     = null
  description = "AMI owner account ID for custom AMI lookup"

  validation {
    condition = (
      (var.custom_ami_owner == null ? "" : trimspace(var.custom_ami_owner)) == "" &&
      (var.custom_ami_name_pattern == null ? "" : trimspace(var.custom_ami_name_pattern)) == ""
      ) || (
      (var.custom_ami_owner == null ? "" : trimspace(var.custom_ami_owner)) != "" &&
      (var.custom_ami_name_pattern == null ? "" : trimspace(var.custom_ami_name_pattern)) != ""
    )
    error_message = "Set both custom_ami_owner and custom_ami_name_pattern, or leave both unset."
  }
}

variable "custom_ami_name_pattern" {
  type        = string
  default     = null
  description = "AMI name pattern for custom AMI lookup"
}

variable "nat_tag_key" {
  type        = string
  default     = "nat-zero:managed"
  description = "Tag key used to identify NAT instances"
}

variable "nat_tag_value" {
  type        = string
  default     = "true"
  description = "Tag value used to identify NAT instances"
}

variable "ignore_tag_key" {
  type        = string
  default     = "nat-zero:ignore"
  description = "Tag key used to mark instances the Lambda should ignore"
}

variable "ignore_tag_value" {
  type        = string
  default     = "true"
  description = "Tag value used to mark instances the Lambda should ignore"
}

variable "lambda_memory_size" {
  type        = number
  default     = 128
  description = "Memory allocated to the Lambda function in MB (also scales CPU proportionally)"

  validation {
    condition     = var.lambda_memory_size >= 128 && var.lambda_memory_size <= 3008
    error_message = "lambda_memory_size must be between 128 and 3008 MB."
  }
}

variable "enable_logging" {
  type        = bool
  default     = true
  description = "Create a CloudWatch log group for the Lambda function"
}

variable "log_retention_days" {
  type        = number
  default     = 14
  description = "CloudWatch log retention in days (only used when enable_logging is true)"
}

variable "build_lambda_locally" {
  type        = bool
  default     = false
  description = "Build the Lambda binary from Go source instead of downloading a pre-compiled release. Requires Go and zip installed locally."
}

variable "lambda_binary_url" {
  type        = string
  default     = "https://github.com/MachineDotDev/nat-zero/releases/download/nat-zero-lambda-latest/lambda.zip"
  description = "URL to the pre-compiled Go Lambda zip. Updated automatically by CI."
}
