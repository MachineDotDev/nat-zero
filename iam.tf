resource "aws_iam_role" "nat_instance_role" {
  name_prefix = var.name
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_instance_profile" "nat_instance_profile" {
  name_prefix = var.name
  role        = aws_iam_role.nat_instance_role.name
  tags        = local.common_tags
}

resource "aws_iam_role_policy_attachment" "ssm_policy_attachment" {
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
  role       = aws_iam_role.nat_instance_role.name
}

resource "aws_iam_role" "lambda_iam_role" {
  name = "${var.name}-Lambda-IAM-Role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_role_policy" "lambda_iam_policy" {
  role        = aws_iam_role.lambda_iam_role.name
  name_prefix = var.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat([
      {
        Sid    = "EC2ReadOnly"
        Effect = "Allow"
        Action = [
          "ec2:DescribeInstances",
          "ec2:DescribeLaunchTemplates",
          "ec2:DescribeAddresses",
        ]
        Resource = "*"
      },
      {
        Sid    = "EC2RunInstances"
        Effect = "Allow"
        Action = [
          "ec2:RunInstances",
          "ec2:CreateTags",
        ]
        Resource = "*"
      },
      {
        Sid    = "EC2ManageNatInstances"
        Effect = "Allow"
        Action = [
          "ec2:StartInstances",
          "ec2:StopInstances",
          "ec2:TerminateInstances",
        ]
        Resource = "*"
        Condition = {
          StringEquals = {
            "ec2:ResourceTag/${var.nat_tag_key}" = var.nat_tag_value
          }
        }
      },
      {
        Sid    = "EIPManagement"
        Effect = "Allow"
        Action = [
          "ec2:AllocateAddress",
          "ec2:ReleaseAddress",
          "ec2:AssociateAddress",
          "ec2:DisassociateAddress",
        ]
        Resource = "*"
      },
      {
        Sid      = "PassRoleToNatInstance"
        Effect   = "Allow"
        Action   = "iam:PassRole"
        Resource = aws_iam_role.nat_instance_role.arn
      },
      ], var.enable_logging ? [{
        Sid    = "CloudWatchLogs"
        Effect = "Allow"
        Action = [
          "logs:CreateLogStream",
          "logs:PutLogEvents",
        ]
        Resource = "${aws_cloudwatch_log_group.nat_zero_logs[0].arn}:*"
    }] : [])
  })
}

resource "aws_lambda_permission" "allow_ec2_state_change_eventbridge" {
  statement_id  = "AllowExecutionFromEC2StateChangeEventBridge"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.nat_zero.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.ec2_state_change.arn
}
