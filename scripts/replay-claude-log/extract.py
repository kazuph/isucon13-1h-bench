#!/usr/bin/env python3
"""Claude Code の stream-json から、ファイルを変えうる手順(Write/Edit/Bash)を順に取り出して JSON の配列で出す。"""
import json, sys
steps = []
for line in open(sys.argv[1], encoding="utf-8"):
    try:
        o = json.loads(line)
    except Exception:
        continue
    if o.get("type") != "assistant":
        continue
    for c in o.get("message", {}).get("content", []) or []:
        if c.get("type") != "tool_use":
            continue
        n, i = c.get("name"), c.get("input", {})
        if n in ("Write", "Edit", "MultiEdit", "Bash", "NotebookEdit"):
            steps.append({"id": c.get("id"), "tool": n, "input": i})
# 元の実行で「実行されなかった」手順(ツールの使い方の誤りで止められたもの)に印を付ける
res = {}
for line in open(sys.argv[1], encoding="utf-8"):
    try:
        o = json.loads(line)
    except Exception:
        continue
    if o.get("type") != "user":
        continue
    for c in o.get("message", {}).get("content", []) or []:
        if isinstance(c, dict) and c.get("type") == "tool_result":
            t = c.get("content")
            if isinstance(t, list):
                t = " ".join(x.get("text", "") for x in t if isinstance(x, dict))
            res[c["tool_use_id"]] = str(t)
for s_ in steps:
    s_["not_run"] = res.get(s_["id"], "").lstrip().startswith("<tool_use_error>")
json.dump(steps, sys.stdout, ensure_ascii=False)
print(f"{len(steps)} steps: " + ", ".join(f"{t}={sum(1 for s in steps if s['tool']==t)}" for t in ("Write","Edit","MultiEdit","Bash","NotebookEdit")), file=sys.stderr)
