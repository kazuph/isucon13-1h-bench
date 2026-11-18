#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
target_host=${1:-s1}
scp app/*.go app/go.mod app/go.sum "$target_host:/home/isucon/webapp/go/"
scp artifacts/isupipe "$target_host:/home/isucon/webapp/go/isupipe.next"
ssh "$target_host" 'mv /home/isucon/webapp/go/isupipe.next /home/isucon/webapp/go/isupipe && sudo systemctl restart isupipe-go'
