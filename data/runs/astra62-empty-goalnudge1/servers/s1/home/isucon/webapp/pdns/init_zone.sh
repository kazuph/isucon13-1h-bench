#!/usr/bin/env bash
set -eu
cd "$(dirname "$0")"
. /home/isucon/env.sh
zone=/var/lib/isupipe-dns/u.isucon.dev.zone
exec 9>"$zone.lock"
flock 9
sed -e "s/<ISUCON_SUBDOMAIN_ADDRESS>/$ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS/g" -e 's/ 0 IN A / 30 IN A /g' u.isucon.dev.zone > "$zone.new"
chmod 644 "$zone.new"
mv "$zone.new" "$zone"
sudo /usr/bin/pdns_control bind-reload-now u.isucon.dev
