#!/usr/bin/env bash
# 公式の globalip ロール(AWS の checkip で IP を得る)の代わり。固定 IP(10.99.0.x)を足し、
# ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS を s1 の固定 IP に書く(ベンチの入口は s1)。
set -eu
/opt/common-net.sh
sed -Ei 's/^ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=.+$/ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS="10.99.0.11"/' /home/isucon/env.sh
chmod 0755 /home/isucon/env.sh
chown isucon:isucon /home/isucon/env.sh
