#!/usr/bin/env bash
set -eu
cd "$(dirname "$0")"
if [[ -f /home/isucon/env.sh ]]; then
    . /home/isucon/env.sh
fi
exec ./zone_update init "${ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS:-127.0.0.1}"
