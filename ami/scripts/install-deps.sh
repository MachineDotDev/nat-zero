#!/bin/sh
set -eu

# Always bake from a fully patched AL2023 base so each AMI includes the
# latest published OS-level fixes available at build time.
dnf -y upgrade --refresh
dnf -y install iptables
dnf clean all
rm -rf /var/cache/dnf
