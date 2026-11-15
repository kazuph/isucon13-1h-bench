#!/usr/bin/env bash
# AI を無人で走らせて記録を取る(Mac mini 上で実行する)。
#   ./run.sh <run-id> <model> <prompt>
#     model : opus (Claude Code, claude-opus-5-5) | astra (Codex, gpt-6-astra) | sol (Codex, gpt-6.1-sol)
#     prompt: empty | kawasaki | seme  (prompts/<prompt>.md)
#   環境変数 RUN_MINUTES (既定 60): AI を動かす時間(分)
#   環境変数 KEEP=1: 終了後も VM を消さない(調査用)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"
export PATH="/opt/homebrew/bin:$PATH"

RUN_ID=${1:-}; MODEL=${2:-}; PROMPT=${3:-}
RUN_MINUTES=${RUN_MINUTES:-60}
LAUNCH_LINE='CONDITIONS.md を読み、今回の条件を確認してから、ISUCON を解いてください。'
AGENT=isu13-agent
SERVERS=(isu13-s1 isu13-s2 isu13-s3)
BENCH_VM=isu13-bench

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

[ -n "$RUN_ID" ] && [ -n "$MODEL" ] && [ -n "$PROMPT" ] || die "usage: ./run.sh <run-id> <opus|astra|sol> <empty|kawasaki|seme>"
[[ "$RUN_ID" =~ ^[A-Za-z0-9._-]+$ ]] || die "run-id は英数字と . _ - だけ"
[[ "$RUN_MINUTES" =~ ^[0-9]+$ ]] || die "RUN_MINUTES は整数"
PROMPT_FILE="prompts/$PROMPT.md"
[ -f "$PROMPT_FILE" ] || die "$PROMPT_FILE が無い(指示ファイルは親が用意する)"

case "$MODEL" in
  opus)
    KIND=claude; MODEL_ID=claude-opus-5-5; INSTR_NAME=CLAUDE.md
    [ -s secrets/claude_oauth_token ] || die "secrets/claude_oauth_token が無い(Claude のログイン用トークンが必要。~/isucon13-1h-bench/secrets/claude_oauth_token に置く)"
    ;;
  astra) KIND=codex; MODEL_ID=gpt-6-astra; INSTR_NAME=AGENTS.md ;;
  sol)   KIND=codex; MODEL_ID=gpt-6.1-sol; INSTR_NAME=AGENTS.md ;;
  *) die "model は opus | astra | sol" ;;
esac
# Codex のログインは、この実験専用のもの(secrets/codex/auth.json)を使う。Mac mini 本体の ~/.codex は使わない。
# Codex は使うたびに更新用のトークンを入れ替えるので、VM の中で更新された auth.json を毎回ここへ書き戻す(書き戻さないと次の回でログインが切れる)。
CODEX_AUTH=secrets/codex/auth.json
if [ "$KIND" = codex ]; then [ -s "$CODEX_AUTH" ] || die "$CODEX_AUTH が無い(この実験専用の Codex のログインが必要。README の「Codex のログイン」を参照)"; fi

RUN_DIR="runs/$RUN_ID"
[ ! -e "$RUN_DIR/agent.jsonl" ] || die "$RUN_DIR に前の実行の記録がある。別の run-id を使う"
mkdir -p "$RUN_DIR"
EVENTS="$RUN_DIR/events.jsonl"
event() { # <type> [detail]
  jq -c -n --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg e "$1" --arg d "${2:-}" '{at:$t,event:$e,detail:$d}' >> "$EVENTS"
}

