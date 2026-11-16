#!/usr/bin/env python3
"""steps.json の番号ごとに、元の実行での結果(エラーかどうかと出力の頭)を出す。"""
import json, sys
steps = json.load(open(sys.argv[2], encoding="utf-8"))
want = [int(x) for x in sys.argv[3].split(",")]
res = {}
for line in open(sys.argv[1], encoding="utf-8"):
    try: o = json.loads(line)
    except Exception: continue
    if o.get("type") != "user": continue
    for c in o.get("message", {}).get("content", []) or []:
        if isinstance(c, dict) and c.get("type") == "tool_result":
            t = c.get("content")
            if isinstance(t, list): t = " ".join(x.get("text", "") for x in t if isinstance(x, dict))
            res[c["tool_use_id"]] = (c.get("is_error"), str(t))
for k in want:
    e, t = res.get(steps[k]["id"], (None, "(結果なし)"))
    print(f"[{k}] is_error={e} :: {t[:300]!r}\n")
