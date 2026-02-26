output "lambda_function_arn" {
  description = "ARN of the nat-zero Lambda function"
  value       = aws_lambda_function.nat_zero.arn
  depends_on  = [time_sleep.eventbridge_propagation]
}

output "lambda_function_name" {
  description = "Name of the nat-zero Lambda function"
  value       = aws_lambda_function.nat_zero.function_name
  depends_on  = [time_sleep.eventbridge_propagation]
}

output "nat_security_group_ids" {
  description = "Security group IDs for NAT instances (one per AZ)"
  value       = aws_security_group.nat_security_group[*].id
}

output "nat_public_eni_ids" {
  description = "Public ENI IDs for NAT instances (one per AZ)"
  value       = aws_network_interface.nat_public_network_interface[*].id
}

output "nat_private_eni_ids" {
  description = "Private ENI IDs for NAT instances (one per AZ)"
  value       = aws_network_interface.nat_private_network_interface[*].id
}

output "launch_template_ids" {
  description = "Launch template IDs for NAT instances (one per AZ)"
  value       = aws_launch_template.nat_launch_template[*].id
}

output "eventbridge_rule_arn" {
  description = "ARN of the EventBridge rule capturing EC2 state changes"
  value       = aws_cloudwatch_event_rule.ec2_state_change.arn
  depends_on  = [time_sleep.eventbridge_propagation]
}
