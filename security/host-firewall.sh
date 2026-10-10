#!/bin/sh
# Blocks the Steward egress bridge (steward-eg) from reaching the host and private
# networks: the bot only needs the public Internet (Discord and feed APIs).
# Installed as /usr/local/sbin/steward-egress-firewall.sh by ops/install-vps.sh and
# run by steward-firewall.service after Docker starts. Idempotent.
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Run as root." >&2
  exit 1
fi

command -v iptables >/dev/null 2>&1 || { echo "iptables is required" >&2; exit 1; }

iface=steward-eg

iptables -C INPUT -i "$iface" -j DROP 2>/dev/null || iptables -I INPUT 1 -i "$iface" -j DROP

iptables -N DOCKER-USER 2>/dev/null || true
for cidr in 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4; do
  iptables -C DOCKER-USER -i "$iface" -d "$cidr" -j REJECT 2>/dev/null ||
    iptables -I DOCKER-USER 1 -i "$iface" -d "$cidr" -j REJECT
done

echo "Steward container-to-host/private-network blocking rules installed."
