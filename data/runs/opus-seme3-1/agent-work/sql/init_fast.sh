#!/usr/bin/env bash
set -eu
cd $(dirname $0)
cat init.sql initial_users.sql initial_livestreams.sql initial_tags.sql initial_livestream_tags.sql initial_reservation_slots.sql initial_reactions.sql initial_ngwords.sql initial_livecomments.sql | mysql -uisucon -pisucon --host ${ISUCON13_MYSQL_DIALCONFIG_ADDRESS:-127.0.0.1} isupipe
