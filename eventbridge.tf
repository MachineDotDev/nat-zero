# EventBridge rule for EC2 instance state change.
# These are interpreted by the nat-zero Lambda.
# One of these works across all AZ's.
resource "aws_cloudwatch_event_rule" "ec2_state_change" {
  name        = "${var.name}-ec2-state-changes"
  description = "Capture EC2 state changes for nat-zero ${var.name}"

  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Instance State-change Notification"]
    detail = {
      state = ["pending", "running", "stopping", "stopped", "shutting-down", "terminated"]
    }
  })
}

resource "aws_cloudwatch_event_target" "state_change_lambda_target" {
  rule      = aws_cloudwatch_event_rule.ec2_state_change.name
  target_id = "${var.name}-ec2-state-change-lambda-target"
  arn       = aws_lambda_function.nat_zero.arn

  # Ensure EventBridge stops invoking the Lambda before the destroy-time
  # cleanup invocation runs, preventing late invocations from recreating
  # the CloudWatch log group after Terraform deletes it.
  depends_on = [aws_lambda_invocation.cleanup]

  input_transformer {
    input_paths = {
      instance_id = "$.detail.instance-id"
      state       = "$.detail.state"
    }
    input_template = <<EOF
{
  "instance_id": <instance_id>,
  "state": <state>
}
EOF
  }
}

# Wait for EventBridge target and Lambda permission to propagate.
# AWS EventBridge rules/targets are eventually consistent — events that
# fire within seconds of target creation may be silently dropped.
# See: https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutTargets.html
resource "time_sleep" "eventbridge_propagation" {
  depends_on = [
    aws_cloudwatch_event_target.state_change_lambda_target,
    aws_lambda_permission.allow_ec2_state_change_eventbridge,
  ]
  create_duration = "60s"
}
