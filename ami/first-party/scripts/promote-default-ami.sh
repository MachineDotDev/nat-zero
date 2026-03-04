#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Pin nat-zero first-party AMI defaults to a specific AMI name.

Usage:
  promote-default-ami.sh --source-ami-id <ami-id> [options]
  promote-default-ami.sh --ami-name <name>

Options:
  --source-ami-id <ami-id>   Source AMI ID used to resolve Name via AWS API
  --source-region <region>   Region for --source-ami-id lookup (default: us-east-1)
  --ami-name <name>          AMI Name to pin directly (skips AWS lookup)
  -h, --help                 Show help

Examples:
  ./promote-default-ami.sh --source-ami-id ami-0123456789abcdef0
  ./promote-default-ami.sh --source-ami-id ami-0123456789abcdef0 --source-region us-east-1
  ./promote-default-ami.sh --ami-name nat-zero-al2023-minimal-arm64-20260304-054741
EOF
}

SOURCE_AMI_ID=""
SOURCE_REGION="us-east-1"
AMI_NAME=""
AMI_NAME_PREFIX="nat-zero-al2023-minimal-arm64-"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-ami-id)
      SOURCE_AMI_ID="${2:-}"
      shift 2
      ;;
    --source-region)
      SOURCE_REGION="${2:-}"
      shift 2
      ;;
    --ami-name)
      AMI_NAME="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$AMI_NAME" && -z "$SOURCE_AMI_ID" ]]; then
  echo "Set either --source-ami-id or --ami-name." >&2
  usage >&2
  exit 2
fi

if [[ -n "$AMI_NAME" && -n "$SOURCE_AMI_ID" ]]; then
  echo "Use only one of --source-ami-id or --ami-name." >&2
  usage >&2
  exit 2
fi

if [[ -n "$SOURCE_AMI_ID" ]]; then
  AMI_NAME="$(aws ec2 describe-images \
    --region "$SOURCE_REGION" \
    --image-ids "$SOURCE_AMI_ID" \
    --query 'Images[0].Name' \
    --output text)"

  AMI_STATE="$(aws ec2 describe-images \
    --region "$SOURCE_REGION" \
    --image-ids "$SOURCE_AMI_ID" \
    --query 'Images[0].State' \
    --output text)"

  AMI_ARCH="$(aws ec2 describe-images \
    --region "$SOURCE_REGION" \
    --image-ids "$SOURCE_AMI_ID" \
    --query 'Images[0].Architecture' \
    --output text)"

  if [[ -z "$AMI_NAME" || "$AMI_NAME" == "None" ]]; then
    echo "Source AMI not found: $SOURCE_AMI_ID in $SOURCE_REGION" >&2
    exit 1
  fi

  if [[ "$AMI_STATE" != "available" ]]; then
    echo "Source AMI must be in state 'available' (got '$AMI_STATE')." >&2
    exit 1
  fi

  if [[ "$AMI_ARCH" != "arm64" ]]; then
    echo "Source AMI architecture must be arm64 (got '$AMI_ARCH')." >&2
    exit 1
  fi
fi

if [[ "$AMI_NAME" != "$AMI_NAME_PREFIX"* ]]; then
  echo "AMI name must start with '$AMI_NAME_PREFIX' (got '$AMI_NAME')." >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

update_file() {
  local file="$1"
  local awk_script="$2"
  local tmp
  tmp="$(mktemp)"
  awk -v ami_name="$AMI_NAME" "$awk_script" "$file" >"$tmp"
  mv "$tmp" "$file"
}

update_file "$ROOT_DIR/variables.tf" '
BEGIN { in_block = 0; updated = 0 }
$0 ~ /^variable "first_party_ami_name_pattern" \{/ { in_block = 1 }
in_block && $0 ~ /^[[:space:]]*default[[:space:]]*=/ {
  sub(/"[^"]*"/, "\"" ami_name "\"")
  updated = 1
}
in_block && $0 ~ /^\}/ { in_block = 0 }
{ print }
END {
  if (!updated) {
    print "Failed to update first_party_ami_name_pattern default in variables.tf" > "/dev/stderr"
    exit 1
  }
}
'

update_file "$ROOT_DIR/README.md" '
BEGIN { updated = 0 }
{
  if ($0 ~ /first_party_ami_name_pattern = "/) {
    sub(/first_party_ami_name_pattern = "[^"]*"/, "first_party_ami_name_pattern = \"" ami_name "\"")
    updated = 1
  }
  print
}
END {
  if (!updated) {
    print "Failed to update first_party_ami_name_pattern example in README.md" > "/dev/stderr"
    exit 1
  }
}
'

update_file "$ROOT_DIR/docs/examples.md" '
BEGIN { updated = 0 }
{
  if ($0 ~ /first_party_ami_name_pattern = "/) {
    sub(/first_party_ami_name_pattern = "[^"]*"/, "first_party_ami_name_pattern = \"" ami_name "\"")
    updated = 1
  }
  print
}
END {
  if (!updated) {
    print "Failed to update first_party_ami_name_pattern example in docs/examples.md" > "/dev/stderr"
    exit 1
  }
}
'

echo "Pinned first-party AMI name: $AMI_NAME"
