#!/usr/bin/env python3
import json,pathlib,subprocess
root=pathlib.Path(__file__).resolve().parents[1]
template=(root/'config/nginx-s1.conf').read_text()
modes={'baseline':(False,False),'http2':(True,False),'gzip1':(False,True),'http2_gzip1':(True,True)}
def install(mode):
 h2,gzip=modes[mode];config=template
 if h2:config=config.replace('listen 443 ssl','listen 443 ssl http2')
 if gzip:config=config.replace('  client_max_body_size 10m;','  client_max_body_size 10m;\n  gzip on;\n  gzip_comp_level 1;\n  gzip_min_length 1024;\n  gzip_types application/json;')
 path=root/'artifacts/nginx-transport-candidate.conf';path.write_text(config)
 subprocess.run(['scp',str(path),'s1:/tmp/nginx-transport-candidate.conf'],check=True)
 subprocess.run(['ssh','s1','sudo cp /tmp/nginx-transport-candidate.conf /etc/nginx/sites-enabled/isupipe.conf && sudo nginx -t && sudo systemctl reload nginx'],check=True,capture_output=True)
 return config
results=[]
try:
 for index,mode in enumerate(modes,29):
  install(mode)
  name=f'{index:03d}-transport-{mode}'
  run=subprocess.run([str(root/'scripts/run-bench.py'),name],text=True,capture_output=True)
  print(run.stdout,flush=True,end='')
  run.check_returncode()
  result=json.loads((root/f'logs/{name}.json').read_text())
  results.append({'mode':mode,'pass':result['pass'],'score':result['score'],'resolved_count':result['resolved_count']})
except Exception:
 install('baseline');raise
best=max(results,key=lambda r:r['score'])['mode']
(root/'artifacts/nginx-transport-selected.conf').write_text(install(best))
(root/'logs/transport-comparison.json').write_text(json.dumps(results,indent=2))
print('Selected '+best,flush=True)
