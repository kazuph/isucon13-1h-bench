#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
for server in s1 s3; do
  scp go/*.go go/go.mod go/go.sum "$server:/home/isucon/webapp/go/"
  ssh "$server" 'cd /home/isucon/webapp/go && rm -f response_batch.go && /home/isucon/local/golang/bin/gofmt -w *.go && CGO_ENABLED=0 /home/isucon/local/golang/bin/go build -trimpath -o /tmp/isupipe-next . && mv /tmp/isupipe-next /home/isucon/webapp/go/isupipe && sudo systemctl restart isupipe-go'
done
