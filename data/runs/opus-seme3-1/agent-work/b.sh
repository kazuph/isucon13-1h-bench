#!/bin/bash
# run bench, save, print summary
n=$(ls ~/bench-logs | wc -l); f=~/bench-logs/r$n.json
bench > $f
python3 -c "
import json,collections;d=json.load(open('$f'));c=collections.Counter(m[:60] for m in d['messages'] if 'expected:400, actual:201' not in m)
print('$f', 'score',d['score'],'pass',d['pass'],'load',d['host_loadavg'],'dns',d['resolved_count']);print(c.most_common(6))"
