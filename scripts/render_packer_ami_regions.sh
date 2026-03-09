#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <regions-file> <source-region> <output-file>" >&2
  exit 1
fi

regions_file="$1"
source_region="$2"
output_file="$3"

mapfile -t configured_regions < <(awk -F'"' '/"/ {print $2}' "$regions_file")

if [ "${#configured_regions[@]}" -eq 0 ]; then
  echo "no regions found in $regions_file" >&2
  exit 1
fi

{
  echo "ami_regions = ["
  for region in "${configured_regions[@]}"; do
    if [ "$region" = "$source_region" ]; then
      continue
    fi
    printf '  "%s",\n' "$region"
  done
  echo "]"
} >"$output_file"
