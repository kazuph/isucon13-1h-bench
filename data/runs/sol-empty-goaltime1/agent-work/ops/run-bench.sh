#!/usr/bin/env bash
set -euo pipefail
cd /home/isucon/work
run_label=${1:?run label required}
[[ $run_label =~ ^[a-zA-Z0-9_-]+$ ]]
mkdir -p results
exec 9>/tmp/isucon-bench.lock
flock -n 9
{
 date -u +%FT%TZ
 git rev-parse HEAD
 ssh s1 'if systemctl is-active --quiet isupipe-fastdns; then sha256sum webapp/go/fastdns /etc/systemd/system/isupipe-fastdns.service; fi; sha256sum webapp/go/isupipe /etc/powerdns/pdns.conf /etc/powerdns/pdns.d/gmysql-host.conf /etc/nginx/sites-enabled/isupipe.conf; grep -E "ADDRESS=|GOMAXPROCS=|API_URL=|PEER_URL=|FASTDNS=" env.sh'
 ssh s2 'sha256sum webapp/go/isupipe /etc/mysql/mysql.conf.d/zz-isucon.cnf; grep -E "ADDRESS=|GOMAXPROCS=|API_URL=|PEER_URL=|FASTDNS=" env.sh'
 ssh s3 'sha256sum /etc/mysql/mysql.conf.d/zz-isucon.cnf'
} > "results/$run_label.manifest"
bench > "results/$run_label.json"
python3 - "$run_label" <<'PY'
import json,sys
with open('results/'+sys.argv[1]+'.json') as f:r=json.load(f)
print({k:r.get(k) for k in ['pass','score','resolved_count','started_at','ended_at']})
print('messages:',len(r.get('messages',[])))
for m in r.get('messages',[]):
    if 'タイムアウト' in m:print(m)
PY
