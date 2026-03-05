#!/bin/sh
set -eu

dnf -y update
dnf -y install iptables
