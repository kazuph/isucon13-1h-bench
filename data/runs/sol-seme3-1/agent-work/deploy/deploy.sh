#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
scp go/*.go s1:/home/isucon/webapp/go/
scp go/*.go s2:/home/isucon/webapp/go/
scp -r go/cmd/announce s2:/home/isucon/webapp/go/cmd/
scp go/cmd/worker/main.go s2:/home/isucon/webapp/go/cmd/worker/main.go
ssh s2 'cd /home/isucon/webapp/go && ~/local/golang/bin/go test ./... && ~/local/golang/bin/go build -o isupipe . && ~/local/golang/bin/go build -o worker ./cmd/worker && ~/local/golang/bin/go build -o announce ./cmd/announce'
scp s2:/home/isucon/webapp/go/isupipe /tmp/isupipe-app
scp s2:/home/isucon/webapp/go/worker /tmp/isupipe-worker
scp s2:/home/isucon/webapp/go/announce /tmp/isupipe-announce
scp /tmp/isupipe-app s1:/home/isucon/webapp/go/isupipe.new
scp data/baseline.json.gz data/collation.bin s1:/home/isucon/webapp/
scp deploy/isupipe-app.service s1:/tmp/isupipe-app.service
scp deploy/s1-app.env s1:/home/isucon/env.sh
for host in s1 s2 s3; do
 scp /tmp/isupipe-worker "$host:/home/isucon/webapp/go/worker.new"
 scp /tmp/isupipe-announce "$host:/home/isucon/webapp/go/announce"
 scp deploy/isupipe-worker.service "$host:/tmp/isupipe-worker.service"
 ssh "$host" 'mv /home/isucon/webapp/go/worker.new /home/isucon/webapp/go/worker; sudo cp /tmp/isupipe-worker.service /etc/systemd/system/isupipe-worker.service'
done
scp deploy/s1-worker-tuning.conf s1:/tmp/tuning.conf
ssh s1 'sudo sed -i s/DNS=0/DNS=1/ /etc/systemd/system/isupipe-worker.service; sudo mkdir -p /etc/systemd/system/isupipe-worker.service.d; sudo cp /tmp/tuning.conf /etc/systemd/system/isupipe-worker.service.d/tuning.conf'
for host in s1 s2 s3; do
 ssh "$host" 'sudo systemctl daemon-reload; sudo systemctl enable isupipe-worker; sudo systemctl restart isupipe-worker; sudo systemctl disable --now pdns mysql nginx'
done
for host in s2 s3; do ssh "$host" 'sudo systemctl disable --now isupipe-go'; done
ssh s1 'mv /home/isucon/webapp/go/isupipe.new /home/isucon/webapp/go/isupipe; sudo cp /tmp/isupipe-app.service /etc/systemd/system/isupipe-go.service; sudo cp /etc/nginx/tls/_.u.isucon.dev.key /home/isucon/webapp/tls.key; sudo chown isucon:isucon /home/isucon/webapp/tls.key; chmod 600 /home/isucon/webapp/tls.key; sudo systemctl daemon-reload; sudo systemctl enable isupipe-go; sudo systemctl restart isupipe-go; for i in $(seq 1 30); do if curl -ksSf --resolve pipe.u.isucon.dev:443:10.99.0.11 https://pipe.u.isucon.dev/api/payment; then exit 0; fi; sleep 1; done; exit 1'
ssh s1 'dig +short pipe.u.isucon.dev @127.0.0.1'
