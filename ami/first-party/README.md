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

This full AMI name is used as the module default target for deterministic first-party rollout.

## GitHub Workflow

Workflow: `.github/workflows/nat-images.yml`

- Requires GitHub environment secret `AMI_BUILD_ROLE_ARN`
- Requires GitHub environment secret `INTEGRATION_ROLE_ARN` when `run_integration_gate=true`
- Uses OIDC via `aws-actions/configure-aws-credentials`
- Inputs:
  - `build_subnet_id`
  - `source_region` (default `us-east-1`)
  - `run_integration_gate` (default `true`)
- Behavior:
  - builds a new first-party AMI with Packer
  - copies it to all currently enabled regions in the account (parallel copy with retries)
  - runs integration tests against the new source AMI (gate) before promotion
  - updates `first_party_ami_name_pattern` (and generated docs) and opens a PR

Merge the promotion PR to `main` to let release-please publish a new module release that points to the promoted AMI name.
