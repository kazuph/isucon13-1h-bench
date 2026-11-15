#!/usr/bin/env bash
# ISUCON13 過去問の競技環境を、Apple の container だけで作る入口(Mac mini 上で実行する)。
#   ./env.sh build                 イメージを作る(初回と、イメージ定義を変えた時だけ)
#   ./env.sh up <run-id>           4台(s1,s2,s3,bench)を捨てて、まっさらに作り直す
#   ./env.sh down                  4台を止めて消す
#   ./env.sh bench <run-id>        ベンチを1回流し、runs/<run-id>/bench.jsonl に1行追記する
#   ./env.sh restart-servers       s1〜s3 を再起動する(再起動後もアプリは自動で上がる)
#   ./env.sh ips                   4台の IP を表示する
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"
export PATH="/opt/homebrew/bin:$PATH"

SRC_SHA="8f6afdc3603f0c661368de4659a7240862f59623"   # isucon/isucon13 main の固定コミット
SERVER_IMAGE="isucon13-1h-server:latest"
BENCH_IMAGE="isucon13-1h-bench:latest"
SERVERS=(isu13-s1 isu13-s2 isu13-s3)
BENCH=isu13-bench
ALL=("${SERVERS[@]}" "$BENCH")

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

container_ip() { # <name> -> IPv4 (CIDR を除く)
  container inspect "$1" | jq -r '.[0].networks[0].ipv4Address // empty' | cut -d/ -f1
}

fetch_src() {
  if [ ! -f "src/isucon13/.sha" ] || [ "$(cat src/isucon13/.sha)" != "$SRC_SHA" ]; then
    log "公式リポジトリ isucon/isucon13@${SRC_SHA:0:7} を取得"
    rm -rf src && mkdir -p src
    curl -fsSL "https://codeload.github.com/isucon/isucon13/tar.gz/$SRC_SHA" | tar xz -C src
    mv "src/isucon13-$SRC_SHA" src/isucon13
    echo "$SRC_SHA" > src/isucon13/.sha
  fi
}

cmd_build() {
  mkdir -p build; scripts/gen-secrets.sh
  fetch_src
  log "ベンチ用イメージをビルド(Go で linux/arm64)"
  container build --progress plain -t "$BENCH_IMAGE" -f images/bench/Dockerfile . > build/bench.log 2>&1 || { tail -30 build/bench.log >&2; die "bench image build 失敗(build/bench.log)"; }
  log "サーバー用イメージをビルド"
  container build --progress plain -t "$SERVER_IMAGE" -f images/server/Dockerfile . > build/server.log 2>&1 || { tail -30 build/server.log >&2; die "server image build 失敗(build/server.log)"; }
  log "build done"
}

have_images() {
  local list; list=$(container image ls)
  grep -q 'isucon13-1h-server' <<<"$list" && grep -q 'isucon13-1h-bench' <<<"$list"
}

cmd_down() {
  for n in "${ALL[@]}"; do container stop "$n" >/dev/null 2>&1 || true; done
  for n in "${ALL[@]}"; do container rm "$n" >/dev/null 2>&1 || true; done
}

wait_server_ready() { # <name>
  local n=$1 i
  for i in $(seq 1 120); do
    if container exec "$n" bash -c '
        systemctl is-active --quiet mysql && systemctl is-active --quiet pdns &&
        systemctl is-active --quiet isupipe-go && systemctl is-active --quiet nginx && systemctl is-active --quiet ssh &&
        curl -sk -o /dev/null -w "%{http_code}" -H "Host: pipe.u.isucon.dev" https://127.0.0.1/api/tag | grep -q 200' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  container exec "$n" systemctl --failed --no-pager >&2 || true
  die "$n が起動しない"
}

write_bench_target() { # <run-id>
  local s1; s1=$(container_ip isu13-s1); [ -n "$s1" ] || die "s1 の IP が取れない"
  container exec "$BENCH" bash -c "printf 'RUN_ID=%s\nS1_IP=%s\n' '$1' '$s1' > /etc/bench/env && chmod 0644 /etc/bench/env"
}

cmd_up() {
  local run_id=${1:-}; [ -n "$run_id" ] || die "usage: env.sh up <run-id>"
  [[ "$run_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "run-id は英数字と . _ - だけ"
  have_images || cmd_build
  local t0=$SECONDS n
  cmd_down
  mkdir -p "runs/$run_id"
  log "4台を起動"
  for n in "${SERVERS[@]}"; do
    container run -d --name "$n" --cpus 2 --memory 4g --cap-add ALL "$SERVER_IMAGE" /sbin/init >/dev/null &
  done
  container run -d --name "$BENCH" --cpus 2 --memory 4g -v "$ROOT/runs:/runs" "$BENCH_IMAGE" >/dev/null &
  wait
  for n in "${SERVERS[@]}"; do wait_server_ready "$n" & done
  wait
  write_bench_target "$run_id"
  log "up done: $((SECONDS - t0)) 秒"
  cmd_ips
}

cmd_ips() {
  local n
  for n in "${ALL[@]}"; do printf '%s %s\n' "$n" "$(container_ip "$n")"; done
}

current_run_id() {
  container exec "$BENCH" bash -c '. /etc/bench/env && echo $RUN_ID' 2>/dev/null || die "bench VM が無い。先に env.sh up <run-id>"
}

cmd_bench() {
  local run_id=${1:-}; [ -n "$run_id" ] || die "usage: env.sh bench <run-id>"
  local cur; cur=$(current_run_id)
  [ "$cur" = "$run_id" ] || die "いま up 済みの run-id は '$cur'。'$run_id' ではベンチを流さない"
  container exec -u bench "$BENCH" /opt/bench/entry.sh
}

cmd_restart_servers() {
  local n cur; cur=$(current_run_id)
  for n in "${SERVERS[@]}"; do container stop "$n" >/dev/null; done
  for n in "${SERVERS[@]}"; do container start "$n" >/dev/null; done
  for n in "${SERVERS[@]}"; do wait_server_ready "$n" & done
  wait
  write_bench_target "$cur"
  log "restart-servers done"
  cmd_ips
}

case "${1:-}" in
  build) cmd_build ;;
  up) shift; cmd_up "$@" ;;
  down) cmd_down ;;
  bench) shift; cmd_bench "$@" ;;
  restart-servers) cmd_restart_servers ;;
  ips) cmd_ips ;;
  *) sed -n '2,9p' "$0"; exit 2 ;;
esac
