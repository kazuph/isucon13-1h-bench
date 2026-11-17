#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
revision=$(git rev-parse --short HEAD)
release="release-$revision"
staging=$(mktemp -d)
git archive HEAD app | tar -x -C "$staging" --strip-components=1
ssh s2 "mkdir -p /home/isucon/webapp/$release"
scp "$staging"/*.go "$staging/go.mod" "$staging/go.sum" "s2:/home/isucon/webapp/$release/"
ssh s2 "cd /home/isucon/webapp/$release && /home/isucon/local/golang/bin/go test ./... && /home/isucon/local/golang/bin/go build -o isupipe ."
scp "s2:/home/isucon/webapp/$release/isupipe" "$staging/isupipe"
for server in s1 s3; do
 ssh "$server" "mkdir -p /home/isucon/webapp/$release"
 scp "$staging/isupipe" "$server:/home/isucon/webapp/$release/"
done
scp scripts/announce-address.go s2:/tmp/announce-address.go
ssh s2 'CGO_ENABLED=0 /home/isucon/local/golang/bin/go build -o /tmp/announce-address /tmp/announce-address.go'
scp s2:/tmp/announce-address "$staging/announce-address"
for server in s1 s2 s3; do
 scp "$staging/announce-address" "$server:/home/isucon/announce-address"
 scp config/isucon-announce.service "$server:/tmp/isucon-announce.service"
 ssh "$server" 'sudo cp /tmp/isucon-announce.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable --now isucon-announce'
 ssh "$server" "ln -sfn /home/isucon/webapp/$release /home/isucon/webapp/current"
done
scp config/isupipe-dns.service config/nginx-edge.conf config/nginx-main.conf s1:/tmp/
scp config/isupipe-fast.service s2:/tmp/
scp config/isupipe-auth.service s3:/tmp/
ssh s1 'sudo cp /tmp/isupipe-dns.service /etc/systemd/system/; sudo cp /tmp/nginx-edge.conf /etc/nginx/sites-enabled/isupipe.conf; sudo cp /tmp/nginx-main.conf /etc/nginx/nginx.conf; sudo nginx -t; sudo systemctl daemon-reload; sudo systemctl enable isupipe-dns; sudo systemctl restart isupipe-dns; sudo systemctl reload nginx'
ssh s3 'sudo cp /tmp/isupipe-auth.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable isupipe-auth; sudo systemctl restart isupipe-auth'
ssh s2 'sudo cp /tmp/isupipe-fast.service /etc/systemd/system/; sudo systemctl daemon-reload; sudo systemctl enable isupipe-fast; sudo systemctl restart isupipe-fast'
for attempt in $(seq 1 60); do
 if ssh s2 'curl -fsS http://127.0.0.1:8080/api/tag >/dev/null'; then
  echo "Deployed $revision"
  exit 0
 fi
 sleep 1
done
exit 1
