# nat-zero

**Scale-to-zero NAT instances for AWS.** Stop paying for NAT when nothing is running.

nat-zero is a Terraform module that brings event-driven, scale-to-zero NAT to your AWS VPCs. When a workload starts in a private subnet, a NAT instance spins up automatically. When the last workload stops, the NAT shuts down and its Elastic IP is released. You pay nothing while idle -- just ~\$0.80/mo for a stopped EBS volume.

Built on [fck-nat](https://fck-nat.dev/) AMIs. Orchestrated by a Go Lambda with a 55 ms cold start. Proven by real integration tests that deploy infrastructure and verify connectivity end-to-end.

```
                         CONTROL PLANE
  ┌──────────────────────────────────────────────────┐
  │  EventBridge ──> Lambda (NAT Orchestrator)       │
  │                    │  start/stop instances        │
  │                    │  allocate/release EIPs       │
  └────────────────────┼─────────────────────────────┘
                       │
          ┌────────────┴────────────┐
          v                         v
   AZ-A (active)             AZ-B (idle)
  ┌──────────────────┐   ┌──────────────────┐
  │ Workloads        │   │ No workloads     │
  │   ↓ route table  │   │ No NAT instance  │
  │ Private ENI      │   │ No EIP           │
  │   ↓              │   │                  │
  │ NAT Instance     │   │ Cost: ~$0.80/mo  │
  │   ↓              │   │ (EBS only)       │
  │ Public ENI + EIP │   │                  │
  │   ↓              │   └──────────────────┘
  │ Internet Gateway │
  └──────────────────┘
```

## Why nat-zero?

AWS NAT Gateway costs a minimum of ~\$36/month per AZ -- even if nothing is using it. fck-nat brings that down to ~\$7-8/month, but the instance and its public IP still run 24/7.

**nat-zero takes it further.** When your private subnets are idle, there's no NAT instance running and no Elastic IP allocated. Your cost drops to the price of a stopped 2 GB EBS volume: about 80 cents a month.

This matters most for:

- **Dev and staging environments** that sit idle nights and weekends
- **CI/CD runners** that spin up for minutes, then disappear for hours
- **Batch and cron workloads** that run periodically
- **Side projects** where every dollar counts

### Cost comparison (per AZ, per month)

| State | nat-zero | fck-nat | NAT Gateway |
|-------|----------|---------|-------------|
| **Idle** (no workloads) | **~\$0.80** | ~\$7-8 | ~\$36+ |
| **Active** (workloads running) | ~\$7-8 | ~\$7-8 | ~\$36+ |

The key: nat-zero **releases the Elastic IP when idle**, avoiding the [\$3.60/month public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/) that fck-nat and NAT Gateway pay around the clock.

## How it works

An EventBridge rule watches for EC2 instance state changes in your VPC. A Lambda function reacts to each event:

- **Workload starts** in a private subnet -- Lambda creates (or restarts) a NAT instance in that AZ and attaches an Elastic IP
- **Last workload stops** in an AZ -- Lambda stops the NAT instance and releases the Elastic IP
- **NAT instance reaches "running"** -- Lambda attaches an EIP to the public ENI
- **NAT instance reaches "stopped"** -- Lambda detaches and releases the EIP

Each NAT instance uses two persistent ENIs (public + private) pre-created by Terraform. They survive stop/start cycles, so route tables stay intact and there's no need to reconfigure anything when a NAT comes back.

See [Architecture](ARCHITECTURE.md) for detailed event flows and sequence diagrams.

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

  tags = {
    Environment = "dev"
  }
}
```

See [Examples](EXAMPLES.md) for complete working configurations including spot instances, custom AMIs, and building from source.

## Performance

The orchestrator Lambda is written in Go and compiled to a native ARM64 binary. It was rewritten from Python to eliminate cold start overhead -- init latency dropped from 667 ms to 55 ms, a **90% improvement**. Peak memory usage went from 98 MB down to 30 MB.

| Scenario | Time to connectivity |
|----------|---------------------|
| First workload in AZ (cold create) | ~15 seconds |
| NAT already running | Instant |
| Restart from stopped | ~12 seconds |

The ~15 second cold-create time is dominated by EC2 instance boot and fck-nat AMI configuration -- not the Lambda. Subsequent workloads in the same AZ get connectivity immediately since the route table already points to the running NAT.

See [Performance](PERFORMANCE.md) for detailed Lambda execution timings, instance type guidance, and cost breakdowns.

## Tested against real infrastructure

nat-zero isn't just unit-tested -- it's integration-tested against real AWS infrastructure on every PR. The test suite uses [Terratest](https://terratest.gruntwork.io/) to:

1. Deploy the full module (Lambda, EventBridge, ENIs, security groups, launch templates)
2. Launch a workload instance and verify NAT creation with EIP
3. Verify the workload's egress IP matches the NAT's Elastic IP
4. Terminate the workload and verify NAT scale-down and EIP release
5. Launch a new workload and verify NAT restart
6. Run the cleanup action and verify all resources are removed
7. Tear down everything with `terraform destroy`

The full lifecycle takes about 5 minutes in CI. See [Testing](TESTING.md) for phase-by-phase documentation.

## When to use this module

| Use case | nat-zero | fck-nat | NAT Gateway |
|----------|----------|---------|-------------|
| Dev/staging with intermittent workloads | **Best fit** | Wasteful | Very wasteful |
| Production 24/7 workloads | Overkill | **Best fit** | Simplest |
| Cost-sensitive environments | **Best fit** | Good | Expensive |
| Simplicity priority | More moving parts | **Simpler** | Simplest |

**Use nat-zero** when your private subnet workloads run intermittently and you want to pay nothing when idle.

**Use fck-nat** when workloads run 24/7 and you want simplicity with ASG self-healing.

**Use NAT Gateway** when you prioritize managed simplicity and availability over cost.

## Important notes

- **EventBridge scope**: The rule captures all EC2 state changes in the account. The Lambda filters by VPC ID, so it only acts on instances in your target VPC.
- **Startup delay**: The first workload in an idle AZ waits ~15 seconds for internet. Design startup scripts to retry outbound connections -- most package managers already do.
- **Dual ENI**: Each AZ gets persistent public + private ENIs that survive instance stop/start cycles.
- **Dead letter queue**: Failed Lambda invocations go to an SQS DLQ for debugging.
- **Clean destroy**: A cleanup action terminates Lambda-created NAT instances before Terraform removes ENIs, ensuring clean `terraform destroy`.

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.

## License

MIT
