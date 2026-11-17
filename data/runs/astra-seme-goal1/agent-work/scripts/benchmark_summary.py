#!/usr/bin/env python3
"""Summarize captured benchmark results, retaining failures and violations."""
import json, pathlib
root=pathlib.Path(__file__).resolve().parents[1]
rows=[]
for path in (root/'logs').glob('bench-*.log'):
    try: r=json.loads(path.read_text())
    except (ValueError,OSError):continue
    rows.append((r.get('started_at',''),path.name,r))
print('| Started (UTC) | Log | Pass | Score | Specification errors |')
print('|---|---|---|---:|---:|')
for started,name,r in sorted(rows):
    violations=sum('仕様違反' in msg for msg in r.get('messages',[]))
    print(f"| {started} | {name} | {r['pass']} | {r['score']:,} | {violations} |")
