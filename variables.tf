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
  default     = true
  description = "Use the public fck-nat AMI. Set to false to use a custom AMI."
}

variable "ami_id" {
  type        = string
  default     = null
  description = "Explicit AMI ID to use (overrides AMI lookup entirely)"
}

variable "custom_ami_owner" {
  type        = string
  default     = null
  description = "AMI owner account ID when use_fck_nat_ami is false"
}

variable "custom_ami_name_pattern" {
  type        = string
  default     = null
  description = "AMI name pattern when use_fck_nat_ami is false"
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
