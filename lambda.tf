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

locals {
  downloaded_lambda_zip_path = "${path.module}/.build/lambda.zip"
  lambda_binary_hash_url     = coalesce(var.lambda_binary_base64sha256_url, "${var.lambda_binary_url}.base64sha256")
  local_lambda_zip_path      = coalesce(var.lambda_binary_path, local.downloaded_lambda_zip_path)
  local_lambda_source_hash = var.lambda_binary_path != null ? (
    coalesce(var.lambda_binary_base64sha256, filebase64sha256(var.lambda_binary_path))
    ) : (
    fileexists(local.downloaded_lambda_zip_path) ? filebase64sha256(local.downloaded_lambda_zip_path) : null
  )
  downloaded_lambda_source_hash = coalesce(
    var.lambda_binary_base64sha256,
    one(data.http.lambda_binary_hash[*].response_body),
    null,
  )
  lambda_source_hash = var.build_lambda_locally ? local.local_lambda_source_hash : trimspace(coalesce(local.downloaded_lambda_source_hash, ""))
}

data "http" "lambda_binary_hash" {
  count = var.build_lambda_locally || var.lambda_binary_path != null || var.lambda_binary_base64sha256 != null ? 0 : 1
  url   = local.lambda_binary_hash_url

  request_headers = {
    Accept = "text/plain"
  }
}

resource "terraform_data" "download_lambda" {
  count = var.build_lambda_locally || var.lambda_binary_path != null ? 0 : 1

  triggers_replace = [
    path.module,
    var.lambda_binary_url,
    local.lambda_binary_hash_url,
    trimspace(coalesce(local.downloaded_lambda_source_hash, "")),
  ]

  provisioner "local-exec" {
    command = <<-EOT
      mkdir -p "${path.module}/.build" && \
      curl -sfL -o "${local.downloaded_lambda_zip_path}" "${var.lambda_binary_url}"
    EOT
  }
}

resource "null_resource" "build_lambda" {
  count = var.build_lambda_locally ? 1 : 0

  triggers = {
    module_path = path.module
    source_hash = sha256(join("", [
      for f in sort(concat(
        tolist(fileset("${path.module}/cmd/lambda", "*.go")),
        ["go.mod", "go.sum"],
      )) :
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
  filename                       = local.local_lambda_zip_path
  function_name                  = "${var.name}-nat-zero"
  handler                        = "bootstrap"
  role                           = aws_iam_role.lambda_iam_role.arn
  runtime                        = "provided.al2023"
  source_code_hash               = local.lambda_source_hash
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

  lifecycle {
    precondition {
      condition     = !(var.build_lambda_locally && var.lambda_binary_path != null)
      error_message = "build_lambda_locally and lambda_binary_path cannot be used together."
    }
  }

  depends_on = [time_sleep.lambda_ready, terraform_data.download_lambda, null_resource.build_lambda]
}

resource "aws_lambda_function_event_invoke_config" "nat_zero_invoke_config" {
  function_name          = aws_lambda_function.nat_zero.function_name
  maximum_retry_attempts = 2
}
