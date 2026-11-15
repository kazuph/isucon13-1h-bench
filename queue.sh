#!/usr/bin/env bash
# 決めた順に run.sh を1本ずつ流す。点を汚さないよう同時には流さない。
# 使い方: ./queue.sh <回の番号> <model:prompt> [<model:prompt> ...]
#   例: ./queue.sh 1 astra:empty sol:empty astra:kawasaki sol:kawasaki astra:seme sol:seme
# run-id は <model>-<prompt>-<回の番号>。すでに final の行がある run-id は飛ばす。
set -uo pipefail
cd "$(dirname "$0")"
n="$1"; shift
for spec in "$@"; do
  model="${spec%%:*}"; prompt="${spec##*:}"
  id="${model}-${prompt}-${n}"
  if [ -f "runs/$id/bench.jsonl" ] && grep -q '"final": *true' "runs/$id/bench.jsonl"; then
    echo "[$(date +%T)] skip $id（終わっている）"; continue
  fi
  echo "[$(date +%T)] start $id"
  ./run.sh "$id" "$model" "$prompt" > "runs-$id.log" 2>&1
  echo "[$(date +%T)] end $id exit=$?"
  mkdir -p "runs/$id" && mv "runs-$id.log" "runs/$id/run.log"
done
echo "[$(date +%T)] queue done"
