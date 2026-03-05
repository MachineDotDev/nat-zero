packer {
  required_plugins {
    amazon = {
      source  = "github.com/hashicorp/amazon"
      version = ">= 1.2.0"
    }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "subnet_id" {
  type = string
}

variable "ami_name_prefix" {
  type    = string
  default = "nat-zero-al2023-minimal-arm64"
}

variable "root_volume_size" {
  type    = number
  default = 4
}

source "amazon-ebs" "nat_zero" {
  ami_name      = "${var.ami_name_prefix}-${formatdate("YYYYMMDD-hhmmss", timestamp())}"
  instance_type = "t4g.nano"
  region        = var.region
  subnet_id     = var.subnet_id
  ssh_username  = "ec2-user"

  launch_block_device_mappings {
    device_name           = "/dev/xvda"
    volume_size           = var.root_volume_size
    volume_type           = "gp3"
    delete_on_termination = true
    encrypted             = true
  }

  source_ami_filter {
    filters = {
      name                = "al2023-ami-minimal-*-kernel-*-arm64"
      root-device-type    = "ebs"
      virtualization-type = "hvm"
    }
    most_recent = true
    owners      = ["amazon"]
  }

  tags = {
    Name         = "nat-zero-first-party"
    Project      = "nat-zero"
    Role         = "nat"
    ManagedBy    = "packer"
    OS           = "al2023-minimal"
    Architecture = "arm64"
  }
}

build {
  name    = "nat-zero-first-party"
  sources = ["source.amazon-ebs.nat_zero"]

  provisioner "file" {
    source      = "files/snat.sh"
    destination = "/tmp/snat.sh"
  }

  provisioner "file" {
    source      = "files/snat.service"
    destination = "/tmp/snat.service"
  }

  provisioner "shell" {
    execute_command = "sudo -E sh -eux '{{ .Path }}'"
    script          = "scripts/install-deps.sh"
  }

  provisioner "shell" {
    execute_command = "sudo -E sh -eux '{{ .Path }}'"
    script          = "scripts/configure.sh"
  }
}
