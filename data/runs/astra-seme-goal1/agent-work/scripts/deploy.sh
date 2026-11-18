#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
rsync -a --exclude=isupipe app/ s2:/home/isucon/webapp/app/
ssh s2 'cd ~/webapp/app && ~/local/golang/bin/go test ./... && ~/local/golang/bin/go build -o /tmp/isupipe-release .'
rsync -a network/ s2:/home/isucon/webapp/network/
ssh s2 '~/local/golang/bin/go build -o /tmp/announce-address-release ~/webapp/network/main.go'
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
scp s2:/tmp/isupipe-release "$stage/isupipe"
scp s2:/tmp/announce-address-release "$stage/announce-address"
for host in s1 s2 s3; do
  ssh "$host" 'mkdir -p ~/webapp/app'
  scp "$stage/isupipe" "$host:/tmp/isupipe-release"
  scp "$stage/announce-address" "$host:/home/isucon/webapp/announce-address"
  scp config/isupipe-network@.service "$host:/tmp/isupipe-network@.service"
  address="10.99.0.1${host#s}"
  ssh "$host" "sudo cp /tmp/isupipe-network@.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-network@$address"
done
scp config/isupipe-dns.service config/site.conf config/performance.conf s1:/tmp/
scp config/isupipe-memory.service s2:/tmp/
scp config/isupipe-bcrypt.service s3:/tmp/
ssh s1 'set -e; sudo systemctl disable --now isupipe-go pdns; sudo systemctl stop isupipe-dns || true; cp /tmp/isupipe-release ~/webapp/app/isupipe; sudo cp /tmp/isupipe-dns.service /etc/systemd/system/; sudo cp /tmp/site.conf /etc/nginx/sites-enabled/isupipe.conf; sudo cp /tmp/performance.conf /etc/nginx/conf.d/isupipe-performance.conf; sudo mkdir -p /var/cache/nginx/isupipe-icons; sudo chown www-data:www-data /var/cache/nginx/isupipe-icons; sudo systemctl daemon-reload; sudo nginx -t; sudo systemctl enable --now isupipe-dns; sudo systemctl reload nginx'
ssh s3 'set -e; sudo systemctl disable --now isupipe-go; sudo systemctl stop isupipe-bcrypt || true; cp /tmp/isupipe-release ~/webapp/app/isupipe; sudo cp /tmp/isupipe-bcrypt.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-bcrypt'
ssh s2 'set -e; sudo systemctl disable --now isupipe-go; sudo systemctl stop isupipe-memory || true; cp /tmp/isupipe-release ~/webapp/app/isupipe; sudo cp /tmp/isupipe-memory.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isupipe-memory'
