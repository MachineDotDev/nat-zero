# nat-zero

**Scale-to-zero NAT instances for AWS.** Stop paying for NAT when nothing is running.

nat-zero is a Terraform module that replaces always-on NAT with on-demand NAT instances. When a workload launches in a private subnet, a NAT instance starts automatically. When the last workload stops, the NAT shuts down and its Elastic IP is released. Idle cost: ~$0.80/month per AZ.

By default, nat-zero uses a first-party AMI path (arm64 + AL2023 minimal) for deterministic dual-ENI NAT behavior. Custom AMI lookup and explicit AMI ID override are also supported. Orchestrated by a single Go Lambda (~55 ms cold start, 29 MB memory). Integration-tested against real AWS infrastructure on every PR.

## Quick start

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  name               = "my-nat"
  vpc_id             = module.vpc.vpc_id
  availability_zones = ["us-east-1a", "us-east-1b"]
  public_subnets     = module.vpc.public_subnets
  private_subnets    = module.vpc.private_subnets

  private_route_table_ids     = module.vpc.private_route_table_ids
  private_subnets_cidr_blocks = module.vpc.private_subnets_cidr_blocks
}
```

## Cost comparison (per AZ, per month)

| State | nat-zero | fck-nat | NAT Gateway |
|-------|----------|---------|-------------|
| **Idle** (no workloads) | **~$0.80** | ~$7-8 | ~$36+ |
| **Active** (workloads running) | ~$7-8 | ~$7-8 | ~$36+ |

## Learn more

- [Architecture](architecture.md) — reconciliation model, decision matrix, event flows
- [Performance](performance.md) — startup latency, Lambda execution times, cost breakdowns
- [Examples](examples.md) — spot instances, first-party/custom AMIs, building from source
- [Security](security.md) — vulnerability reporting and first-party AMI hardening/cadence guidance
- [Terraform Reference](reference.md) — inputs, outputs, resources
- [Testing](testing.md) — integration test lifecycle and CI
