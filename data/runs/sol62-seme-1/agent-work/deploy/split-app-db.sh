#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
artifact=${1:?s1 build path required}

scp -q deploy/app-db.cnf deploy/provision-app-db.sh s3:/tmp/
ssh s3 'sudo install -m 644 /tmp/app-db.cnf /etc/mysql/mysql.conf.d/zz-isupipe.cnf && sudo systemctl restart mysql'
ssh s1 'cat ~/.isupipe-worker-password' | ssh s3 'bash /tmp/provision-app-db.sh'

# Preserve the existing data when changing the database host.
ssh s1 'sudo mysqldump --single-transaction --set-gtid-purged=OFF --no-tablespaces isupipe | gzip -1' |
    ssh s3 'set -o pipefail; gzip -dc | sudo mysql isupipe'
scp -q deploy/point-initializer-at-db.sh s1:/tmp/
scp -q -p sql/init.sh s1:webapp/sql/init.sh
ssh s1 'cat ~/.isupipe-worker-password | bash /tmp/point-initializer-at-db.sh'
ssh s2 'sudo sed -i "s/^ISUCON13_MYSQL_DIALCONFIG_ADDRESS=.*/ISUCON13_MYSQL_DIALCONFIG_ADDRESS=10.99.0.13/" /etc/isupipe-worker.env'
bash deploy/install-build.sh "$artifact" s2
scp -q deploy/split-upstream.conf s1:/tmp/isupipe-split-upstream.conf
ssh s1 'sudo install -m 644 /tmp/isupipe-split-upstream.conf /etc/nginx/conf.d/isupipe-upstream.conf && sudo nginx -t && sudo systemctl reload nginx'
ssh s3 'sudo systemctl disable --now isupipe-go'
