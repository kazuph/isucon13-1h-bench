#!/usr/bin/env bash

set -eux
cd $(dirname $0)

if test -f /home/isucon/env.sh; then
	. /home/isucon/env.sh
fi

ISUCON_SUBDOMAIN_ADDRESS=${ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS:-127.0.0.1}

temp_dir=$(mktemp -d)
trap 'rm -rf $temp_dir' EXIT
sed 's/<ISUCON_SUBDOMAIN_ADDRESS>/'$ISUCON_SUBDOMAIN_ADDRESS'/g' u.isucon.dev.zone > ${temp_dir}/u.isucon.dev.zone
chmod 755 "${temp_dir}"
chmod 644 "${temp_dir}/u.isucon.dev.zone"
sudo -u pdns pdnsutil load-zone u.isucon.dev ${temp_dir}/u.isucon.dev.zone


# Drop cached DNS answers after replacing the zone during initialization.
sudo systemctl restart pdns
