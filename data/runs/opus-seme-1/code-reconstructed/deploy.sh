#!/bin/bash
# usage: deploy.sh [host]  (builds on s1 and restarts)
set -e
cd /home/isucon/work/app
H=${1:-s1}
rsync -a --exclude .git ./ $H:app/
ssh $H 'set -e; cd app && export PATH=$HOME/local/golang/bin:$PATH && go build -o isupipe.new . && mv isupipe.new isupipe
sudo cp conf/isupipe.service /etc/systemd/system/isupipe.service
sudo cp conf/sysctl/99-isu.conf /etc/sysctl.d/ && sudo sysctl -q -p /etc/sysctl.d/99-isu.conf
sudo cp conf/nginx.conf /etc/nginx/nginx.conf && sudo cp conf/nginx-isupipe.conf /etc/nginx/sites-enabled/isupipe.conf && sudo nginx -t -q
sudo systemctl daemon-reload
if grep -q "^TLS_ADDR" conf/extra.env; then sudo systemctl disable --now nginx 2>/dev/null; sudo systemctl restart isupipe; else sudo systemctl restart isupipe; sudo systemctl enable nginx 2>/dev/null; sudo systemctl restart nginx; fi
for i in $(seq 1 60); do curl -s -o /dev/null localhost:8080/api/tag && break; sleep 0.5; done; systemctl is-active isupipe'
