#!/usr/bin/env bash
# VM を stop/start すると IP が変わる(AWS では固定)。PowerDNS のゾーンに残った A レコードを、いまの IP に揃える。
# このゾーンの A レコードは全部「この競技サーバー自身」を指す(公式の初期ゾーン・webapp が足すレコードとも同じ)。
set -eu
. /home/isucon/env.sh
mysql -uisudns -pisudns -h127.0.0.1 isudns -e "UPDATE records SET content='${ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS}' WHERE type='A' AND content<>'${ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS}';"