WATCH_PID=""
cleanup() {
  [ -z "$WATCH_PID" ] || kill "$WATCH_PID" 2>/dev/null || true
  if [ "${KEEP:-0}" != 1 ]; then ./env.sh down >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

# 1. まっさらに作る
log "env.sh up $RUN_ID"
./env.sh up "$RUN_ID" >/dev/null 2>"$RUN_DIR/up.log" || { tail -5 "$RUN_DIR/up.log" >&2; die "env.sh up 失敗"; }
./env.sh agent-up >/dev/null 2>&1 || die "agent VM を作れない"
for i in $(seq 1 30); do container exec "$AGENT" true >/dev/null 2>&1 && break; sleep 1; done
container exec "$AGENT" cat /etc/agent-versions.txt > "$RUN_DIR/agent-versions.txt"

# 2. 作業フォルダ(CONDITIONS.md と指示ファイル)
START_EPOCH=$(date +%s)
END_EPOCH=$((START_EPOCH + 8 * 3600))
fmt_jst() { TZ=Asia/Tokyo date -r "$1" '+%Y-%m-%d %H:%M JST'; }
STAGE=$(mktemp -d)
cat > "$STAGE/CONDITIONS.md" <<EOF
# 今回の条件

- 競技時間は8時間です。開始は $(fmt_jst "$START_EPOCH")、終了は $(fmt_jst "$END_EPOCH") です。
- 競技サーバーは s1、s2、s3 の3台です(固定 IP は 10.99.0.11、10.99.0.12、10.99.0.13)。各サーバーは 2 コア・4GB メモリ・Ubuntu 22.04 です。
- \`ssh s1\` のように入れます(ユーザー isucon、sudo 可)。
- ベンチマーカーは \`bench\` コマンドで流します。1回約80秒かかり、同時に流せるのは1本で、対象は s1 です。
- 当日マニュアルは \`manual/\` にあります。
- ポータルサイトはありません。
- 競技の終了後に、サーバーを再起動してからベンチマーカーを流します。
- サーバーの再起動は \`sudo reboot\` で行ってかまいません。
- 初期実装は Go で、すでに動いています。
EOF
cp "$PROMPT_FILE" "$STAGE/$INSTR_NAME"
container cp "$STAGE/CONDITIONS.md" "$AGENT:/home/isucon/work/CONDITIONS.md"
container cp "$STAGE/$INSTR_NAME" "$AGENT:/home/isucon/work/$INSTR_NAME"
cp "$STAGE/CONDITIONS.md" "$RUN_DIR/CONDITIONS.md"; cp "$PROMPT_FILE" "$RUN_DIR/prompt-$PROMPT.md"
rm -rf "$STAGE"
container exec "$AGENT" chown -R isucon:isucon /home/isucon/work

# 3. AI の起動コマンド(CLI の既定のまま。足したのは自動実行に必要な権限の許可と、出力の形式だけ)
if [ "$KIND" = claude ]; then
  AGENT_CMD=(claude -p "$LAUNCH_LINE" --model "$MODEL_ID" --dangerously-skip-permissions --output-format stream-json --verbose)
  ENV_ARGS=(-e "CLAUDE_CODE_OAUTH_TOKEN=$(tr -d '\n' < secrets/claude_oauth_token)")
else
  container exec "$AGENT" install -d -o isucon -g isucon -m 700 /home/isucon/.codex
  container cp "$CODEX_AUTH" "$AGENT:/home/isucon/.codex/auth.json"
  container exec "$AGENT" chown isucon:isucon /home/isucon/.codex/auth.json
  container exec "$AGENT" chmod 600 /home/isucon/.codex/auth.json
  AGENT_CMD=(codex exec --model "$MODEL_ID" --dangerously-bypass-approvals-and-sandbox --json "$LAUNCH_LINE")
  ENV_ARGS=()
fi
jq -n --arg run "$RUN_ID" --arg model "$MODEL" --arg model_id "$MODEL_ID" --arg prompt "$PROMPT" --arg kind "$KIND" \
  --arg cmd "$(printf '%q ' "${AGENT_CMD[@]}")" --argjson minutes "$RUN_MINUTES" --arg started "$(date -u -r "$START_EPOCH" +%Y-%m-%dT%H:%M:%SZ)" \
  --arg versions "$(cat "$RUN_DIR/agent-versions.txt" | tr '\n' ';')" \
  '{run_id:$run, model:$model, model_id:$model_id, cli:$kind, prompt:$prompt, command:$cmd, run_minutes:$minutes, started_at:$started, versions:$versions}' > "$RUN_DIR/meta.json"

# 4. 見張り(5秒おき): Mac mini の負荷平均を書き、止まった競技サーバーを戻す
echo "$START_EPOCH" > "$RUN_DIR/start_epoch"
watch_loop() {
  while true; do
    uptime | sed 's/.*load averages*: //' > "$RUN_DIR/loadavg"
    for n in "${SERVERS[@]}"; do
      st=$(container inspect "$n" 2>/dev/null | jq -r '.[0].status.state // "missing"')
      if [ "$st" != running ]; then
        event server_down "$n state=$st"
        if container start "$n" >/dev/null 2>&1; then event server_restarted "$n"; else event server_restart_failed "$n"; fi
      fi
    done
    sleep 5
  done
}
watch_loop & WATCH_PID=$!
event start "model=$MODEL_ID prompt=$PROMPT minutes=$RUN_MINUTES"

# 5. AI を起動し、RUN_MINUTES 分で止める
log "AI 起動: $MODEL_ID / $PROMPT / ${RUN_MINUTES}分"
container exec -u isucon -w /home/isucon/work -e HOME=/home/isucon ${ENV_ARGS[@]+"${ENV_ARGS[@]}"} "$AGENT" "${AGENT_CMD[@]}" \
  > "$RUN_DIR/agent.jsonl" 2> "$RUN_DIR/agent.stderr" &
AI_PID=$!
DEADLINE=$((START_EPOCH + RUN_MINUTES * 60))
while kill -0 "$AI_PID" 2>/dev/null; do
  if [ "$(date +%s)" -ge "$DEADLINE" ]; then event time_up "AI の VM を止める"; break; fi
  sleep 5
done
# VM を止める前に、VM の中で更新された Codex の auth.json を書き戻す
save_codex_auth() {
  [ "$KIND" = codex ] || return 0
  if container cp "$AGENT:/home/isucon/.codex/auth.json" "$CODEX_AUTH.new" 2>/dev/null && jq -e .tokens "$CODEX_AUTH.new" >/dev/null 2>&1; then
    chmod 600 "$CODEX_AUTH.new"; mv "$CODEX_AUTH.new" "$CODEX_AUTH"; event codex_auth_saved "ok"
  else rm -f "$CODEX_AUTH.new"; event codex_auth_save_failed "VM から auth.json を取れなかった"; fi
}
if kill -0 "$AI_PID" 2>/dev/null; then
  save_codex_auth
  container stop "$AGENT" >/dev/null 2>&1 || true
  wait "$AI_PID" 2>/dev/null || true
else
  wait "$AI_PID" 2>/dev/null && AI_RC=0 || AI_RC=$?
  event agent_exited "exit=$AI_RC"
  save_codex_auth
  container stop "$AGENT" >/dev/null 2>&1 || true
fi
AGENT_END_EPOCH=$(date +%s)

# AI が打ったベンチが走ったままなら(AI の ssh は切れてもベンチ VM の中では続く)、打ち切る。
# 残すと最終ベンチが「別のベンチが実行中」で落ち、時間切れ後の点が記録に混ざる。打ち切ったベンチは bench.jsonl に行を残さない。
KILLED=$(container exec "$BENCH_VM" bash -c 'n=$(pgrep -x entry.sh | wc -l); for p in $(pgrep -x entry.sh) $(pgrep -x bench); do kill "$p" 2>/dev/null; done; echo $n' 2>/dev/null || echo 0)
[ "${KILLED:-0}" = 0 ] || event bench_cut_at_time_up "実行中のベンチを打ち切った"
for i in $(seq 1 20); do container exec "$BENCH_VM" pgrep -x bench >/dev/null 2>&1 || break; sleep 1; done

# 6. 見張りを止め、サーバーを再起動して最終ベンチ
kill "$WATCH_PID" 2>/dev/null || true; WATCH_PID=""
log "restart-servers → 最終ベンチ"
./env.sh restart-servers >/dev/null 2>"$RUN_DIR/restart.log" || event restart_failed "$(tail -2 "$RUN_DIR/restart.log" | tr '\n' ' ')"
# 再起動後に s1 のアプリが応答するまで待つ(最大180秒)。かかった秒数を記録する。応答しなくても最終ベンチは流す。
# 3台を同時に再起動すると、DB を別の台へ移した構成では、アプリが DB の起動より先に上がって一度落ち、systemd の再起動で数秒後に戻る。
# nginx の 502 を「準備できた」と数えると、その数秒の間に最終ベンチが走って0点になる(2026-11-15 の astra-kawasaki-1・astra-seme-1)。
# だから DNS が引けて、アプリの GET /api/tag が200を3回続けて返すまで待つ。
READY_T0=$(date +%s); READY=no; OK_STREAK=0
for i in $(seq 1 90); do
  if [ -n "$(container exec "$BENCH_VM" dig +time=1 +tries=1 @10.99.0.11 pipe.u.isucon.dev +short 2>/dev/null)" ] \
     && [ "$(container exec "$BENCH_VM" curl -sk -o /dev/null -m 3 -w '%{http_code}' --resolve pipe.u.isucon.dev:443:10.99.0.11 https://pipe.u.isucon.dev/api/tag 2>/dev/null)" = 200 ]; then
    OK_STREAK=$((OK_STREAK + 1)); [ "$OK_STREAK" -ge 3 ] && { READY=yes; break; }
  else OK_STREAK=0; fi
  sleep 2
done
event s1_entry_ready "$READY after $(( $(date +%s) - READY_T0 ))s"
FINAL=1 ./env.sh bench "$RUN_ID" > "$RUN_DIR/final-bench.out" 2>&1 || event final_bench_failed "$(tail -c 300 "$RUN_DIR/final-bench.out")"
event final_bench "$(tail -n 1 "$RUN_DIR/final-bench.out" | cut -c1-300)"

# 7. 競技サーバーの設定を持ち帰る
for n in "${SERVERS[@]}"; do
  d="$RUN_DIR/servers/${n#isu13-}"; mkdir -p "$d"
  container exec "$n" tar -cf - -C / home/isucon/webapp etc/nginx etc/mysql etc/powerdns etc/systemd/system 2>/dev/null | tar -xf - -C "$d" 2>/dev/null \
    || event collect_failed "$n"
  container exec "$n" journalctl -b --no-pager > "$d/journal-last-boot.txt" 2>/dev/null || true
done

jq --arg ended "$(date -u -r "$AGENT_END_EPOCH" +%Y-%m-%dT%H:%M:%SZ)" --argjson el "$((AGENT_END_EPOCH - START_EPOCH))" '. + {agent_ended_at:$ended, agent_elapsed_s:$el}' "$RUN_DIR/meta.json" > "$RUN_DIR/meta.json.tmp" && mv "$RUN_DIR/meta.json.tmp" "$RUN_DIR/meta.json"
# ログインが切れていた回は、AI が何もできていないので無効にする(queue.sh は終了コード3で止まる)
if grep -q "Failed to refresh token\|Not logged in\|Invalid API key\|OAuth token has expired" "$RUN_DIR/agent.stderr" "$RUN_DIR/agent.jsonl" 2>/dev/null; then
  event auth_failed "AI のログインが切れていた。この回は無効"; log "AUTH FAILED: $RUN_DIR"; exit 3
fi
log "done: $RUN_DIR"
