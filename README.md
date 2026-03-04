# nat-zero

[![Go Tests](https://github.com/MachineDotDev/nat-zero/actions/workflows/go-tests.yml/badge.svg?branch=main)](https://github.com/MachineDotDev/nat-zero/actions/workflows/go-tests.yml)
[![Docs](https://img.shields.io/badge/docs-nat--zero.machine.dev-blue)](https://nat-zero.machine.dev)

**Scale-to-zero NAT instances for AWS.** Stop paying for NAT when nothing is running.

nat-zero is a Terraform module that replaces always-on NAT with on-demand NAT instances. When a workload launches in a private subnet, a NAT instance starts automatically. When the last workload stops, the NAT shuts down and its Elastic IP is released. Idle cost: ~$0.80/month per AZ.

By default, nat-zero uses a first-party AMI path (arm64 + AL2023 minimal) for deterministic dual-ENI NAT behavior. Custom AMI lookup and explicit AMI ID override are also supported. Orchestrated by a single Go Lambda (~55 ms cold start, 29 MB memory). Integration-tested against real AWS infrastructure on every PR.

```
   AZ-A (active)               AZ-B (idle)
  ┌──────────────────┐       ┌──────────────────┐
  │ Workloads        │       │ No workloads     │
  │   ↓ route table  │       │ No NAT instance  │
  │ Private ENI      │       │ No EIP           │
  │   ↓              │       │                  │
  │ NAT Instance     │       │ Cost: ~$0.80/mo  │
  │   ↓              │       │ (EBS only)       │
  │ Public ENI + EIP │       │                  │
  │   ↓              │       └──────────────────┘
  │ Internet Gateway │
  └──────────────────┘
           ▲
  EventBridge → Lambda (reconciler, concurrency=1)
```

## Why nat-zero?

| State | nat-zero | fck-nat | NAT Gateway |
|-------|----------|---------|-------------|
| **Idle** (no workloads) | **~$0.80/mo** | ~$7-8 | ~$36+ |
| **Active** (workloads running) | ~$7-8 | ~$7-8 | ~$36+ |

AWS NAT Gateway costs ~$36/month per AZ even when idle. fck-nat brings that to ~$7-8/month, but the instance and EIP run 24/7. nat-zero releases the Elastic IP when idle, avoiding the [$3.60/month public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/).

Best for dev/staging environments, CI/CD runners, batch jobs, and side projects where workloads run intermittently.

## How it works

An EventBridge rule captures EC2 instance state changes. A Lambda function (concurrency=1, single writer) runs a **reconciliation loop** on each event:

1. **Observe** — query workloads, NAT instances, and EIPs in the AZ
2. **Decide** — compare actual state to desired state
3. **Act** — take at most one mutating action, then return

The event is just a trigger — the reconciler always computes the correct action from current state. With `reserved_concurrent_executions=1`, events are processed sequentially, eliminating race conditions.

| Workloads? | NAT State | Action |
|------------|-----------|--------|
| Yes | None / terminated | Create NAT |
| Yes | Stopped | Start NAT |
| Yes | Stopping | Wait |
| Yes | Running, no EIP | Attach EIP |
| No | Running / pending | Stop NAT |
| No | Stopped, has EIP | Release EIP |
| — | Multiple NATs | Terminate duplicates |

Each NAT uses two persistent ENIs (public + private) created by Terraform. They survive stop/start cycles, keeping route tables intact.

See [Architecture](docs/architecture.md) for the full reconciliation model and event flow diagrams.

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

  tags = { Environment = "dev" }
}
```

See [Examples](docs/examples.md) for spot instances, first-party AMIs, custom AMIs, and building from source.

## AMI Selection

nat-zero AMI selection precedence is deterministic:

1. `ami_id` explicit override
2. custom owner/pattern lookup (`custom_ami_owner`, `custom_ami_name_pattern`)
3. default first-party lookup (`first_party_ami_owner`, `first_party_ami_name_pattern`)

First-party AMI lookup is enabled by default:

```hcl
module "nat_zero" {
  source = "github.com/MachineDotDev/nat-zero"

  # ... required variables ...

  use_first_party_ami = true

  # Defaults target AMIs you publish from ami/first-party:
  # first_party_ami_owner        = "self"
  # first_party_ami_name_pattern = "nat-zero-al2023-minimal-arm64-20260304-054741"
}
```

Supported first-party flavor:

- `arm64`
- Amazon Linux 2023 minimal

Why this exists: in nat-zero's dual-ENI model we observed reliability issues with fck-nat caused by boot-time interface resolution races. The first-party image keeps runtime NAT logic intentionally minimal and deterministic (fixed interface model, no IMDS/aws-cli/runtime ENI or EIP control-plane actions).

`use_fck_nat_ami` remains as a deprecated compatibility variable and must stay `false`.

AMI build assets and build instructions are in [`ami/first-party/README.md`](ami/first-party/README.md).

## Performance

| Scenario | Time to connectivity |
|----------|---------------------|
| First workload (cold create) | ~10.7 s |
| Restart from stopped | ~8.5 s |
| NAT already running | Instant |

The Lambda is a compiled Go ARM64 binary. Cold start: 55 ms. Typical invocation: 400-600 ms. Peak memory: 29 MB. The startup delay is dominated by EC2 instance boot, not the Lambda.

See [Performance](docs/performance.md) for detailed timings and cost breakdowns.

## Notes

- **EventBridge scope**: Captures all EC2 state changes in the account; Lambda filters by VPC ID.
- **Startup delay**: First workload in an idle AZ waits ~10 seconds for internet. Design scripts to retry outbound connections.
- **Dual ENI**: Persistent public + private ENIs survive stop/start cycles.
- **Retries**: Failed Lambda invocations are retried up to 2 times by EventBridge.
- **Clean destroy**: A cleanup action terminates NAT instances before `terraform destroy` removes ENIs.
- **Config versioning**: Changing AMI or instance type auto-replaces NAT instances on next workload event.
- **First-party AMI cadence**: Rebuild and publish first-party AMIs at least monthly, and do expedited rebuilds for critical CVEs.
- **EC2 events only**: Currently nat-zero responds only to EC2 instance state changes. If you have a use case for other event sources (ECS tasks, Lambda, etc.), PRs are welcome.

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
| <a name="provider_aws"></a> [aws](#provider\_aws) | 6.34.0 |
| <a name="provider_null"></a> [null](#provider\_null) | 3.2.4 |
| <a name="provider_time"></a> [time](#provider\_time) | 0.13.1 |

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
| [time_sleep.eventbridge_propagation](https://registry.terraform.io/providers/hashicorp/time/latest/docs/resources/sleep) | resource |
| [time_sleep.lambda_ready](https://registry.terraform.io/providers/hashicorp/time/latest/docs/resources/sleep) | resource |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | Explicit AMI ID to use (overrides AMI lookup entirely) | `string` | `null` | no |
| <a name="input_availability_zones"></a> [availability\_zones](#input\_availability\_zones) | List of availability zones to deploy NAT instances in | `list(string)` | n/a | yes |
| <a name="input_block_device_size"></a> [block\_device\_size](#input\_block\_device\_size) | Size in GB of the root EBS volume | `number` | `10` | no |
| <a name="input_build_lambda_locally"></a> [build\_lambda\_locally](#input\_build\_lambda\_locally) | Build the Lambda binary from Go source instead of downloading a pre-compiled release. Requires Go and zip installed locally. | `bool` | `false` | no |
| <a name="input_custom_ami_name_pattern"></a> [custom\_ami\_name\_pattern](#input\_custom\_ami\_name\_pattern) | AMI name pattern for custom AMI lookup | `string` | `null` | no |
| <a name="input_custom_ami_owner"></a> [custom\_ami\_owner](#input\_custom\_ami\_owner) | AMI owner account ID for custom AMI lookup | `string` | `null` | no |
| <a name="input_enable_logging"></a> [enable\_logging](#input\_enable\_logging) | Create a CloudWatch log group for the Lambda function | `bool` | `true` | no |
| <a name="input_encrypt_root_volume"></a> [encrypt\_root\_volume](#input\_encrypt\_root\_volume) | Encrypt the root EBS volume. | `bool` | `true` | no |
| <a name="input_first_party_ami_name_pattern"></a> [first\_party\_ami\_name\_pattern](#input\_first\_party\_ami\_name\_pattern) | AMI name pattern for first-party AMI lookup. | `string` | `"nat-zero-al2023-minimal-arm64-20260304-054741"` | no |
| <a name="input_first_party_ami_owner"></a> [first\_party\_ami\_owner](#input\_first\_party\_ami\_owner) | AMI owner account for first-party AMI lookup. | `string` | `"self"` | no |
| <a name="input_ignore_tag_key"></a> [ignore\_tag\_key](#input\_ignore\_tag\_key) | Tag key used to mark instances the Lambda should ignore | `string` | `"nat-zero:ignore"` | no |
| <a name="input_ignore_tag_value"></a> [ignore\_tag\_value](#input\_ignore\_tag\_value) | Tag value used to mark instances the Lambda should ignore | `string` | `"true"` | no |
| <a name="input_instance_type"></a> [instance\_type](#input\_instance\_type) | Instance type for the NAT instance | `string` | `"t4g.nano"` | no |
| <a name="input_lambda_binary_url"></a> [lambda\_binary\_url](#input\_lambda\_binary\_url) | URL to the pre-compiled Go Lambda zip. Updated automatically by CI. | `string` | `"https://github.com/MachineDotDev/nat-zero/releases/download/nat-zero-lambda-latest/lambda.zip"` | no |
| <a name="input_lambda_memory_size"></a> [lambda\_memory\_size](#input\_lambda\_memory\_size) | Memory allocated to the Lambda function in MB (also scales CPU proportionally) | `number` | `128` | no |
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
| <a name="input_use_fck_nat_ami"></a> [use\_fck\_nat\_ami](#input\_use\_fck\_nat\_ami) | DEPRECATED: fck-nat AMIs are unsupported. Leave false. | `bool` | `false` | no |
| <a name="input_use_first_party_ami"></a> [use\_first\_party\_ami](#input\_use\_first\_party\_ami) | Use nat-zero first-party AMI lookup (arm64, AL2023 minimal). Enabled by default. | `bool` | `true` | no |
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

Contributions welcome. Please open an issue or submit a pull request.

## License

MIT
