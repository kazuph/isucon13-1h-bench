#!/usr/bin/env python3
import json,re,subprocess,pathlib,sys
root=pathlib.Path(__file__).resolve().parents[1]
template=(root/'config/nginx-s1.conf').read_text()
modes={
 's2only':'  server 10.99.0.12:8080;',
 's2_auth3':'  server 10.99.0.12:8080;',
 's2_s3_balanced':'  server 10.99.0.12:8080;\n  server 10.99.0.13:8080;',
 's3only':'  server 10.99.0.13:8080;',
}

def install(mode):
    config=re.sub(r'upstream isupipe_backend \{.*?\}', 'upstream isupipe_backend {\n'+modes[mode]+'\n  keepalive 64;\n}', template,flags=re.S)
    if mode=='s2_auth3':
        config='upstream isupipe_auth { server 10.99.0.13:8080; keepalive 64; }\n'+config
        locations=''
        for endpoint in ('register','login'):
            locations += '  location = /api/'+endpoint+' { proxy_set_header Host $host; proxy_http_version 1.1; proxy_set_header Connection ""; proxy_pass http://isupipe_auth; }\n'
        config=config.replace('  location = /api/initialize {',locations+'  location = /api/initialize {')
    path=root/'artifacts/nginx-candidate.conf';path.write_text(config)
    subprocess.run(['scp',str(path),'s1:/tmp/nginx-candidate.conf'],check=True)
    subprocess.run(['ssh','s1','sudo cp /tmp/nginx-candidate.conf /etc/nginx/sites-enabled/isupipe.conf && sudo nginx -t && sudo systemctl reload nginx'],check=True,capture_output=True)
    return config
results=[]
for index,mode in enumerate(modes,24):
    install(mode)
    run=subprocess.run(['bench'],text=True,capture_output=True)
    (root/f'logs/{index:03d}-upstream-{mode}.json').write_text(run.stdout)
    try: result=json.loads(run.stdout)
    except Exception:
        install('s3only');raise
    summary={'mode':mode,'pass':result.get('pass'),'score':result.get('score'),'resolved_count':result.get('resolved_count')}
    results.append(summary); print(json.dumps(summary),flush=True)
    if not summary['pass']:
        install('s3only');sys.exit(1)
best=max(results,key=lambda r:r['score'])['mode']
config=install(best)
(root/'artifacts/nginx-colocation-selected.conf').write_text(config)
(root/'logs/colocation-comparison.json').write_text(json.dumps(results,indent=2))
print('Selected '+best,flush=True)
