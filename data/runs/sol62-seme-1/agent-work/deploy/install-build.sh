#!/usr/bin/env bash
set -euo pipefail
artifact=${1:?s1 build path required}
shift
if [[ $# -eq 0 ]]; then
    set -- s2 s3
fi
for worker in "$@"; do
    scp -q -3 "s1:$artifact" "$worker:webapp/go/isupipe.next"
    ssh "$worker" 'cd webapp/go && mv isupipe.next isupipe && sudo systemctl restart isupipe-go'
done
ssh s1 "cd webapp/go && install -m 755 \"\$HOME/$artifact\" isupipe.next && mv isupipe.next isupipe && sudo systemctl restart isupipe-go"
