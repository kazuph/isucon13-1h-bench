#!/usr/bin/env python3
"""runs/ の結果から、外部ファイルを読まない1枚の HTML(SVG のグラフと表)を作る。標準ライブラリだけ。

    ./plot.py <runs フォルダ> [-o results.html] [--minutes 60]

読むもの: runs/<run-id>/bench.jsonl と meta.json。run-id が <model>-<prompt>-<n>
(model=opus|astra|sol、prompt=empty|kawasaki|seme)の形のものだけ。それ以外は飛ばす。
"""
import argparse
import html
import json
import re
import sys
from pathlib import Path

RUN_RE = re.compile(r"^(opus|astra|sol)-(empty|kawasaki|seme)-(\d+)$")
MODELS = ["opus", "astra", "sol"]
PROMPTS = ["empty", "kawasaki", "seme"]
COLORS = {"empty": "#8a94a3", "kawasaki": "#c2621d", "seme": "#b4232f"}
PROMPT_LABEL = {"empty": "empty(指示なし)", "kawasaki": "kawasaki", "seme": "seme"}


def load_runs(runs_dir):
    runs = []
    for d in sorted(Path(runs_dir).iterdir()):
        m = RUN_RE.match(d.name)
        if not m or not d.is_dir():
            continue
        bench = d / "bench.jsonl"
        if not bench.is_file():
            continue
        meta = {}
        if (d / "meta.json").is_file():
            try:
                meta = json.loads((d / "meta.json").read_text())
            except ValueError:
                meta = {}
        rows = []
        for line in bench.read_text().splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except ValueError:
                continue
        note = (d / "NOTE.txt").read_text().strip() if (d / "NOTE.txt").is_file() else ""
        running = not any(r.get("final") for r in rows)
        runs.append({"id": d.name, "model": m.group(1), "prompt": m.group(2), "n": int(m.group(3)), "meta": meta, "rows": rows, "note": note, "running": running})
    return runs


def load1(row):
    try:
        return float(str(row.get("host_loadavg", "")).split()[0])
    except (ValueError, IndexError):
        return None


def summarize(run, limit_min):
    rows = run["rows"]
    normal = [r for r in rows if not r.get("final") and r.get("elapsed_s") is not None and r["elapsed_s"] <= limit_min * 60]
    finals = [r for r in rows if r.get("final")]
    passed = [r for r in normal if r.get("pass")]
    best = max((r["score"] for r in passed), default=None)

    def first_over(th):
        for r in sorted(passed, key=lambda r: r["elapsed_s"]):
            if r["score"] > th:
                return r["elapsed_s"] / 60
        return None

    loads = [v for v in (load1(r) for r in normal) if v is not None]
    return {
        "normal": sorted(normal, key=lambda r: r["elapsed_s"]),
        "final": finals[-1] if finals else None,
        "best": best,
        "count": len(normal),
        "t50k": first_over(50000),
        "t100k": first_over(100000),
        "load": sum(loads) / len(loads) if loads else None,
    }


def nice_max(v):
    if v <= 0:
        return 1000
    step = 10 ** (len(str(int(v))) - 1)
    for k in (1, 2, 2.5, 5, 10):
        if v <= step * k:
            return step * k
    return step * 10


