locals {
  common_tags = merge(
    {
      Name = var.name
    },
    var.tags,
  )
}

resource "aws_launch_template" "nat_launch_template" {
  count         = length(var.availability_zones)
  name          = "${var.name}-${var.availability_zones[count.index]}-launch-template"
  instance_type = var.instance_type
  image_id      = local.has_explicit_ami_id ? local.explicit_ami_id : null

  iam_instance_profile {
    arn = aws_iam_instance_profile.nat_instance_profile.arn
  }

  block_device_mappings {
    device_name = "/dev/xvda"

    ebs {
      volume_size = var.block_device_size
      volume_type = "gp3"
      iops        = 3000
      throughput  = 250
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
        Name              = "${var.name}-${var.availability_zones[count.index]}-nat-instance"
      },
    )
  }

  description = "Launch template for NAT instance ${var.name} in ${var.availability_zones[count.index]}"
  tags = merge(
    {
      AvailabilityZone = var.availability_zones[count.index],
      VpcId            = var.vpc_id,
    },
    local.common_tags,
  )
}
