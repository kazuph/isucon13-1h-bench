#!/bin/bash
# 公開する結果を ~/isucon13-1h-bench/publish/data/runs/<回>/ にまとめる。
# 入れるもの: 記録(作業ログ・ベンチ・出来事・条件・指示)と、60分後のコードと設定。
# 入れないもの: 実行ファイル、データの写し(state.gob など)、TLS の秘密鍵、Mac mini のパスが入る run.log など。
set -uo pipefail
cd ~/isucon13-1h-bench
OUT=publish/data/runs
rm -rf publish; mkdir -p "$OUT"
R="opus-empty-1 opus-kawasaki-1 opus-seme-1 sol-empty-1 sol-kawasaki-1 sol-seme-1 astra-empty-1 astra-kawasaki-1 astra-kawasaki-2 astra-seme-1 astra-seme-2"
for d in $R; do
  o=$OUT/$d; mkdir -p "$o"
  for f in agent.jsonl bench.jsonl events.jsonl meta.json CONDITIONS.md agent-versions.txt final-bench.out loadavg NOTE.txt; do
    [ -f runs/$d/$f ] && cp runs/$d/$f "$o/"
  done
  cp runs/$d/prompt-*.md "$o/" 2>/dev/null || true
  for s in s1 s2 s3; do
    src=$PWD/runs/$d/servers/$s; dst=$PWD/$o/servers/$s
    [ -d "$src" ] || continue
    mkdir -p "$dst"
    cp "$src/journal-last-boot.txt" "$dst/" 2>/dev/null || true
    # コード(Go)と設定。言語ごとの初期実装・SQL の初期データ・画像は公式と同じなので入れない
    (cd "$src" && find home/isucon/webapp/go home/isucon/webapp/fast home/isucon/webapp/pdns etc/nginx etc/mysql etc/powerdns etc/systemd/system \
        -type f ! -name '*.key' ! -name debian.cnf ! -name 'state.gob' ! -name 'state.wal' ! -name '*.jsonl' -size -1M 2>/dev/null \
      | while read -r f; do file -b "$src/$f" | grep -q ELF && continue; mkdir -p "$dst/$(dirname "$f")"; cp "$src/$f" "$dst/$f"; done)
  done
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
