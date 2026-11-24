#!/usr/bin/env bash
set -euo pipefail
ssh s1 'sed -i '\''s/ISUCON13_MYSQL_DIALCONFIG_ADDRESS="127.0.0.1"/ISUCON13_MYSQL_DIALCONFIG_ADDRESS="10.99.0.12"/'\'' /home/isucon/env.sh && sudo systemctl start isupipe-go'
