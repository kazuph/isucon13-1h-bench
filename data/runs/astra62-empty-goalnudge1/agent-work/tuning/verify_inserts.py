"""Confirm concurrent comment response IDs refer to the same durable records."""
import json
import subprocess
import time

token = 'isupipe-check-' + str(time.time_ns())
script = '''
set -eu
base=https://pipe.u.isucon.dev
tmp=$(mktemp -d)
api(){ curl -kfSs --resolve pipe.u.isucon.dev:443:127.0.0.1 "$@"; }
api -c "$tmp/cookie" -H 'Content-Type: application/json' -d '{"username":"test001","password":"test"}' "$base/api/login"
for i in $(seq 1 16); do
  api -b "$tmp/cookie" -H 'Content-Type: application/json' -d "{\\"comment\\":\\"TOKEN-$i\\",\\"tip\\":0}" "$base/api/livestream/1/livecomment" >"$tmp/$i" &
done
wait
for i in $(seq 1 16); do cat "$tmp/$i"; done
'''.replace('TOKEN',token)
result=subprocess.run(['ssh','s1','bash -s'],input=script,text=True,capture_output=True,check=True)
items={j['id']:j['comment'] for j in map(json.loads,result.stdout.splitlines())}
assert len(items)==16,items
sql="SELECT id,comment FROM livecomments WHERE id IN ("+','.join(map(str,items))+");"
raw=subprocess.run(['ssh','s2','sudo mysql -BN isupipe'],input=sql,text=True,capture_output=True,check=True).stdout
stored={int(k):v for k,v in (line.split('\t',1) for line in raw.splitlines())}
assert items==stored
print('Verified 16 distinct response IDs against their persisted comment text.')
