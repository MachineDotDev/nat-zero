#!/bin/bash
set -euo pipefail

cat >/usr/local/sbin/nat-zero-fck-nat-guard.sh <<'GUARD_SCRIPT'
#!/bin/bash
set -uo pipefail

LOG_TAG="nat-zero-fck-nat-guard"
IMDS_BASE_URL="http://169.254.169.254/latest"
MAX_ATTEMPTS=15
SLEEP_SECONDS=4

log() {
  local msg="$1"
  logger -t "$LOG_TAG" "$msg"
  echo "$LOG_TAG: $msg"
}

get_imds_token() {
  curl --silent --show-error --fail --connect-timeout 1 --max-time 2 \
    -X PUT "$IMDS_BASE_URL/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60"
}

resolve_public_interface() {
  local token macs_raw mac mac_no_slash device_number iface iface_mac

  token="$(get_imds_token)" || return 1

  macs_raw="$(curl --silent --show-error --fail --connect-timeout 1 --max-time 2 \
    -H "X-aws-ec2-metadata-token: $token" \
    "$IMDS_BASE_URL/meta-data/network/interfaces/macs/")" || return 1

  for mac in $macs_raw; do
    mac_no_slash="${mac%/}"

    device_number="$(curl --silent --show-error --fail --connect-timeout 1 --max-time 2 \
      -H "X-aws-ec2-metadata-token: $token" \
      "$IMDS_BASE_URL/meta-data/network/interfaces/macs/$mac_no_slash/device-number")" || return 1

    if [ "$device_number" != "0" ]; then
      continue
    fi

    for iface in /sys/class/net/*; do
      iface="${iface##*/}"
      if [ "$iface" = "lo" ] || [ ! -f "/sys/class/net/$iface/address" ]; then
        continue
      fi

      iface_mac="$(cat "/sys/class/net/$iface/address" 2>/dev/null || true)"
      if [ "$iface_mac" = "$mac_no_slash" ]; then
        echo "$iface"
        return 0
      fi
    done
  done

  return 1
}

has_snat_rule_for_interface() {
  local iface="$1"

  iptables -t nat -S POSTROUTING 2>/dev/null | awk -v ifn="$iface" '
    $1 == "-A" && $2 == "POSTROUTING" {
      has_if = 0
      has_masq = 0
      for (i = 3; i <= NF; i++) {
        if ($i == "-o" && (i + 1) <= NF && $(i + 1) == ifn) {
          has_if = 1
        }
        if ($i == "-j" && (i + 1) <= NF && $(i + 1) == "MASQUERADE") {
          has_masq = 1
        }
      }
      if (has_if && has_masq) {
        found = 1
      }
    }
    END { exit(found ? 0 : 1) }
  '
}

main() {
  local attempt public_interface

  for attempt in $(seq 1 "$MAX_ATTEMPTS"); do
    public_interface="$(resolve_public_interface 2>/dev/null || true)"
    if [ -z "$public_interface" ]; then
      log "attempt $attempt/$MAX_ATTEMPTS: IMDS or interface lookup not ready"
      sleep "$SLEEP_SECONDS"
      continue
    fi

    log "attempt $attempt/$MAX_ATTEMPTS: resolved public interface $public_interface"

    if ! systemctl restart fck-nat.service; then
      log "attempt $attempt/$MAX_ATTEMPTS: failed restarting fck-nat.service"
      sleep "$SLEEP_SECONDS"
      continue
    fi

    if has_snat_rule_for_interface "$public_interface"; then
      log "SNAT MASQUERADE rule is installed on $public_interface"
      return 0
    fi

    log "attempt $attempt/$MAX_ATTEMPTS: SNAT MASQUERADE rule not present on $public_interface"
    sleep "$SLEEP_SECONDS"
  done

  log "exhausted retries without a valid SNAT rule"
  return 1
}

main "$@"
GUARD_SCRIPT

chmod 0755 /usr/local/sbin/nat-zero-fck-nat-guard.sh

cat >/etc/systemd/system/nat-zero-fck-nat-guard.service <<'GUARD_UNIT'
[Unit]
Description=nat-zero guard for fck-nat IMDS/interface race
Wants=network-online.target fck-nat.service
After=network-online.target fck-nat.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/nat-zero-fck-nat-guard.sh

[Install]
WantedBy=multi-user.target
GUARD_UNIT

systemctl daemon-reload
systemctl enable nat-zero-fck-nat-guard.service
# Run once now, but do not fail cloud-init if retries are exhausted.
systemctl start nat-zero-fck-nat-guard.service || true
