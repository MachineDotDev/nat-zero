# nat-zero

Scale-to-zero NAT instances for AWS. Uses [fck-nat](https://fck-nat.dev/) AMIs. Zero cost when idle.

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

## How It Works

An EventBridge rule captures all EC2 instance state changes. A Lambda function evaluates each event and manages NAT instance lifecycle per-AZ:

- **Workload starts** in a private subnet → Lambda starts (or creates) a NAT instance in the same AZ and attaches an Elastic IP
- **Last workload stops** in an AZ → Lambda stops the NAT instance and releases the Elastic IP
- **NAT instance starts** → Lambda attaches an EIP to the public ENI
- **NAT instance stops** → Lambda detaches and releases the EIP

Each NAT instance uses dual ENIs (public + private) pre-created by Terraform. Traffic from private subnets routes through the private ENI, gets masqueraded via iptables, and exits through the public ENI with an Elastic IP.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed diagrams, [docs/PERFORMANCE.md](docs/PERFORMANCE.md) for timing and cost data, and [docs/TEST.md](docs/TEST.md) for integration test documentation.

## When To Use This Module

| Use Case | This Module | fck-nat | NAT Gateway |
|---|---|---|---|
| Dev/staging with intermittent workloads | **Best fit** | Wasteful | Very wasteful |
| Production 24/7 workloads | Overkill | **Best fit** | Simplest |
| Cost-obsessive environments | **Best fit** | Good | Expensive |
| Simplicity priority | More moving parts | **Simpler** | Simplest |

**Use this module** when your private subnet workloads run intermittently (CI/CD, dev environments, batch jobs) and you want to pay nothing when idle.

**Use fck-nat** when workloads run 24/7 and you want simplicity with ASG self-healing.

**Use NAT Gateway** when you prioritize simplicity and availability over cost.

## Usage

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

See [`examples/basic/`](examples/basic/) for a complete working example.

## Cost Estimate

Per AZ, per month. Accounts for the [AWS public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/) ($0.005/hr per public IP, effective Feb 2024).

| State | This Module | fck-nat | NAT Gateway |
|-------|------------|---------|-------------|
| **Idle** (no workloads) | **~$0.80** (EBS only) | ~$7-8 (instance + EIP) | ~$36+ ($32 gw + $3.60 IP) |
| **Active** (workloads running) | ~$7-8 (instance + EBS + EIP) | ~$7-8 (same) | ~$36+ (+ $0.045/GB) |

Key cost difference: this module **releases the EIP when idle**, avoiding the $3.60/mo public IPv4 charge. fck-nat keeps an EIP attached 24/7.

## Startup Latency

| Scenario | Time to Connectivity |
|----------|---------------------|
| First workload in AZ (cold create) | **~15 seconds** |
| NAT already running | **Instant** |
| Restart from stopped (after idle) | **~12 seconds** |

The first workload instance in an AZ will not have internet access for approximately 15 seconds. Design startup scripts to retry outbound connections. Subsequent instances in the same AZ get connectivity immediately since the route table already points to the running NAT.

See [docs/PERFORMANCE.md](docs/PERFORMANCE.md) for detailed timing breakdowns and instance type benchmarks.

## Important Notes

- **EventBridge scope**: The EventBridge rule captures ALL EC2 state changes in the account. The Lambda filters events by VPC ID, so it only acts on instances in the target VPC.
- **EIP behavior**: An Elastic IP is allocated when a NAT instance starts and released when it stops. You are not charged for EIPs while the NAT instance is stopped.
- **fck-nat AMI**: By default, this module uses the public fck-nat AMI (`568608671756`). You can override this with `use_fck_nat_ami = false` and provide `custom_ami_owner` + `custom_ami_name_pattern`, or set `ami_id` directly.
- **Dual ENI**: Each AZ gets a pair of persistent ENIs (public + private). These survive instance stop/start cycles, preserving route table entries.
- **Dead Letter Queue**: Failed Lambda invocations are sent to an SQS DLQ for debugging.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| aws | >= 5.0 |
| archive | >= 2.0 |

## Providers

| Name | Version |
|------|---------|
| aws | >= 5.0 |
| archive | >= 2.0 |

## Resources

| Name | Type |
|------|------|
| aws_cloudwatch_event_rule.ec2_state_change | resource |
| aws_cloudwatch_event_target.state_change_lambda_target | resource |
| aws_cloudwatch_log_group.nat_zero_logs | resource |
| aws_iam_instance_profile.nat_instance_profile | resource |
| aws_iam_role.lambda_iam_role | resource |
| aws_iam_role.nat_instance_role | resource |
| aws_iam_role_policy.lambda_iam_policy | resource |
| aws_iam_role_policy_attachment.lambda_basic_policy_attachment | resource |
| aws_iam_role_policy_attachment.ssm_policy_attachment | resource |
| aws_lambda_function.nat_zero | resource |
| aws_lambda_function_event_invoke_config.nat_zero_invoke_config | resource |
| aws_lambda_permission.allow_ec2_state_change_eventbridge | resource |
| aws_launch_template.nat_launch_template | resource |
| aws_network_interface.nat_private_network_interface | resource |
| aws_network_interface.nat_public_network_interface | resource |
| aws_route.nat_route | resource |
| aws_security_group.nat_security_group | resource |
| aws_sqs_queue.lambda_dlq | resource |
| archive_file.nat_zero | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| name | Name prefix for all resources | `string` | n/a | yes |
| vpc_id | VPC ID where NAT instances will be deployed | `string` | n/a | yes |
| availability_zones | List of AZs to deploy NAT instances in | `list(string)` | n/a | yes |
| public_subnets | Public subnet IDs (one per AZ) | `list(string)` | n/a | yes |
| private_subnets | Private subnet IDs (one per AZ) | `list(string)` | n/a | yes |
| private_route_table_ids | Route table IDs for private subnets (one per AZ) | `list(string)` | n/a | yes |
| private_subnets_cidr_blocks | CIDR blocks for private subnets (one per AZ) | `list(string)` | n/a | yes |
| tags | Additional tags for all resources | `map(string)` | `{}` | no |
| instance_type | EC2 instance type for NAT instances | `string` | `"t4g.nano"` | no |
| market_type | `"spot"` or `"on-demand"` | `string` | `"on-demand"` | no |
| block_device_size | Root volume size in GB | `number` | `2` | no |
| use_fck_nat_ami | Use the public fck-nat AMI | `bool` | `true` | no |
| ami_id | Explicit AMI ID (overrides lookup) | `string` | `null` | no |
| custom_ami_owner | AMI owner account when not using fck-nat | `string` | `null` | no |
| custom_ami_name_pattern | AMI name pattern when not using fck-nat | `string` | `null` | no |
| nat_tag_key | Tag key to identify NAT instances | `string` | `"nat-zero:managed"` | no |
| nat_tag_value | Tag value to identify NAT instances | `string` | `"true"` | no |
| ignore_tag_key | Tag key to mark instances the Lambda should ignore | `string` | `"nat-zero:ignore"` | no |
| ignore_tag_value | Tag value to mark instances the Lambda should ignore | `string` | `"true"` | no |
| log_retention_days | CloudWatch log retention in days | `number` | `14` | no |

## Outputs

| Name | Description |
|------|-------------|
| lambda_function_arn | ARN of the nat-zero Lambda function |
| lambda_function_name | Name of the nat-zero Lambda function |
| nat_security_group_ids | Security group IDs (one per AZ) |
| nat_public_eni_ids | Public ENI IDs (one per AZ) |
| nat_private_eni_ids | Private ENI IDs (one per AZ) |
| launch_template_ids | Launch template IDs (one per AZ) |
| eventbridge_rule_arn | ARN of the EventBridge rule |
| dlq_arn | ARN of the dead letter queue |

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.

## License

MIT
