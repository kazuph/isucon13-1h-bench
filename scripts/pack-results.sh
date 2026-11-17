#!/bin/bash
# 公開する結果を ~/isucon13-1h-bench/publish/data/runs/<回>/ にまとめる。
# 入れるもの: 記録(作業ログ・ベンチ・出来事・条件・指示)と、60分後のコードと設定。
# 入れないもの: 実行ファイル、データの写し(state.gob など)、TLS の秘密鍵、Mac mini のパスが入る run.log など。
set -uo pipefail
cd ~/isucon13-1h-bench
OUT=publish/data/runs
rm -rf publish; mkdir -p "$OUT"
# 使い方: scripts/pack-results.sh [回 ...]（回を書かなければ 2026-11-15 の11本）
R="${*:-opus-empty-1 opus-kawasaki-1 opus-seme-1 sol-empty-1 sol-kawasaki-1 sol-seme-1 astra-empty-1 astra-kawasaki-1 astra-kawasaki-2 astra-seme-1 astra-seme-2}"
for d in $R; do
  o=$OUT/$d; mkdir -p "$o"
  for f in agent.jsonl agent-screen.txt bench.jsonl events.jsonl meta.json CONDITIONS.md agent-versions.txt final-bench.out loadavg NOTE.txt; do
    [ -f runs/$d/$f ] && cp runs/$d/$f "$o/"
  done
  cp runs/$d/prompt-*.md "$o/" 2>/dev/null || true
  # GOAL=1（Codex の対話画面）の回の agent.jsonl は Codex のセッションの記録で、Codex 自身の組み込みの指示・モデルの一覧・内部の状態が入る。
  # 実験の記録ではないので、それらを除いて公開する（world_state の行、developer の発言、session_meta の指示）
  if [ -f runs/$d/agent-screen.txt ] && [ -f "$o/agent.jsonl" ]; then
    jq -c 'select(.type != "world_state")
           | select(((.type == "response_item") and (.payload.role? == "developer")) | not)
           | if .type == "session_meta" then del(.payload.instructions, .payload.base_instructions) else . end' "$o/agent.jsonl" > "$o/agent.jsonl.tmp" \
      && mv "$o/agent.jsonl.tmp" "$o/agent.jsonl"
  fi
  for s in s1 s2 s3; do
    src=$PWD/runs/$d/servers/$s; dst=$PWD/$o/servers/$s
    [ -d "$src" ] || continue
    mkdir -p "$dst"
    cp "$src/journal-last-boot.txt" "$dst/" 2>/dev/null || true
    # コード(Go)と設定。言語ごとの初期実装・SQL の初期データ・画像は公式と同じなので入れない
    # webapp は、公式の言語別の初期実装・画像・SQL と、AI が置いたデータ（state・data）を除いて全部入れる（AI は go 以外の場所にコードを置くことがある）
    (cd "$src" && find home/isucon/webapp etc/nginx etc/mysql etc/powerdns etc/systemd/system \
        \( -path home/isucon/webapp/img -o -path home/isucon/webapp/node -o -path home/isucon/webapp/perl -o -path home/isucon/webapp/php \
           -o -path home/isucon/webapp/public -o -path home/isucon/webapp/python -o -path home/isucon/webapp/ruby -o -path home/isucon/webapp/rust \
           -o -path home/isucon/webapp/sql -o -path home/isucon/webapp/state -o -path home/isucon/webapp/data \) -prune \
        -o -type f ! -name '*.key' ! -name debian.cnf ! -name 'state.gob' ! -name 'state.wal' ! -name '*.jsonl' -size -1M -print 2>/dev/null \
      | while read -r f; do file -b "$src/$f" | grep -q ELF && continue; mkdir -p "$dst/$(dirname "$f")"; cp "$src/$f" "$dst/$f"; done)
  done
  # AI の作業フォルダ（AI が書いたコード・メモ・git の履歴）。マニュアルの写し・ログ・データ・実行ファイルは入れない
  if [ -d runs/$d/agent-work ]; then
    mkdir -p "$o/agent-work"
    (cd runs/$d/agent-work && find . \( -path ./manual -o -path ./logs -o -path ./.git -o -path ./orig -o -name state -o -name data -o -name node_modules \) -prune \
        -o -type f -size -1M ! -name '*.gob' ! -name '*.key' ! -name 'auth.json' ! -name '*.jsonl' -print \
      | while read -r f; do file -b "$f" | grep -q ELF && continue; mkdir -p "$OLDPWD/$o/agent-work/$(dirname "$f")"; cp "$f" "$OLDPWD/$o/agent-work/$f"; done)
    [ -d runs/$d/agent-work/.git ] && (cd runs/$d/agent-work && git -c safe.directory='*' bundle create "$OLDPWD/$o/agent-work.history.bundle" --all >/dev/null 2>&1 || true)
  fi
  if [ -d runs/$d/code-reconstructed ]; then
    mkdir -p "$o/code-reconstructed"
    (cd runs/$d/code-reconstructed && git -c safe.directory='*' bundle create "$OLDPWD/$o/code-reconstructed/history.bundle" --all >/dev/null 2>&1 || true)
    (cd runs/$d/code-reconstructed && find . -path ./.git -prune -o -type f -print | while read -r f; do file -b "$f" | grep -q ELF && continue; mkdir -p "$OLDPWD/$o/code-reconstructed/$(dirname "$f")"; cp "$f" "$OLDPWD/$o/code-reconstructed/$f"; done)
    cp runs/$d/code-reconstructed.replay.log "$o/code-reconstructed/replay.log"
  fi
done
du -sh publish/data/runs; du -sh publish/data/runs/* | sort -h | tail -3
echo "--- keys"; grep -rl "PRIVATE KEY" publish || echo none
echo "--- local paths"; grep -rl "/Users/" publish || echo none
