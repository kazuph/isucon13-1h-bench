#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
release_dir=$(mktemp -d)
trap 'rm -rf "$release_dir"' EXIT
ssh s2 'mkdir -p /home/isucon/build-memory'
scp go/*.go go/go.mod go/go.sum s2:/home/isucon/build-memory/
ssh s2 'source ~/.profile && cd /home/isucon/build-memory && go test ./... && go build -o /tmp/isupipe-release .'
scp s2:/tmp/isupipe-release "$release_dir/isupipe-memory"
for host in s1 s2 s3; do
 scp "$release_dir/isupipe-memory" "$host:/tmp/isupipe-release"
done
scp deploy/isupipe-dns.service deploy/nginx.conf deploy/nginx-main-s1.conf deploy/nginx-limits.conf s1:/tmp/
scp deploy/isupipe-memory.service s2:/tmp/
scp deploy/isupipe-crypto.service s3:/tmp/
ssh s1 'sudo systemctl disable --now isupipe-go pdns mysql; sudo systemctl stop isupipe-dns; cp /tmp/isupipe-release /home/isucon/webapp/go/isupipe-memory; sudo cp /tmp/isupipe-dns.service /etc/systemd/system/; sudo cp /tmp/nginx.conf /etc/nginx/sites-enabled/isupipe.conf; sudo cp /tmp/nginx-main-s1.conf /etc/nginx/nginx.conf; sudo mkdir -p /etc/systemd/system/nginx.service.d; sudo cp /tmp/nginx-limits.conf /etc/systemd/system/nginx.service.d/limits.conf; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-dns; sudo nginx -t; sudo systemctl restart nginx'
ssh s3 'sudo systemctl disable --now isupipe-go pdns mysql; sudo systemctl stop isupipe-crypto; cp /tmp/isupipe-release /home/isucon/webapp/go/isupipe-memory; sudo cp /tmp/isupipe-crypto.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-crypto'
ssh s2 'sudo systemctl enable --now mysql; sudo systemctl disable --now isupipe-go; sudo systemctl stop isupipe-memory; cp /tmp/isupipe-release /home/isucon/webapp/go/isupipe-memory; sudo cp /tmp/isupipe-memory.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-memory'
for spec in s1:10.99.0.11 s2:10.99.0.12 s3:10.99.0.13; do
 IFS=: read -r host address <<< "$spec"
 scp deploy/isupipe-announce.service "$host:/tmp/"
 printf 'ISUPIPE_ADDRESS=%s\n' "$address" | ssh "$host" 'cat > /home/isucon/announce.env'
 ssh "$host" 'sudo cp /tmp/isupipe-announce.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-announce'
done
for host in s1 s2 s3; do
 git rev-parse HEAD | ssh "$host" 'cat > /home/isucon/webapp/REVISION'
done
for attempt in {1..30}; do
 if curl -fsS http://10.99.0.12:8080/api/tag >/dev/null; then exit 0; fi
 sleep 1
done
exit 1
