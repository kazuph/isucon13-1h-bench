#!/usr/bin/env bash

set -euo pipefail
cd -- "$(dirname -- "$0")"

if [[ -f /home/isucon/env.sh ]]; then
	. /home/isucon/env.sh
fi
if [[ -f /home/isucon/isupipe-db.env ]]; then
	. /home/isucon/isupipe-db.env
fi

export MYSQL_PWD=${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}
cat init.sql \
	initial_users.sql \
	initial_livestreams.sql \
	initial_tags.sql \
	initial_livestream_tags.sql \
	initial_reservation_slots.sql \
	initial_reactions.sql \
	initial_ngwords.sql \
	initial_livecomments.sql |
	mysql --user "${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}" \
		--host "${ISUCON13_MYSQL_DIALCONFIG_ADDRESS:-127.0.0.1}" \
		--port "${ISUCON13_MYSQL_DIALCONFIG_PORT:-3306}" \
		"${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}"
unset MYSQL_PWD

bash ../pdns/init_zone.sh
pdns_control purge
