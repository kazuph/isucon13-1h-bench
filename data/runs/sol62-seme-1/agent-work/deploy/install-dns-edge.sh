#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
artifact=${1:?s1 build path required}
scp -q deploy/dns-edge.service deploy/pdns-backend.conf deploy/provision-dns-edge.sh     deploy/enable-dns-refresh.sh s1:/tmp/
scp -q deploy/enable-dns-refresh.sh s2:/tmp/
ssh s1 'bash /tmp/provision-dns-edge.sh'
ssh s1 'cd webapp/go && mv dnsedge.next dnsedge && sudo install -m 644 /tmp/pdns-backend.conf /etc/powerdns/pdns.d/zz-isupipe-edge.conf && sudo systemctl restart pdns && sudo systemctl enable --now isupipe-dns-edge'
ssh s1 'for attempt in 1 2 3 4 5; do
    if dig +short +tries=1 +time=1 @127.0.0.1 pipe.u.isucon.dev A | grep -Fxq 10.99.0.11; then
        exit 0
    fi
    sleep 1
done
exit 1'
for server in s1 s2; do
    ssh s1 'cat ~/.isupipe-dns-refresh-token' |
        ssh "$server" 'sudo bash /tmp/enable-dns-refresh.sh'
done
bash deploy/install-build.sh "$artifact" s2
