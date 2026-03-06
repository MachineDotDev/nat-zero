locals {
  ami_lookup_enabled = var.ami_id == null && var.ami_owner_account != null && var.ami_name_pattern != null
}

data "aws_ami" "nat" {
  count       = local.ami_lookup_enabled ? 1 : 0
  most_recent = true
  owners      = [local.ami_lookup_enabled ? var.ami_owner_account : "000000000000"]

  filter {
    name   = "name"
    values = [local.ami_lookup_enabled ? var.ami_name_pattern : "missing"]
  }

  filter {
    name   = "state"
    values = ["available"]
  }
}

locals {
  effective_ami_id = var.ami_id != null ? var.ami_id : try(data.aws_ami.nat[0].id, null)
}