def chart(model, runs, limit_min):
    W, H = 776, 340
    L, R, T, B = 80, 120, 16, 44  # 右の余白に最終ベンチを置く
    pw, ph = W - L - R, H - T - B
    sums = {r["id"]: summarize(r, limit_min) for r in runs}
    tops = [1000]
    for s in sums.values():
        tops.append(s["best"] or 0)
        if s["final"] and s["final"].get("pass"):
            tops.append(s["final"]["score"])
    ymax = nice_max(max(tops) * 1.05)

    def X(minute):
        return L + pw * minute / limit_min

    def Y(score):
        return T + ph * (1 - score / ymax)

    o = [f'<svg viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-label="{html.escape(model)} のスコア推移">']
    for i in range(6):
        v = ymax * i / 5
        o.append(f'<line x1="{L}" x2="{L + pw}" y1="{Y(v):.1f}" y2="{Y(v):.1f}" class="grid"/>')
        o.append(f'<text x="{L - 8}" y="{Y(v) + 4:.1f}" class="ax" text-anchor="end">{int(v):,}</text>')
    step = 10 if limit_min >= 40 else max(1, limit_min // 6)
    for m in range(0, int(limit_min) + 1, step):
        o.append(f'<line x1="{X(m):.1f}" x2="{X(m):.1f}" y1="{T}" y2="{T + ph}" class="grid"/>')
        o.append(f'<text x="{X(m):.1f}" y="{T + ph + 16}" class="ax" text-anchor="middle">{m}</text>')
    o.append(f'<text x="{L + pw / 2:.1f}" y="{H - 6}" class="ax" text-anchor="middle">開始からの分</text>')
    cy = T + ph / 2
    o.append(f'<text x="12" y="{cy:.1f}" class="ax" text-anchor="middle" transform="rotate(-90 12 {cy:.1f})">その時点までの最高点(pass のみ)</text>')
    o.append(f'<line x1="{L}" x2="{L}" y1="{T}" y2="{T + ph}" class="axis"/><line x1="{L}" x2="{L + pw}" y1="{T + ph}" y2="{T + ph}" class="axis"/>')
    fx = L + pw + 40
    o.append(f'<line x1="{L + pw + 20}" x2="{L + pw + 20}" y1="{T}" y2="{T + ph}" class="sep"/>')
    o.append(f'<text x="{fx + 14}" y="{T + ph + 16}" class="ax" text-anchor="middle">再起動後</text>')

    for run in runs:
        s = sums[run["id"]]
        color = COLORS[run["prompt"]]
        multi = sum(1 for r in runs if r["prompt"] == run["prompt"]) > 1
        sw = 1.4 if multi else 2.4
        best, pts = 0, []
        for r in s["normal"]:
            if r.get("pass") and r["score"] > best:
                best = r["score"]
            pts.append((r["elapsed_s"] / 60, best))
        if pts:
            d = [f"M{X(pts[0][0]):.1f},{Y(pts[0][1]):.1f}"]
            for (px, py), (nx, ny) in zip(pts, pts[1:]):
                d.append(f"L{X(nx):.1f},{Y(py):.1f}L{X(nx):.1f},{Y(ny):.1f}")
            d.append(f"L{X(limit_min):.1f},{Y(pts[-1][1]):.1f}")
            o.append(f'<path d="{" ".join(d)}" fill="none" stroke="{color}" stroke-width="{sw}" stroke-linejoin="round"/>')
        for r in s["normal"]:
            x = X(r["elapsed_s"] / 60)
            tip = html.escape(run["id"]) + " " + str(r["elapsed_s"] // 60) + "分 "
            if r.get("pass"):
                y = Y(r["score"])
                o.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="3.4" fill="{color}" stroke="#fff" stroke-width="1"><title>{tip}{r["score"]:,}点 pass</title></circle>')
            else:
                y = Y(0)
                o.append(f'<path d="M{x - 4:.1f},{y - 4:.1f}L{x + 4:.1f},{y + 4:.1f}M{x - 4:.1f},{y + 4:.1f}L{x + 4:.1f},{y - 4:.1f}" stroke="{color}" stroke-width="2" fill="none"><title>{tip}fail</title></path>')
        f = s["final"]
        if f:
            x = fx + PROMPTS.index(run["prompt"]) * 12 - 12
            if f.get("pass"):
                y = Y(f["score"])
                o.append(f'<path d="M{x:.1f},{y - 6:.1f}L{x + 6:.1f},{y:.1f}L{x:.1f},{y + 6:.1f}L{x - 6:.1f},{y:.1f}Z" fill="{color}" stroke="#222" stroke-width="1.2"><title>{html.escape(run["id"])} 最終 {f["score"]:,}点 pass</title></path>')
                o.append(f'<text x="{x + 9:.1f}" y="{y + 4:.1f}" class="fl" fill="{color}">{f["score"]:,}</text>')
            else:
                y = Y(0)
                o.append(f'<path d="M{x - 5:.1f},{y - 5:.1f}L{x + 5:.1f},{y + 5:.1f}M{x - 5:.1f},{y + 5:.1f}L{x + 5:.1f},{y - 5:.1f}" stroke="{color}" stroke-width="2.5" fill="none"><title>{html.escape(run["id"])} 最終 fail</title></path>')
                o.append(f'<text x="{x + 9:.1f}" y="{y - 6:.1f}" class="fl" fill="{color}">fail</text>')
    o.append("</svg>")
    return "\n".join(o)


def fmt_min(v):
    return f"{v:.1f}分" if v is not None else "未到達"


def table(runs, limit_min):
    h = [f'<div class="scroll"><table><thead><tr><th>条件</th><th>run-id</th><th>{limit_min}分までの最高点</th><th>最終ベンチ</th><th>回数</th><th>5万点超え</th><th>10万点超え</th><th>負荷平均(1分値)</th></tr></thead><tbody>']
    for run in sorted(runs, key=lambda r: (PROMPTS.index(r["prompt"]), r["n"])):
        s = summarize(run, limit_min)
        f = s["final"]
        if f is None:
            fin = "実行中" if run.get("running") else "なし"
        elif f.get("pass"):
            fin = "{:,} 点 (pass)".format(f["score"])
        else:
            fin = "fail (0 点)"
        if run.get("note"):
            fin += '<br><span class="note">' + html.escape(run["note"]) + "</span>"
        best = "{:,}".format(s["best"]) if s["best"] is not None else "pass なし"
        load = "{:.2f}".format(s["load"]) if s["load"] is not None else "-"
        h.append(
            '<tr><td><span class="sw" style="background:%s"></span>%s</td><td>%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%d</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td></tr>'
            % (COLORS[run["prompt"]], PROMPT_LABEL[run["prompt"]], html.escape(run["id"]), best, fin, s["count"], fmt_min(s["t50k"]), fmt_min(s["t100k"]), load)
        )
    h.append("</tbody></table></div>")
    return "\n".join(h)


def model_heading(model, runs):
    ids, clis = set(), set()
    for r in runs:
        m = r["meta"]
        if m.get("model_id"):
            ids.add(m["model_id"])
        cli = m.get("cli", "")
        for part in m.get("versions", "").split(";"):
            if (cli == "codex" and part.startswith("codex")) or (cli == "claude" and part.startswith("claude-code")):
                clis.add(part.strip())
    mid = ", ".join(sorted(ids)) or model
    ver = ", ".join(sorted(clis)) or "版は meta.json に無し"
    return f"{html.escape(model)}: モデル ID <code>{html.escape(mid)}</code> / CLI {html.escape(ver)}"


CSS = """
:root{--bg:#fff;--fg:#1c2330;--mut:#5b6677;--line:#e3e7ee;--card:#f6f8fb}
@media (prefers-color-scheme:dark){:root{--bg:#12161d;--fg:#e6e9ef;--mut:#9aa5b5;--line:#2a3140;--card:#1a2029}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.6 system-ui,-apple-system,"Hiragino Sans",sans-serif;padding:20px 16px 48px}
main{max-width:860px;margin:0 auto}h1{font-size:20px;margin:0 0 4px}h2{font-size:15px;margin:32px 0 8px;overflow-wrap:anywhere}p.sub{color:var(--mut);margin:0 0 16px}
code{background:var(--card);padding:1px 5px;border-radius:4px;font-size:13px}
.scroll{overflow-x:auto;border:1px solid var(--line);border-radius:8px;background:var(--card);margin:8px 0}
.scroll svg{display:block;min-width:776px;max-width:none;height:auto}
table{border-collapse:collapse;width:100%;min-width:640px}th,td{padding:6px 10px;border-bottom:1px solid var(--line);text-align:left;white-space:nowrap}
th{font-weight:600;color:var(--mut);font-size:12px}td.n{text-align:right;font-variant-numeric:tabular-nums}.note{font-size:11px;color:var(--mut)}
.sw{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:6px}
.legend{display:flex;flex-wrap:wrap;gap:4px 16px;margin:8px 0 0;color:var(--mut);font-size:13px}
svg .grid{stroke:var(--line);stroke-width:1}svg .axis{stroke:var(--mut);stroke-width:1}svg .sep{stroke:var(--mut);stroke-dasharray:3 4}
svg .ax{fill:var(--mut);font-size:11px}svg .fl{font-size:11px;font-weight:600}
"""


def build(runs, limit_min):
    out = ['<!doctype html><html lang="ja"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="60"><title>ISUCON13 60分 実験結果</title><style>' + CSS + "</style></head><body><main>"]
    out.append("<h1>ISUCON13 開始から60分 実験結果</h1>")
    out.append(f'<p class="sub">横軸は開始からの分、縦軸はその時点までの pass した最高点(階段)。円は pass したベンチ、×は fail。菱形(右の区画)は {limit_min} 分後にサーバーを再起動して流した最終ベンチ。</p>')
    out.append('<div class="legend">' + "".join('<span><span class="sw" style="background:%s"></span>%s</span>' % (COLORS[p], PROMPT_LABEL[p]) for p in PROMPTS) + "</div>")
    shown = False
    for model in MODELS:
        mr = [r for r in runs if r["model"] == model]
        if not mr:
            continue
        shown = True
        out.append(f"<h2>{model_heading(model, mr)}</h2>")
        out.append(f'<div class="scroll">{chart(model, mr, limit_min)}</div>')
        out.append(table(mr, limit_min))
    if not shown:
        out.append("<p>対象の run がありません(run-id は <code>&lt;model&gt;-&lt;prompt&gt;-&lt;n&gt;</code> の形)。</p>")
    out.append("</main></body></html>")
    return "\n".join(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("runs_dir")
    ap.add_argument("-o", "--output", default="results.html")
    ap.add_argument("--minutes", type=int, default=60)
    a = ap.parse_args()
    runs = load_runs(a.runs_dir)
    Path(a.output).write_text(build(runs, a.minutes), encoding="utf-8")
    print(f"{a.output}: {len(runs)} runs ({', '.join(r['id'] for r in runs)})", file=sys.stderr)


if __name__ == "__main__":
    main()
