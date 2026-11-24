#!/usr/bin/env python3
"""Measure one or more serving configurations and save a restorable Git tag on improvement."""
import argparse
import datetime
import json
import os
import pathlib
import re
import subprocess
import tempfile

def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)

def git(*args, **kwargs):
    return run('git', *args, stdout=subprocess.PIPE, **kwargs).stdout.strip()

def snapshot(revision, config, label, layout=None):
    # An alternate index keeps experiments independent of ongoing source edits.
    with tempfile.TemporaryDirectory() as directory:
        env = dict(os.environ, GIT_INDEX_FILE=str(pathlib.Path(directory)/'index'))
        git('read-tree', revision, env=env)
        blob = git('hash-object', '-w', '--stdin', input=config)
        git('update-index', '--add', '--cacheinfo', f'100644,{blob},ops/active-front.conf', env=env)
        if layout is not None:
            layout_blob=git('hash-object','-w','--stdin',input=json.dumps(layout,indent=2)+'\n')
            git('update-index','--add','--cacheinfo',f'100644,{layout_blob},ops/active-layout.json',env=env)
        tree = git('write-tree', env=env)
    return git('commit-tree', tree, '-p', revision, '-m', f'Measured ingress configuration {label}')

def measure(revision, config, label, directory, layout=None):
    previous = run('ssh','s1','cat /etc/nginx/sites-enabled/isupipe.conf',stdout=subprocess.PIPE).stdout
    command = 'sudo cp /tmp/pipe-candidate.conf /etc/nginx/sites-enabled/isupipe.conf && sudo nginx -t && sudo systemctl reload nginx'
    run('ssh','s1','cat >/tmp/pipe-candidate.conf',input=config)
    try:
        run('ssh','s1',command,stderr=subprocess.DEVNULL)
    except subprocess.CalledProcessError:
        run('ssh','s1','sudo tee /etc/nginx/sites-enabled/isupipe.conf >/dev/null',input=previous)
        raise
    proc = subprocess.run(['bench'],text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S')
    raw = directory / f'{stamp}-{label}.json'
    raw.write_text(proc.stdout)
    d = json.loads(proc.stdout)
    tag = None
    if d.get('pass'):
        scores = [int(t[6:]) for t in git('tag','--list','score-*').splitlines() if re.fullmatch(r'score-\d+',t)]
        if d['score'] > max(scores,default=0):
            tag = 'score-'+str(d['score'])
            git('tag',tag,snapshot(revision,config,label,layout))
    row = dict(time=stamp,label=label,rev=revision,layout=layout,score=d['score'],passed=d['pass'],dns=d.get('resolved_count'),tag=tag)
    with (directory/'scores.jsonl').open('a') as out: out.write(json.dumps(row)+'\n')
    print(json.dumps(row),flush=True)
    return d

if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--layout',type=pathlib.Path)
    parser.add_argument('revision')
    parser.add_argument('files',type=pathlib.Path,nargs='+')
    args=parser.parse_args()
    directory=pathlib.Path(git('rev-parse','--git-dir'))/'measurements'
    directory.mkdir(exist_ok=True)
    for path in args.files:
        measure(args.revision,path.read_text(),path.stem,directory,json.loads(args.layout.read_text()) if args.layout else None)
