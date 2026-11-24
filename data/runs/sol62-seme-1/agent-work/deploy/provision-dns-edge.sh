#!/usr/bin/env bash
set -euo pipefail
umask 077
token_file="$HOME/.isupipe-dns-refresh-token"
password_file="$HOME/.isupipe-dns-edge-password"
if [[ ! -s "$token_file" ]]; then
    openssl rand -hex 32 > "$token_file"
fi
if [[ ! -s "$password_file" ]]; then
    openssl rand -hex 32 > "$password_file"
fi
token=$(<"$token_file")
password=$(<"$password_file")

sudo mysql <<SQL
CREATE USER IF NOT EXISTS 'isupipe_dns_edge'@'localhost' IDENTIFIED BY '$password';
ALTER USER 'isupipe_dns_edge'@'localhost' IDENTIFIED BY '$password';
GRANT SELECT ON isudns.records TO 'isupipe_dns_edge'@'localhost';
GRANT SELECT ON isudns.domains TO 'isupipe_dns_edge'@'localhost';
SQL

printf 'ISUPIPE_DNS_REFRESH_TOKEN=%s\nISUPIPE_DNS_DB_PASSWORD=%s\n' "$token" "$password" |
    sudo bash -c 'umask 077; cat > /etc/isupipe-dns-edge.env'
sudo install -m 644 /tmp/dns-edge.service /etc/systemd/system/isupipe-dns-edge.service
sudo systemctl daemon-reload
