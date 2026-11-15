#!/bin/bash
# 全 VM 共通: ホスト名で役割を決め、eth0 に固定 IP(2つ目)を足し、/etc/hosts に s1 s2 s3 bench agent を書く。
# Apple container は IP を固定できないため、10.99.0.0/24 の固定アドレスを自前で足している。
set -eu
case "$(hostname)" in
  isu13-s1) IP=10.99.0.11 ;;
  isu13-s2) IP=10.99.0.12 ;;
  isu13-s3) IP=10.99.0.13 ;;
  isu13-bench) IP=10.99.0.200 ;;
  isu13-agent) IP=10.99.0.100 ;;
  *) echo "unknown host $(hostname)" >&2; exit 1 ;;
esac
for i in $(seq 1 50); do ip link show eth0 >/dev/null 2>&1 && break; sleep 0.2; done
ip addr show dev eth0 | grep -q "inet $IP/" || ip addr add "$IP/24" dev eth0
sed -i '/# isucon13-1h-bench/d' /etc/hosts
cat >> /etc/hosts <<EOT
10.99.0.11 s1 # isucon13-1h-bench
10.99.0.12 s2 # isucon13-1h-bench
10.99.0.13 s3 # isucon13-1h-bench
10.99.0.200 bench # isucon13-1h-bench
10.99.0.100 agent # isucon13-1h-bench
EOT
