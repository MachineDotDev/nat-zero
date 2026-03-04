#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Copy a source AMI to all currently enabled commercial regions in this account.

Usage:
  copy-to-enabled-regions.sh --source-ami-id <ami-id> [options]

Options:
  --source-region <region>   Source AMI region (default: us-east-1)
  --name <name>              Destination AMI name (default: source AMI name)
  --description <text>       Destination AMI description
  --wait                     Wait for copied AMIs to become available
  --dry-run                  Print planned copies without creating AMIs
  -h, --help                 Show help

Examples:
  ./copy-to-enabled-regions.sh --source-ami-id ami-0123456789abcdef0
  ./copy-to-enabled-regions.sh --source-ami-id ami-0123456789abcdef0 --wait
EOF
}

SOURCE_AMI_ID=""
SOURCE_REGION="us-east-1"
NAME_OVERRIDE=""
DESCRIPTION_OVERRIDE=""
WAIT_FOR_AVAILABLE=0
DRY_RUN=0

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
    --name)
      NAME_OVERRIDE="${2:-}"
      shift 2
      ;;
    --description)
      DESCRIPTION_OVERRIDE="${2:-}"
      shift 2
      ;;
    --wait)
      WAIT_FOR_AVAILABLE=1
      shift
      ;;
    --dry-run)
      DRY_RUN=1
      shift
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

if [[ -z "$SOURCE_AMI_ID" ]]; then
  echo "--source-ami-id is required" >&2
  usage >&2
  exit 2
fi

SOURCE_NAME="$(aws ec2 describe-images \
  --region "$SOURCE_REGION" \
  --image-ids "$SOURCE_AMI_ID" \
  --query 'Images[0].Name' \
  --output text)"

if [[ -z "$SOURCE_NAME" || "$SOURCE_NAME" == "None" ]]; then
  echo "Source AMI not found: $SOURCE_AMI_ID in $SOURCE_REGION" >&2
  exit 1
fi

DEST_NAME="$SOURCE_NAME"
if [[ -n "$NAME_OVERRIDE" ]]; then
  DEST_NAME="$NAME_OVERRIDE"
fi

DEST_DESCRIPTION="Copy of ${SOURCE_AMI_ID} from ${SOURCE_REGION}"
if [[ -n "$DESCRIPTION_OVERRIDE" ]]; then
  DEST_DESCRIPTION="$DESCRIPTION_OVERRIDE"
fi

readarray -t ENABLED_REGIONS < <(
  aws account list-regions \
    --region-opt-status-contains ENABLED ENABLED_BY_DEFAULT \
    --query 'Regions[].RegionName' \
    --output text \
  | tr '\t' '\n' \
  | sed '/^$/d' \
  | sort
)

if [[ "${#ENABLED_REGIONS[@]}" -eq 0 ]]; then
  echo "No enabled regions returned by account:list-regions" >&2
  exit 1
fi

echo "Source AMI: $SOURCE_AMI_ID ($SOURCE_REGION)"
echo "Name: $DEST_NAME"
echo "Enabled regions: ${#ENABLED_REGIONS[@]}"

declare -a CREATED=()

for region in "${ENABLED_REGIONS[@]}"; do
  if [[ "$region" == "$SOURCE_REGION" ]]; then
    echo "[$region] skip (source region)"
    continue
  fi

  existing_id="$(aws ec2 describe-images \
    --region "$region" \
    --owners self \
    --filters \
      "Name=source-image-id,Values=${SOURCE_AMI_ID}" \
      "Name=name,Values=${DEST_NAME}" \
      "Name=state,Values=available" \
    --query 'Images[0].ImageId' \
    --output text 2>/dev/null || true)"

  if [[ -n "$existing_id" && "$existing_id" != "None" ]]; then
    echo "[$region] already exists: $existing_id"
    continue
  fi

  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "[$region] dry-run copy requested"
    continue
  fi

  new_id="$(aws ec2 copy-image \
    --region "$region" \
    --source-region "$SOURCE_REGION" \
    --source-image-id "$SOURCE_AMI_ID" \
    --name "$DEST_NAME" \
    --description "$DEST_DESCRIPTION" \
    --query 'ImageId' \
    --output text)"

  echo "[$region] copy started: $new_id"
  CREATED+=("${region}:${new_id}")
done

if [[ "$WAIT_FOR_AVAILABLE" -eq 1 && "${#CREATED[@]}" -gt 0 ]]; then
  for entry in "${CREATED[@]}"; do
    region="${entry%%:*}"
    ami_id="${entry##*:}"
    echo "[$region] waiting for $ami_id"
    aws ec2 wait image-available --region "$region" --image-ids "$ami_id"
    echo "[$region] available: $ami_id"
  done
fi

echo "Done."
