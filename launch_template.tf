locals {
  # In single-instance mode only AZ[0] gets a NAT (SG/ENIs/launch template);
  # routes for every private route table all point at that one private ENI.
  nat_azs   = var.single_instance ? [var.availability_zones[0]] : var.availability_zones
  nat_count = length(local.nat_azs)

  common_tags = merge(
    {
      Name = var.name
    },
    var.tags,
  )
}

resource "aws_launch_template" "nat_launch_template" {
  count         = local.nat_count
  name          = "${var.name}-${local.nat_azs[count.index]}-launch-template"
  instance_type = var.instance_type
  image_id      = local.effective_ami_id

  iam_instance_profile {
    arn = aws_iam_instance_profile.nat_instance_profile.arn
  }

  block_device_mappings {
    device_name = "/dev/xvda"

    ebs {
      volume_size = var.block_device_size
      volume_type = "gp3"
      iops        = var.block_device_iops
      throughput  = var.block_device_throughput
      encrypted   = var.encrypt_root_volume
    }
  }

  dynamic "instance_market_options" {
    for_each = var.market_type == "spot" ? [1] : []
    content {
      market_type = "spot"
      spot_options {
        spot_instance_type             = "one-time"
        instance_interruption_behavior = "terminate"
      }
    }
  }

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  network_interfaces {
    network_interface_id  = aws_network_interface.nat_public_network_interface[count.index].id
    device_index          = 0
    delete_on_termination = false
  }

  network_interfaces {
    device_index          = 1
    network_interface_id  = aws_network_interface.nat_private_network_interface[count.index].id
    delete_on_termination = false
  }

  tag_specifications {
    resource_type = "instance"
    tags = merge(
      local.common_tags,
      {
        (var.nat_tag_key) = var.nat_tag_value,
        Name              = "${var.name}-${local.nat_azs[count.index]}-nat-instance"
      },
    )
  }

  description = "Launch template for NAT instance ${var.name} in ${local.nat_azs[count.index]}"
  tags = merge(
    {
      AvailabilityZone = local.nat_azs[count.index],
      VpcId            = var.vpc_id,
    },
    local.common_tags,
  )

  lifecycle {
    precondition {
      condition     = local.effective_ami_id != null
      error_message = "Set ami_id or configure a resolvable AMI source with ami_owner_account and ami_name_pattern."
    }
  }
}
