# NAT Zero AMI (arm64, AL2023 minimal)

This directory contains the Packer build for the nat-zero AMI.

## Supported Flavor

- Architecture: `arm64`
- Base image: Amazon Linux 2023 minimal
- Runtime model: deterministic dual ENI (`ens5` public, `ens6` private)

## Runtime Design Constraints

- No IMDS calls in bootstrap/runtime NAT scripts
- No `aws` CLI calls in bootstrap/runtime NAT scripts
- No runtime ENI attach/detach or EIP association logic
- Small, readable bootstrap and NAT config scripts
- `fck-nat`-style bootstrap discovery is intentionally avoided because nat-zero relies on launch-template-owned ENIs and attaches the EIP later in the reconciliation loop
- Unencrypted AMI backing snapshot so the image can be made public; the module can still encrypt runtime NAT instance volumes
- Build-time OS patching via `dnf upgrade --refresh` before the AMI is created

## Build

1. Choose a public subnet ID in the target region.
2. Build with Packer:

```bash
cd ami
packer init nat-zero.pkr.hcl
packer build \
  -var-file "nat-zero-private-all-regions.pkrvars.hcl" \
  -var "region=us-east-1" \
  -var "subnet_id=subnet-0123456789abcdef0" \
  nat-zero.pkr.hcl
```

The AMI name format is:

- `nat-zero-al2023-minimal-arm64-<timestamp>`

This full AMI name is used as the module default target for deterministic rollout.

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
  - builds a new nat-zero AMI with Packer
  - uses `nat-zero-private-all-regions.pkrvars.hcl` as the checked-in list of private regional copies
  - runs integration tests against the new us-east-1 AMI before any public sharing
  - publishes the copied AMIs only after the integration gates pass
- updates `ami_owner_account`, `ami_name_pattern` (and generated docs) and opens a PR

Merge the promotion PR to `main` to let release-please publish a new module release that points to the promoted AMI name.
