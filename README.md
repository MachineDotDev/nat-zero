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

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed event flows and sequence diagrams.

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

See [docs/EXAMPLES.md](docs/EXAMPLES.md) for complete working configurations including spot instances, custom AMIs, and building from source.

## Performance

The orchestrator Lambda is written in Go and compiled to a native ARM64 binary. It was rewritten from Python to eliminate cold start overhead -- init latency dropped from 667 ms to 55 ms, a **90% improvement**. Peak memory usage went from 98 MB down to 30 MB.

| Scenario | Time to connectivity |
|----------|---------------------|
| First workload in AZ (cold create) | ~15 seconds |
| NAT already running | Instant |
| Restart from stopped | ~12 seconds |

See [docs/PERFORMANCE.md](docs/PERFORMANCE.md) for detailed Lambda execution timings, instance type guidance, and cost breakdowns.

## Tested against real infrastructure

nat-zero isn't just unit-tested -- it's integration-tested against real AWS infrastructure on every PR. The test suite uses [Terratest](https://terratest.gruntwork.io/) to deploy the full module, launch workloads, verify NAT creation and connectivity, exercise scale-down and restart, then tear everything down cleanly.

See [docs/TESTING.md](docs/TESTING.md) for phase-by-phase documentation.

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

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.3 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | >= 5.0 |
| <a name="requirement_null"></a> [null](#requirement\_null) | >= 3.0 |
| <a name="requirement_time"></a> [time](#requirement\_time) | >= 0.9 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | >= 5.0 |
| <a name="provider_null"></a> [null](#provider\_null) | >= 3.0 |
| <a name="provider_time"></a> [time](#provider\_time) | >= 0.9 |

## Modules

No modules.

## Resources

| Name | Type |
|------|------|
| [aws_cloudwatch_event_rule.ec2_state_change](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_event_rule) | resource |
| [aws_cloudwatch_event_target.state_change_lambda_target](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_event_target) | resource |
| [aws_cloudwatch_log_group.nat_zero_logs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudwatch_log_group) | resource |
| [aws_iam_instance_profile.nat_instance_profile](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_instance_profile) | resource |
| [aws_iam_role.lambda_iam_role](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role.nat_instance_role](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role_policy.lambda_iam_policy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws_iam_role_policy_attachment.ssm_policy_attachment](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy_attachment) | resource |
| [aws_lambda_function.nat_zero](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lambda_function) | resource |
| [aws_lambda_function_event_invoke_config.nat_zero_invoke_config](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lambda_function_event_invoke_config) | resource |
| [aws_lambda_invocation.cleanup](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lambda_invocation) | resource |
| [aws_lambda_permission.allow_ec2_state_change_eventbridge](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lambda_permission) | resource |
| [aws_launch_template.nat_launch_template](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/launch_template) | resource |
| [aws_network_interface.nat_private_network_interface](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/network_interface) | resource |
| [aws_network_interface.nat_public_network_interface](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/network_interface) | resource |
| [aws_route.nat_route](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route) | resource |
| [aws_security_group.nat_security_group](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [null_resource.build_lambda](https://registry.terraform.io/providers/hashicorp/null/latest/docs/resources/resource) | resource |
| [null_resource.download_lambda](https://registry.terraform.io/providers/hashicorp/null/latest/docs/resources/resource) | resource |
| [time_sleep.lambda_ready](https://registry.terraform.io/providers/hashicorp/time/latest/docs/resources/sleep) | resource |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | Explicit AMI ID to use (overrides AMI lookup entirely) | `string` | `null` | no |
| <a name="input_availability_zones"></a> [availability\_zones](#input\_availability\_zones) | List of availability zones to deploy NAT instances in | `list(string)` | n/a | yes |
| <a name="input_block_device_size"></a> [block\_device\_size](#input\_block\_device\_size) | Size in GB of the root EBS volume | `number` | `10` | no |
| <a name="input_build_lambda_locally"></a> [build\_lambda\_locally](#input\_build\_lambda\_locally) | Build the Lambda binary from Go source instead of downloading a pre-compiled release. Requires Go and zip installed locally. | `bool` | `false` | no |
| <a name="input_custom_ami_name_pattern"></a> [custom\_ami\_name\_pattern](#input\_custom\_ami\_name\_pattern) | AMI name pattern when use\_fck\_nat\_ami is false | `string` | `null` | no |
| <a name="input_custom_ami_owner"></a> [custom\_ami\_owner](#input\_custom\_ami\_owner) | AMI owner account ID when use\_fck\_nat\_ami is false | `string` | `null` | no |
| <a name="input_enable_logging"></a> [enable\_logging](#input\_enable\_logging) | Create a CloudWatch log group for the Lambda function | `bool` | `true` | no |
| <a name="input_ignore_tag_key"></a> [ignore\_tag\_key](#input\_ignore\_tag\_key) | Tag key used to mark instances the Lambda should ignore | `string` | `"nat-zero:ignore"` | no |
| <a name="input_ignore_tag_value"></a> [ignore\_tag\_value](#input\_ignore\_tag\_value) | Tag value used to mark instances the Lambda should ignore | `string` | `"true"` | no |
| <a name="input_instance_type"></a> [instance\_type](#input\_instance\_type) | Instance type for the NAT instance | `string` | `"t4g.nano"` | no |
| <a name="input_lambda_binary_url"></a> [lambda\_binary\_url](#input\_lambda\_binary\_url) | URL to the pre-compiled Go Lambda zip. Updated automatically by CI. | `string` | `"https://github.com/MachineDotDev/nat-zero/releases/download/nat-zero-lambda-latest/lambda.zip"` | no |
| <a name="input_lambda_memory_size"></a> [lambda\_memory\_size](#input\_lambda\_memory\_size) | Memory allocated to the Lambda function in MB (also scales CPU proportionally) | `number` | `256` | no |
| <a name="input_log_retention_days"></a> [log\_retention\_days](#input\_log\_retention\_days) | CloudWatch log retention in days (only used when enable\_logging is true) | `number` | `14` | no |
| <a name="input_market_type"></a> [market\_type](#input\_market\_type) | Whether to use spot or on-demand instances | `string` | `"on-demand"` | no |
| <a name="input_name"></a> [name](#input\_name) | Name prefix for all resources created by this module | `string` | n/a | yes |
| <a name="input_nat_tag_key"></a> [nat\_tag\_key](#input\_nat\_tag\_key) | Tag key used to identify NAT instances | `string` | `"nat-zero:managed"` | no |
| <a name="input_nat_tag_value"></a> [nat\_tag\_value](#input\_nat\_tag\_value) | Tag value used to identify NAT instances | `string` | `"true"` | no |
| <a name="input_private_route_table_ids"></a> [private\_route\_table\_ids](#input\_private\_route\_table\_ids) | Route table IDs for the private subnets (one per AZ) | `list(string)` | n/a | yes |
| <a name="input_private_subnets"></a> [private\_subnets](#input\_private\_subnets) | Private subnet IDs (one per AZ) for NAT instance private ENIs | `list(string)` | n/a | yes |
| <a name="input_private_subnets_cidr_blocks"></a> [private\_subnets\_cidr\_blocks](#input\_private\_subnets\_cidr\_blocks) | CIDR blocks for the private subnets (one per AZ, used in security group rules) | `list(string)` | n/a | yes |
| <a name="input_public_subnets"></a> [public\_subnets](#input\_public\_subnets) | Public subnet IDs (one per AZ) for NAT instance public ENIs | `list(string)` | n/a | yes |
| <a name="input_tags"></a> [tags](#input\_tags) | Additional tags to apply to all resources | `map(string)` | `{}` | no |
| <a name="input_use_fck_nat_ami"></a> [use\_fck\_nat\_ami](#input\_use\_fck\_nat\_ami) | Use the public fck-nat AMI. Set to false to use a custom AMI. | `bool` | `true` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | The VPC ID where NAT instances will be deployed | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_eventbridge_rule_arn"></a> [eventbridge\_rule\_arn](#output\_eventbridge\_rule\_arn) | ARN of the EventBridge rule capturing EC2 state changes |
| <a name="output_lambda_function_arn"></a> [lambda\_function\_arn](#output\_lambda\_function\_arn) | ARN of the nat-zero Lambda function |
| <a name="output_lambda_function_name"></a> [lambda\_function\_name](#output\_lambda\_function\_name) | Name of the nat-zero Lambda function |
| <a name="output_launch_template_ids"></a> [launch\_template\_ids](#output\_launch\_template\_ids) | Launch template IDs for NAT instances (one per AZ) |
| <a name="output_nat_private_eni_ids"></a> [nat\_private\_eni\_ids](#output\_nat\_private\_eni\_ids) | Private ENI IDs for NAT instances (one per AZ) |
| <a name="output_nat_public_eni_ids"></a> [nat\_public\_eni\_ids](#output\_nat\_public\_eni\_ids) | Public ENI IDs for NAT instances (one per AZ) |
| <a name="output_nat_security_group_ids"></a> [nat\_security\_group\_ids](#output\_nat\_security\_group\_ids) | Security group IDs for NAT instances (one per AZ) |
<!-- END_TF_DOCS -->

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.

## License

MIT
