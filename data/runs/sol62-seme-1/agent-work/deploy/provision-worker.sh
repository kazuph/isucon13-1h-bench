#!/usr/bin/env bash
set -euo pipefail
read -r password
install -d -m 750 -o root -g isucon /etc/isupipe-pdns
umask 077
cat > /etc/isupipe-worker.env <<ENV
ISUCON13_MYSQL_DIALCONFIG_ADDRESS=10.99.0.11
ISUCON13_MYSQL_DIALCONFIG_USER=isupipe_worker
ISUCON13_MYSQL_DIALCONFIG_PASSWORD=$password
ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=10.99.0.11
ISUPIPE_PDNS_CONFIG_DIR=/etc/isupipe-pdns
ENV
cat > /etc/isupipe-pdns/pdns.conf <<CONF
launch=gmysql
gmysql-host=10.99.0.11
gmysql-port=3306
gmysql-dbname=isudns
gmysql-user=isupipe_worker
gmysql-password=$password
CONF
chgrp isucon /etc/isupipe-pdns/pdns.conf
chmod 640 /etc/isupipe-pdns/pdns.conf
install -m 644 /tmp/isupipe-worker.service /etc/systemd/system/isupipe-go.service
systemctl daemon-reload
systemctl enable isupipe-go
