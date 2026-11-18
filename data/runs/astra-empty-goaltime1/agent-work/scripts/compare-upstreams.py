#!/usr/bin/env python3
import json,re,subprocess,pathlib,sys
root=pathlib.Path(__file__).resolve().parents[1]
template=(root/'config/nginx-s1.conf').read_text()
modes={
 's3only':'  server 10.99.0.13:8080;',
 's1_s3_1_3':'  server 127.0.0.1:8080 weight=1;\n  server 10.99.0.13:8080 weight=3;',
 'balanced':'  server 127.0.0.1:8080;\n  server 10.99.0.13:8080;',
}
def install(mode):
    config=re.sub(r'upstream isupipe_backend \{.*?\}', 'upstream isupipe_backend {\n'+modes[mode]+'\n  keepalive 64;\n}', template,flags=re.S)
    path=root/'artifacts/nginx-candidate.conf';path.write_text(config)
    subprocess.run(['scp',str(path),'s1:/tmp/nginx-candidate.conf'],check=True)
    subprocess.run(['ssh','s1','sudo cp /tmp/nginx-candidate.conf /etc/nginx/sites-enabled/isupipe.conf && sudo nginx -t && sudo systemctl reload nginx'],check=True,capture_output=True)
    return config
results=[]
for index,mode in enumerate(modes,18):
    install(mode)
    run=subprocess.run(['bench'],text=True,capture_output=True)
    (root/f'logs/{index:03d}-upstream-{mode}.json').write_text(run.stdout)
    try: result=json.loads(run.stdout)
    except Exception:
        install('balanced');raise
    summary={'mode':mode,'pass':result.get('pass'),'score':result.get('score'),'resolved_count':result.get('resolved_count')}
    results.append(summary); print(json.dumps(summary),flush=True)
    if not summary['pass']:
        install('balanced');sys.exit(1)
best=max(results,key=lambda r:r['score'])['mode']
config=install(best)
(root/'artifacts/nginx-selected.conf').write_text(config)
(root/'logs/upstream-comparison.json').write_text(json.dumps(results,indent=2))
print('Selected '+best,flush=True)
