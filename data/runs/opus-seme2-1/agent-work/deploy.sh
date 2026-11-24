#!/bin/bash
set -e
cd ~/work/src/app
rsync -a --exclude isupipe ./ s1:webapp/app/
ssh s1 'cd ~/webapp/app && export PATH=$PATH:~/local/golang/bin && go build -o isupipe.new . && mv isupipe.new isupipe && sudo systemctl restart isupipe && sleep 1 && systemctl is-active isupipe'
