# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in this project, please report it through [GitHub Security Advisories](https://github.com/MachineDotDev/nat-zero/security/advisories/new).

Do **not** open a public issue for security vulnerabilities.

We will acknowledge your report within 48 hours and aim to provide a fix within 7 days for critical issues.

## First-Party NAT AMI Security Notes

The optional first-party AMI path (`ami/first-party/`) is intentionally constrained:

- supported flavor: `arm64` + Amazon Linux 2023 minimal only
- deterministic dual-ENI runtime (`ens5` public, `ens6` private)
- no IMDS lookups in NAT runtime scripts
- no `aws` CLI runtime orchestration
- no runtime ENI attach/detach or EIP association logic in the AMI

This keeps the NAT instance data plane simple while nat-zero Lambda remains the control plane.

## Patch Cadence Recommendation

For first-party AMIs:

1. Rebuild and republish at least monthly.
2. Rebuild and republish on an expedited schedule for critical CVEs.
