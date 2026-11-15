#!/usr/bin/env bash
# 公式の globalip ロール(AWS の checkip で IP を得る)の代わり。VM の eth0 の IPv4 を env.sh に書く。
set -eu
ip=""
for i in $(seq 1 50); do
  ip=$(ip -4 -o addr show eth0 | awk '{print $4}' | cut -d/ -f1 | head -n1)
  [ -n "$ip" ] && break
  sleep 0.2
done
[ -n "$ip" ] || { echo "eth0 has no IPv4" >&2; exit 1; }
sed -Ei 's/^ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=.+$/ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS="'"$ip"'"/' /home/isucon/env.sh
chmod 0755 /home/isucon/env.sh
chown isucon:isucon /home/isucon/env.sh
