#!/bin/bash
# usage: deploy.sh  -> build on s1, install, restart
set -e
cd /home/isucon/work
rsync -a --delete --exclude isupipe go/ s1:webapp/go/
rsync -a sql/ s1:webapp/sql/
ssh s1 'cd ~/webapp/go && export PATH=$HOME/local/golang/bin:$PATH && go mod tidy >/dev/null 2>&1; go build -o isupipe . && sudo systemctl restart isupipe && sleep 1 && systemctl is-active isupipe'
