#!/usr/bin/env bash
set -euo pipefail
read -r token
install -d -m 755 /etc/systemd/system/isupipe-go.service.d
umask 077
cat > /etc/isupipe-dns-refresh.env <<ENV
ISUPIPE_DNS_REFRESH_URL=http://10.99.0.11:8082/refresh
ISUPIPE_DNS_REFRESH_TOKEN=$token
ENV
printf '[Service]\nEnvironmentFile=/etc/isupipe-dns-refresh.env\n'     > /etc/systemd/system/isupipe-go.service.d/10-dns-refresh.conf
systemctl daemon-reload
