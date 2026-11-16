#!/bin/bash
# configure s2/s3 as crypto workers (binary built on s1)
set -e
cd /home/isucon/work/app
for H in s2 s3; do
ssh $H 'mkdir -p app'
rsync -a conf $H:app/
ssh s1 "rsync -a -e 'ssh -o StrictHostKeyChecking=no' app/isupipe $H:app/isupipe" 2>/dev/null || { rsync -a s1:app/isupipe /tmp/isupipe.bin && rsync -a /tmp/isupipe.bin $H:app/isupipe; }
ssh $H 'set -e
sudo systemctl disable --now isupipe-go pdns mysql nginx 2>/dev/null || true
sudo cp app/conf/isuworker.service /etc/systemd/system/
sudo systemctl daemon-reload; sudo systemctl enable isuworker 2>/dev/null; sudo systemctl restart isuworker; sleep 0.5; systemctl is-active isuworker'
done
