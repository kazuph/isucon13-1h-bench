#!/bin/bash
# run bench with cpu accounting; usage: b.sh name
n=${1:-run}
/home/isucon/work/bin/cpustat.sh > /tmp/cpu_before
bench > /tmp/bench_$n.json 2>&1
/home/isucon/work/bin/cpustat.sh > /tmp/cpu_after
python3 - "$n" <<'PY'
import json,sys,re,collections
n=sys.argv[1]
d=json.load(open(f'/tmp/bench_{n}.json'))
b={l.split()[0]:list(map(int,l.split()[1:])) for l in open('/tmp/cpu_before')}
a={l.split()[0]:list(map(int,l.split()[1:])) for l in open('/tmp/cpu_after')}
cpu={k:round((a[k][0]-b[k][0])/1e6,1) for k in a}
cpu['nr_thr']=a['throttled'][1]-b['throttled'][1]
c=collections.Counter(re.sub(r'\d+','N',m)[:110] for m in d['messages'])
print("SCORE",d.get('score'),"pass",d.get('pass'),"resolved",d.get('resolved_count'),"cpu_s",cpu)
for k,v in c.most_common(8): print("  ",v,k)
PY
