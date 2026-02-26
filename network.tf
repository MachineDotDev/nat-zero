# The network configuration for the NAT instance
# Each of these resources is deployed one for each AZ, including EIPs, ENIs, and route table entries
resource "aws_security_group" "nat_security_group" {
  count       = length(var.availability_zones)
  name_prefix = "${var.name}-${var.availability_zones[count.index]}-nat-sg"
  vpc_id      = var.vpc_id
  description = "Security group for NAT instance ${var.name}"

  # Allow all traffic from private subnets (NAT must pass all protocols)
  ingress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.private_subnets_cidr_blocks[count.index]]
  }

  # Allow all outbound traffic to the internet
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(
    local.common_tags,
    {
      Name = "${var.name}-${var.availability_zones[count.index]}-nat-instance-sg",
      AZ   = var.availability_zones[count.index],
    },
  )
}

resource "aws_network_interface" "nat_public_network_interface" {
  count             = length(var.availability_zones)
  subnet_id         = var.public_subnets[count.index]
  security_groups   = [aws_security_group.nat_security_group[count.index].id]
  source_dest_check = false
  description       = "Public ENI for NAT instance ${var.name} in ${var.availability_zones[count.index]}"
  tags = merge(
    local.common_tags,
    {
      Name = "${var.name}-${var.availability_zones[count.index]}-nat-public-eni"
    },
  )
  depends_on = [aws_security_group.nat_security_group]
}

resource "aws_network_interface" "nat_private_network_interface" {
  count             = length(var.availability_zones)
  security_groups   = [aws_security_group.nat_security_group[count.index].id]
  subnet_id         = var.private_subnets[count.index]
  source_dest_check = false
  description       = "Private ENI for NAT instance ${var.name} in ${var.availability_zones[count.index]}"
  tags = merge(
    local.common_tags,
    {
      Name = "${var.name}-${var.availability_zones[count.index]}-nat-private-eni"
    },
  )
  depends_on = [aws_security_group.nat_security_group]
}

resource "aws_route" "nat_route" {
  count                  = length(var.availability_zones)
  route_table_id         = var.private_route_table_ids[count.index]
  destination_cidr_block = "0.0.0.0/0"
  network_interface_id   = aws_network_interface.nat_private_network_interface[count.index].id
  depends_on             = [aws_network_interface.nat_private_network_interface]
}

# Cleanup Lambda-created NAT instances and EIPs on terraform destroy.
# These are not Terraform-managed, so they must be removed before the
# ENIs and security groups can be destroyed.
# lifecycle_scope "CRUD" invokes on both create (harmless no-op) and destroy.
#
# Destroy ordering: the cleanup invocation runs while the Lambda function,
# IAM permissions, and log group all still exist. Terraform then destroys
# the Lambda, waits (time_sleep), and finally removes the log group and
# IAM resources. This prevents the cleanup invocation from recreating a
# log group that was already destroyed.
resource "aws_lambda_invocation" "cleanup" {
  function_name   = aws_lambda_function.nat_zero.function_name
  input           = jsonencode({ action = "cleanup" })
  lifecycle_scope = "CRUD"

  depends_on = [
    aws_network_interface.nat_public_network_interface,
    aws_network_interface.nat_private_network_interface,
    aws_cloudwatch_log_group.nat_zero_logs,
    aws_iam_role_policy.lambda_iam_policy,
  ]
}
