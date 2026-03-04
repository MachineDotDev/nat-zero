#!/bin/sh
set -eu

NAT_PUBLIC_IFACE="${NAT_PUBLIC_IFACE:-ens5}"
NAT_PRIVATE_IFACE="${NAT_PRIVATE_IFACE:-ens6}"

if ! ip link show "$NAT_PUBLIC_IFACE" >/dev/null 2>&1; then
  echo "Missing expected public interface: $NAT_PUBLIC_IFACE" >&2
  exit 1
fi

if ! ip link show "$NAT_PRIVATE_IFACE" >/dev/null 2>&1; then
  echo "Missing expected private interface: $NAT_PRIVATE_IFACE" >&2
  exit 1
fi

cat >/etc/sysctl.d/99-nat.conf <<'EOF_SYSCTL'
net.ipv4.ip_forward = 1
EOF_SYSCTL
sysctl --system >/dev/null

cat >/etc/sysconfig/iptables <<EOF_IPTABLES
*filter
:INPUT DROP [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
-A INPUT -i lo -j ACCEPT
-A INPUT -i $NAT_PRIVATE_IFACE -j ACCEPT
-A INPUT -i $NAT_PUBLIC_IFACE -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A FORWARD -i $NAT_PRIVATE_IFACE -o $NAT_PUBLIC_IFACE -j ACCEPT
-A FORWARD -i $NAT_PUBLIC_IFACE -o $NAT_PRIVATE_IFACE -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
COMMIT

*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
-A POSTROUTING -o $NAT_PUBLIC_IFACE -j MASQUERADE
COMMIT
EOF_IPTABLES

iptables-restore < /etc/sysconfig/iptables
