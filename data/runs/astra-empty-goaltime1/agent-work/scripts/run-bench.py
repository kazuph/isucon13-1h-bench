#!/usr/bin/env python3
import json,pathlib,re,subprocess,sys,fcntl,datetime
root=pathlib.Path(__file__).resolve().parents[1]
name=sys.argv[1]
if not re.fullmatch(r'[0-9]{3,4}-[a-z0-9_-]+',name):raise SystemExit('invalid benchmark name')
lock=open(root/'logs/benchmark.lock','w');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
def command(args):return subprocess.check_output(args,text=True)
manifest={'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
 'git_head':command(['git','-C',str(root),'rev-parse','HEAD']).strip(),
 'git_status':command(['git','-C',str(root),'status','--short']),
 'hosts':{}}
for host in ('s1','s2','s3'):
 manifest['hosts'][host]=command(['ssh',host,'sha256sum /home/isucon/webapp/go/isupipe /home/isucon/env.sh /etc/nginx/sites-enabled/isupipe.conf /etc/powerdns/pdns.conf'])
(root/f'logs/{name}.manifest.json').write_text(json.dumps(manifest,indent=2))
(root/f'logs/{name}.nginx.conf').write_text(command(['ssh','s1','cat /etc/nginx/sites-enabled/isupipe.conf']))
result=subprocess.run(['bench'],text=True,capture_output=True)
(root/f'logs/{name}.json').write_text(result.stdout)
if result.stderr:(root/f'logs/{name}.stderr').write_text(result.stderr)
parsed=json.loads(result.stdout)
print(json.dumps({key:parsed.get(key) for key in ('pass','score','resolved_count','started_at','ended_at')}),flush=True)
raise SystemExit(0 if parsed.get('pass') else 1)
