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
| [time_sleep.eventbridge_propagation](https://registry.terraform.io/providers/hashicorp/time/latest/docs/resources/sleep) | resource |
| [time_sleep.lambda_ready](https://registry.terraform.io/providers/hashicorp/time/latest/docs/resources/sleep) | resource |
| [aws_ami.selected](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ami) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | Explicit AMI ID to use directly (skips owner/name-pattern AMI lookup) | `string` | `null` | no |
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
