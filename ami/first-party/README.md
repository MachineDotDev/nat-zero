# First-Party NAT AMI (arm64, AL2023 minimal)

This directory contains the Packer build for the nat-zero first-party NAT AMI.

## Supported Flavor

- Architecture: `arm64`
- Base image: Amazon Linux 2023 minimal
- Runtime model: deterministic dual ENI (`ens5` public, `ens6` private)

## Runtime Design Constraints

- No IMDS calls in bootstrap/runtime NAT scripts
- No `aws` CLI calls in bootstrap/runtime NAT scripts
- No runtime ENI attach/detach or EIP association logic
- Small, readable bootstrap and NAT config scripts

## Build

1. Choose a public subnet ID in the target region.
2. Build with Packer:

```bash
cd ami/first-party
packer init nat-zero.pkr.hcl
packer build \
  -var "region=us-east-1" \
  -var "subnet_id=subnet-0123456789abcdef0" \
  nat-zero.pkr.hcl
```

The AMI name format is:

- `nat-zero-al2023-minimal-arm64-<timestamp>`

This full AMI name is used as the module default for deterministic first-party rollout.

## Copy To Enabled Regions

After building in a source region, copy the AMI to all currently enabled regions:

```bash
./ami/first-party/scripts/copy-to-enabled-regions.sh \
  --source-ami-id ami-0123456789abcdef0 \
  --source-region us-east-1
```

Use `--wait` to block until all destination AMIs are available.

## Promote As Module Default

After integration validation and region copies are complete, promote the AMI name in module defaults:

```bash
./ami/first-party/scripts/promote-default-ami.sh \
  --source-ami-id ami-0123456789abcdef0 \
  --source-region us-east-1
```

This updates:

- `variables.tf` (`first_party_ami_name_pattern` default)
- `cmd/lambda/main.go` fallback for standalone Lambda runs
- user-facing AMI example snippets in docs

## GitHub Workflows

### Copy AMI To Enabled Regions

Workflow: `.github/workflows/ami-copy-enabled-regions.yml`

- Requires GitHub environment secret `AMI_PUBLISH_ROLE_ARN`
- Uses OIDC via `aws-actions/configure-aws-credentials`
- Inputs:
  - `source_ami_id`
  - `source_region` (default `us-east-1`)
  - `wait_for_available`
  - `dry_run`

### Promote AMI + Release PR

Workflow: `.github/workflows/ami-promote-release.yml`

- Requires GitHub environment secret `AMI_PUBLISH_ROLE_ARN`
- Uses OIDC via `aws-actions/configure-aws-credentials`
- Inputs:
  - `source_ami_id`
  - `source_region` (default `us-east-1`)
- Behavior:
  - pins module default AMI name to the provided AMI
  - refreshes terraform-docs output
  - creates a PR with conventional commit title: `feat(ami): ...`

Merge that PR to `main` to let release-please publish a new module release that points to the promoted AMI name.
