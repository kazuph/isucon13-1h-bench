#!/usr/bin/env bash
set -eu
zone=/var/lib/isupipe-dns/u.isucon.dev.zone
exec 9>"$zone.lock"
flock 9
printf '%s 30 IN A %s\n' "$1" "$2" >> "$zone"
sudo /usr/bin/pdns_control bind-reload-now u.isucon.dev
