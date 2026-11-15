#!/usr/bin/env bash
# Mac mini の runs/ を手元へ写し、グラフの HTML を作り直し続ける（1分おき）。ページは60秒ごとに自分で読み直す。
# 使い方: ./live.sh <出力する HTML のパス>
set -uo pipefail
cd "$(dirname "$0")"
out="$1"
while true; do
  rsync -a --delete --exclude servers --exclude agent.jsonl --exclude '*.stderr' --exclude _invalid mini:isucon13-1h-bench/runs/ results/runs/ 2>/dev/null
  python3 -I plot.py results/runs -o "$out.tmp" >/dev/null 2>&1 && mv "$out.tmp" "$out"
  sleep 60
done
