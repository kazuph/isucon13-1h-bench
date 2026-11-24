#!/usr/bin/env python3
"""Use the remaining contest window to test and reconfirm measured configurations."""
import argparse
import datetime as dt
import hashlib
import json
import random
import re
import runpy
import statistics
import subprocess
import time
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parent
MEASURE = runpy.run_path(str(ROOT/'measure-front.py'))['measure']
DEPLOY = runpy.run_path(str(ROOT/'deploy-layout.py'))['layout']
RESULTS = Path('.git/measurements')
UTC = dt.timezone.utc

def normalize(raw):
    cfg = dict(raw)
    defaults = {
        'application':'s1', 'accounts':'s2', 'app_procs':2, 'app_gc':100,
        'dns_procs':2, 'dns_workers':2, 'dns_batch':16, 'sync_mode':'full',
        'sync_ms':5, 'group_us':0, 'unix_socket':False, 'native_tls':False,
        'icon_worker':False, 'search_cache':True, 'search_gzip':False,
        'integrated_dns':False,'pgo':False,'db_binlog':True,'db_flush':1,'nginx_workers':3,
        'upstream_idle':64,'ssl_buffer_kb':16,'http2':True,'reuseport':False,
        'log_buffer':True,'proxy_buffers':True,'keepalive':10000
    }
    cfg = {**defaults, **cfg}
    if cfg['sync_mode']!='interval':cfg['sync_ms']=5
    if cfg['sync_mode']=='interval':cfg['group_us']=0
    if cfg['accounts']=='none':cfg['icon_worker']=False
    if not cfg['search_cache']:cfg['search_gzip']=False
    if cfg['application']!='s1':cfg['native_tls']=cfg['unix_socket']=cfg['integrated_dns']=False
    if cfg['integrated_dns']:cfg['dns_procs']=cfg['app_procs']
    if cfg['native_tls']:
        cfg['unix_socket']=False
        for k in ('upstream_idle','ssl_buffer_kb','http2','reuseport','log_buffer','proxy_buffers','keepalive','nginx_workers'):cfg[k]=defaults[k]
    return cfg

SPACE = {
    'integrated_dns':[False,True], 'native_tls':[False,True], 'unix_socket':[False,True], 'pgo':[False,True],
    'app_procs':[1,2,3], 'app_gc':[60,100,180,300,450],
    'dns_procs':[1,2,3], 'dns_workers':[1,2,3,4], 'dns_batch':[1,4,8,16,32,64],
    'accounts':['s2','s3','none'], 'icon_worker':[True,False],
    'sync_mode':['full','data','interval'],'sync_ms':[1,3,5,12,30,75],'group_us':[-1,0,50,200],
    'db_binlog':[True,False],'db_flush':[1,2], 'search_cache':[True,False],'search_gzip':[False,True],
    'nginx_workers':[1,2,3,4], 'upstream_idle':[8,32,64,128,256], 'ssl_buffer_kb':[4,8,16,32],
    'http2':[True,False],'reuseport':[False,True],'log_buffer':[True,False],'proxy_buffers':[True,False],
    'keepalive':[256,1000,10000,30000]
}

def front(cfg):
    s=(ROOT/'edge-front.conf').read_text()
    s=s.replace('keepalive 64;', f'keepalive {cfg["upstream_idle"]};',1)
    s=s.replace('keepalive_requests 10000;',f'keepalive_requests {cfg["keepalive"]};')
    if not cfg['http2']:s=s.replace('listen 443 ssl http2;', 'listen 443 ssl;')
    if cfg['reuseport']:s=s.replace('listen 443 ssl default_server;', 'listen 443 ssl default_server reuseport;')
    options=[f'ssl_buffer_size {cfg["ssl_buffer_kb"]}k;']
    if cfg['log_buffer']:options+=['access_log /var/log/nginx/access.log combined buffer=256k flush=2s;']
    if cfg['proxy_buffers']:options+=['proxy_buffers 16 16k;','proxy_buffer_size 16k;','proxy_busy_buffers_size 64k;']
    return s.replace('  client_max_body_size','  '+'\n  '.join(options)+'\n  client_max_body_size')

def identity(cfg):
    return hashlib.sha256(json.dumps(normalize(cfg),sort_keys=True).encode()).hexdigest()[:12]

