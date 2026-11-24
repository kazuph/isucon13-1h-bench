#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
. /home/isucon/env.sh
{
  cat init.sql
  echo "TRUNCATE TABLE icon_objects;"
  echo 'START TRANSACTION;'
  cat initial_users.sql initial_livestreams.sql initial_tags.sql initial_livestream_tags.sql initial_reservation_slots.sql initial_reactions.sql initial_ngwords.sql initial_livecomments.sql
  echo 'COMMIT;'
} | MYSQL_PWD="${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}" mysql --host="${ISUCON13_MYSQL_DIALCONFIG_ADDRESS:-127.0.0.1}" --port="${ISUCON13_MYSQL_DIALCONFIG_PORT:-3306}" --user="${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}" "${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}"
