#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <owner-account-id> <ami-name> <source-region> <regions-file>" >&2
  exit 1
fi

owner_account_id="$1"
ami_name="$2"
source_region="$3"
regions_file="$4"

mapfile -t publish_regions < <(awk -F'"' '/"/ {print $2}' "$regions_file")

if [ "${#publish_regions[@]}" -eq 0 ]; then
  echo "no regions found in $regions_file" >&2
  exit 1
fi

source_present=0
for region in "${publish_regions[@]}"; do
  if [ "$region" = "$source_region" ]; then
    source_present=1
    break
  fi
done
if [ "$source_present" -eq 0 ]; then
  publish_regions+=("$source_region")
fi

cleanup() {
  local region

  for region in "${publish_regions[@]}"; do
    aws ec2 enable-image-block-public-access \
      --region "$region" \
      --image-block-public-access-state block-new-sharing >/dev/null
  done

  for region in "${publish_regions[@]}"; do
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
      state="$(
        aws ec2 get-image-block-public-access-state \
          --region "$region" \
          --query 'ImageBlockPublicAccessState' \
          --output text
      )"
      if [ "$state" = "block-new-sharing" ]; then
        break
      fi
      sleep 20
    done
  done
}

trap cleanup EXIT

for region in "${publish_regions[@]}"; do
  aws ec2 disable-image-block-public-access --region "$region" >/dev/null
done

for region in "${publish_regions[@]}"; do
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    state="$(
      aws ec2 get-image-block-public-access-state \
        --region "$region" \
        --query 'ImageBlockPublicAccessState' \
        --output text
    )"
    if [ "$state" = "unblocked" ]; then
      break
    fi
    sleep 20
  done
done

for region in "${publish_regions[@]}"; do
  image_id="$(
    aws ec2 describe-images \
      --region "$region" \
      --owners "$owner_account_id" \
      --filters "Name=name,Values=$ami_name" "Name=state,Values=available" \
      --query 'Images[0].ImageId' \
      --output text
  )"

  if [ -z "$image_id" ] || [ "$image_id" = "None" ]; then
    echo "failed to resolve image for $region" >&2
    exit 1
  fi

  aws ec2 modify-image-attribute \
    --region "$region" \
    --image-id "$image_id" \
    --launch-permission 'Add=[{Group=all}]'
done
