#!/bin/bash
# deploy app to s1 and build
set -e
cd /home/isucon/work/repo/webapp
rsync -a --exclude isupipe go/ s1:webapp/go/
rsync -a sql/ s1:webapp/sql/
ssh s1 'cd webapp/go && PATH=$HOME/local/go127/bin:$PATH go build -o isupipe.new . && mv isupipe.new isupipe && sudo systemctl restart isupipe-go && sleep 1 && systemctl is-active isupipe-go'
