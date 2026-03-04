locals {
  explicit_ami_id     = var.ami_id == null ? "" : trimspace(var.ami_id)
  has_explicit_ami_id = local.explicit_ami_id != ""

  first_party_ami_owner_account = trimspace(var.first_party_ami_owner)
  first_party_ami_name_pattern  = trimspace(var.first_party_ami_name_pattern)

  custom_ami_owner_account = var.custom_ami_owner == null ? "" : trimspace(var.custom_ami_owner)
  custom_ami_name_pattern  = var.custom_ami_name_pattern == null ? "" : trimspace(var.custom_ami_name_pattern)
  has_custom_ami_lookup    = local.custom_ami_owner_account != "" && local.custom_ami_name_pattern != ""

  selected_ami_owner_account = local.has_custom_ami_lookup ? local.custom_ami_owner_account : (
    var.use_first_party_ami && !var.use_fck_nat_ami ? local.first_party_ami_owner_account : ""
  )
  selected_ami_name_pattern = local.has_custom_ami_lookup ? local.custom_ami_name_pattern : (
    var.use_first_party_ami && !var.use_fck_nat_ami ? local.first_party_ami_name_pattern : ""
  )
}

data "aws_ami" "selected" {
  count       = local.has_explicit_ami_id ? 0 : 1
  owners      = [local.selected_ami_owner_account]
  most_recent = true

  filter {
    name   = "name"
    values = [local.selected_ami_name_pattern]
  }

  filter {
    name   = "state"
    values = ["available"]
  }
}

locals {
  selected_ami_id = local.has_explicit_ami_id ? local.explicit_ami_id : data.aws_ami.selected[0].id
}
