#!/bin/sh
set -eu

systemctl stop sshd
systemctl disable sshd
systemctl mask sshd
dnf remove -y openssh-server

mkdir -p /opt/nat
install /tmp/snat.sh /opt/nat/snat.sh -m u+rx
cp /tmp/snat.service /etc/systemd/system/snat.service

systemctl daemon-reload
systemctl enable snat
