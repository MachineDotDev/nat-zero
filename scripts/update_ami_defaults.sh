#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <owner-account-id> <ami-name-pattern>" >&2
  exit 1
fi

owner_account_id="$1"
ami_name_pattern="$2"

update_variable_default() {
  local file="$1"
  local variable_name="$2"
  local replacement="$3"
  local tmp_file

  tmp_file="$(mktemp)"
  if ! awk -v variable_name="$variable_name" -v replacement="$replacement" '
    BEGIN {
      in_variable = 0
      updated = 0
    }
    $0 ~ "^variable \"" variable_name "\" \\{" {
      in_variable = 1
    }
    in_variable && $1 == "default" {
      sub(/=.*/, "= " replacement)
      in_variable = 0
      updated = 1
    }
    {
      print
    }
    END {
      if (updated == 0) {
        exit 1
      }
    }
  ' "$file" > "$tmp_file"; then
    rm -f "$tmp_file"
    echo "failed to update default for ${variable_name}" >&2
    exit 1
  fi

  mv "$tmp_file" "$file"
}

update_variable_default "variables.tf" "ami_owner_account" "\"${owner_account_id}\""
update_variable_default "variables.tf" "ami_name_pattern" "\"${ami_name_pattern}\""
