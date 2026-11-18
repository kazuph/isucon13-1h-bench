#!/usr/bin/env bash
set -euo pipefail
cd /home/isucon/work
. config/primary.env
target=${1:?usage: move_primary.sh s1|s2}
case "$target" in s1) address=10.99.0.11;; s2) address=10.99.0.12;; *) exit 2;; esac
if [[ "$target" == "$ISU_PRIMARY" ]]; then exit 0; fi
source_server=$ISU_PRIMARY
ssh "$source_server" 'systemctl is-active --quiet isupipe-memory'
(cd memory && /tmp/isucon-toolchain/golang/bin/go build -o /tmp/memory-migration .)
scp -r memory "$target":/home/isucon/webapp/
scp /tmp/memory-migration "$target":/home/isucon/webapp/memory/isupipe.next
scp config/isupipe-memory.service "$target":/tmp/
ssh "$source_server" 'sudo systemctl disable --now isupipe-memory'
ssh "$target" 'sudo systemctl disable --now isupipe-go nginx'
ssh "$source_server" 'tar -C /home/isucon/webapp/state -cf - .' | ssh "$target" 'mkdir -p /home/isucon/webapp/state && tar -C /home/isucon/webapp/state -xf -'
ssh "$target" 'mkdir -p webapp/tls && sudo install -o isucon -g isucon -m 600 /etc/nginx/tls/_.u.isucon.dev.key webapp/tls/server.key && sudo install -o isucon -g isucon -m 644 /etc/nginx/tls/_.u.isucon.dev.crt webapp/tls/server.crt && sudo cp /tmp/isupipe-memory.service /etc/systemd/system/ && sudo systemctl daemon-reload && mv webapp/memory/isupipe.next webapp/memory/isupipe && sudo systemctl enable --now isupipe-memory'
ISU_API_ADDRESS="$address" python3 tools/wait_ready.py
if [[ "$target" == s2 ]]; then ssh s1 'sudo nginx -t && sudo systemctl enable --now nginx'; fi
printf 'ISU_PRIMARY=%s\n' "$target" > config/primary.env