def read_rows():
    path=RESULTS/'scores.jsonl'
    return [json.loads(l) for l in path.read_text().splitlines() if l.strip()] if path.exists() else []

def seed():
    by_label=defaultdict(list)
    for row in read_rows():
        if row['passed'] and row.get('layout'):
            by_label[(row['rev'],row['label'])].append(row)
    best=max(by_label.values(),key=lambda rs:statistics.median(r['score'] for r in rs))
    return normalize(best[0]['layout'])

def best_tag():
    tags=subprocess.check_output(['git','tag','--list','score-*'],text=True).splitlines()
    return max((t for t in tags if re.fullmatch(r'score-\d+',t)),key=lambda t:int(t[6:]))

def recover():
    tag=best_tag()
    cfg=json.loads(subprocess.check_output(['git','show',tag+':ops/active-layout.json']))
    site=subprocess.check_output(['git','show',tag+':ops/active-front.conf'],text=True)
    print('Restoring verified configuration',tag,flush=True)
    DEPLOY(tag,cfg,site)

def search(revision, cut):
    RESULTS.mkdir(exist_ok=True)
    records=RESULTS/'campaign.jsonl'
    prior=[json.loads(l) for l in records.read_text().splitlines() if l.strip()] if records.exists() else []
    scores=defaultdict(list); configs={}; tried=set()
    for r in prior:
        if r['revision']!=revision:continue
        key=identity(r['config']);tried.add(key);configs[key]=r['config']
        if r['score']>0:scores[key].append(r['score'])
    initial=seed()
    rng=random.Random(20261121+len(prior))
    candidates=[initial]
    for key in ('integrated_dns','pgo','native_tls','unix_socket','db_binlog','db_flush','icon_worker','search_gzip','sync_mode','sync_ms','dns_workers','dns_batch','app_gc','nginx_workers','app_procs','ssl_buffer_kb','reuseport','upstream_idle'):
        values=SPACE[key].copy();rng.shuffle(values)
        candidates.extend(normalize({**initial,key:v}) for v in values if v!=initial[key])
    step=len(prior)
    last_cfg=None
    while dt.datetime.now(UTC)+dt.timedelta(seconds=135)<cut:
        ranked=sorted(scores,key=lambda k:statistics.median(scores[k]),reverse=True)
        cfg=None
        # Revisit top configurations to distinguish real gains from run-to-run noise.
        if ranked and step%8==7:
            key=min(ranked[:4],key=lambda k:(len(scores[k]),-statistics.median(scores[k])))
            cfg=configs[key]
        while cfg is None and candidates:
            candidate=normalize(candidates.pop(0))
            if identity(candidate) not in tried:cfg=candidate
        while cfg is None:
            parents=ranked[:min(6,len(ranked))]
            parent=configs[rng.choice(parents)] if parents else initial
            mutated=dict(parent)
            if len(parents)>1 and rng.random()<.35:
                other=configs[rng.choice(parents)]
                for key in SPACE:
                    if rng.random()<.25:mutated[key]=other[key]
            for key in rng.sample(list(SPACE),rng.choice([1,2,2,3])):
                mutated[key]=rng.choice(SPACE[key])
            mutated=normalize(mutated)
            if identity(mutated) not in tried:cfg=mutated
        key=identity(cfg);label=f'campaign-{step:03d}-{key}';tried.add(key);configs[key]=cfg
        print('Trial',label,json.dumps(cfg,sort_keys=True),flush=True)
        try:
            rendered=DEPLOY(revision,cfg,front(cfg))
            last_cfg=cfg
            result=MEASURE(revision,rendered,label,RESULTS,cfg)
            score=result['score'] if result['pass'] else 0
        except (RuntimeError,subprocess.SubprocessError,OSError,ValueError) as exc:
            print('Trial setup or measurement failed',label,str(exc),flush=True)
            score=0
            recover();last_cfg=None
        if score:scores[key].append(score)
        with records.open('a') as f:
            f.write(json.dumps(dict(revision=revision,config=cfg,score=score,label=label))+'\n')
        step+=1
    return last_cfg

if __name__=='__main__':
    ap=argparse.ArgumentParser()
    ap.add_argument('revision')
    ap.add_argument('--until',required=True,help='UTC timestamp at which to reserve time for final verification')
    args=ap.parse_args()
    search(args.revision,dt.datetime.fromisoformat(args.until).astimezone(UTC))
