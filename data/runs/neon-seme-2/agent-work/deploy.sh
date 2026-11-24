#!/usr/bin/env bash
set -euo pipefail

# Build CGO binary natively on agent (linux/arm64 Ubuntu 22.04, identical to s1/s2/s3)
cd /home/isucon/work/repo/go
CGO_ENABLED=1 GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomod \
  /tmp/golang/bin/go build -ldflags='-s -w' -o /tmp/isupipe.bin .

scp -q /tmp/isupipe.bin s1:/home/isucon/webapp/go/isupipe.new &
scp -q /tmp/isupipe.bin s2:/home/isucon/webapp/go/isupipe.new &
scp -q /tmp/isupipe.bin s3:/home/isucon/webapp/go/isupipe.new &
wait

ssh s1 "mv /home/isucon/webapp/go/isupipe.new /home/isucon/webapp/go/isupipe && sudo systemctl restart isupipe-go.service"
sleep 0.3
ssh s2 "mv /home/isucon/webapp/go/isupipe.new /home/isucon/webapp/go/isupipe && sudo systemctl restart isupipe-go.service" &
ssh s3 "mv /home/isucon/webapp/go/isupipe.new /home/isucon/webapp/go/isupipe && sudo systemctl restart isupipe-go.service" &
wait
echo "Built CGO binary on agent and deployed to s1, s2, s3!"
