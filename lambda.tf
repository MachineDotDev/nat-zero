resource "aws_cloudwatch_log_group" "nat_zero_logs" {
  count             = var.enable_logging ? 1 : 0
  name              = "/aws/lambda/${var.name}-nat-zero"
  retention_in_days = var.log_retention_days
  tags              = local.common_tags
}

# create_duration:  waits for IAM role propagation before Lambda is created.
# destroy_duration: when logging is enabled, waits for async CloudWatch log
#                   delivery to settle before the log group is deleted.
resource "time_sleep" "lambda_ready" {
  depends_on = [
    aws_cloudwatch_log_group.nat_zero_logs,
    aws_iam_role_policy.lambda_iam_policy,
  ]
  create_duration  = "10s"
  destroy_duration = var.enable_logging ? "10s" : "0s"
}

resource "null_resource" "download_lambda" {
  count = var.build_lambda_locally ? 0 : 1

  triggers = {
    url = var.lambda_binary_url
  }

  provisioner "local-exec" {
    command = "test -f ${path.module}/.build/lambda.zip || (mkdir -p ${path.module}/.build && curl -sfL -o ${path.module}/.build/lambda.zip ${var.lambda_binary_url})"
  }
}

resource "null_resource" "build_lambda" {
  count = var.build_lambda_locally ? 1 : 0

  triggers = {
    source_hash = sha256(join("", [
      for f in sort(fileset("${path.module}/cmd/lambda", "*.go")) :
      filesha256("${path.module}/cmd/lambda/${f}")
    ]))
  }

  provisioner "local-exec" {
    command = <<-EOT
      cd ${path.module}/cmd/lambda && \
      GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -ldflags='-s -w' -o bootstrap && \
      zip lambda.zip bootstrap && \
      mkdir -p ../../.build && \
      cp lambda.zip ../../.build/lambda.zip && \
      rm bootstrap lambda.zip
    EOT
  }
}

resource "aws_lambda_function" "nat_zero" {
  filename                       = "${path.module}/.build/lambda.zip"
  function_name                  = "${var.name}-nat-zero"
  handler                        = "bootstrap"
  role                           = aws_iam_role.lambda_iam_role.arn
  runtime                        = "provided.al2023"
  source_code_hash               = fileexists("${path.module}/.build/lambda.zip") ? filebase64sha256("${path.module}/.build/lambda.zip") : null
  architectures                  = ["arm64"]
  timeout                        = 90
  reserved_concurrent_executions = 1
  memory_size                    = var.lambda_memory_size
  tags                           = local.common_tags

  environment {
    variables = {
      NAT_TAG_KEY      = var.nat_tag_key
      NAT_TAG_VALUE    = var.nat_tag_value
      IGNORE_TAG_KEY   = var.ignore_tag_key
      IGNORE_TAG_VALUE = var.ignore_tag_value
      TARGET_VPC_ID    = var.vpc_id
      CONFIG_VERSION = sha256(join(",", [
        coalesce(local.effective_ami_id, "missing"),
        var.instance_type,
        var.market_type,
        tostring(var.block_device_size),
        tostring(var.encrypt_root_volume),
      ]))
    }
  }

  depends_on = [time_sleep.lambda_ready, null_resource.download_lambda, null_resource.build_lambda]
}

resource "aws_lambda_function_event_invoke_config" "nat_zero_invoke_config" {
  function_name          = aws_lambda_function.nat_zero.function_name
  maximum_retry_attempts = 2
}
