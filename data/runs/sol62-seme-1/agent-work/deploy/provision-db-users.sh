#!/usr/bin/env bash
set -euo pipefail
umask 077
credential="$HOME/.isupipe-worker-password"
if [[ ! -s "$credential" ]]; then
    openssl rand -hex 32 > "$credential"
fi
password=$(<"$credential")
for host in 10.99.0.12 10.99.0.13; do
    sudo mysql <<SQL
CREATE USER IF NOT EXISTS 'isupipe_worker'@'$host' IDENTIFIED BY '$password';
ALTER USER 'isupipe_worker'@'$host' IDENTIFIED BY '$password';
GRANT SELECT, INSERT, UPDATE, DELETE ON isupipe.* TO 'isupipe_worker'@'$host';
GRANT SELECT ON isudns.* TO 'isupipe_worker'@'$host';
GRANT INSERT, DELETE ON isudns.records TO 'isupipe_worker'@'$host';
SQL
done
