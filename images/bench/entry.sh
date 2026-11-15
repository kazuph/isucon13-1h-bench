#!/bin/bash
# ベンチを1回流す入口。ssh の forced command からも、mini 側の `env.sh bench` からも同じものが走る。
# 結果と開始・終了時刻は、mini からマウントされた /runs/<run-id>/bench.jsonl に1行追記する(この VM の中には残さない)。
set -u
. /etc/bench/env   # RUN_ID, S1_IP は env.sh up / restart-servers が書く

exec 9>/run/bench/lock
if ! flock -n 9; then
  echo '{"pass":false,"score":0,"messages":["別のベンチが実行中です"]}'
  exit 75
fi

RESULT=/tmp/result.json
rm -f "$RESULT" /tmp/staff.log /tmp/contestant.log
START=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
/opt/bench/bench run \
  --target "https://pipe.u.isucon.dev" \
  --nameserver "$S1_IP" \
  --dns-port 53 \
  --enable-ssl \
  --result-path "$RESULT" \
  --staff-log-path /tmp/staff.log \
  --contestant-log-path /tmp/contestant.log \
  >/tmp/bench.stdout 2>/tmp/bench.stderr
CODE=$?
END=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)

if [ -s "$RESULT" ] && jq -e . "$RESULT" >/dev/null 2>&1; then
  BODY=$(cat "$RESULT")
else
  BODY=$(jq -n --arg e "$(tail -n 5 /tmp/bench.stderr | tr '\n' ' ')" '{pass:false,score:0,messages:["result.json が出力されませんでした",$e]}')
fi

# 開始からの経過秒(run.sh が書く start_epoch)と、Mac mini の負荷平均(run.sh の見張りが5秒おきに書く loadavg)
ELAPSED=null
if [ -f "/runs/$RUN_ID/start_epoch" ]; then ELAPSED=$(( $(date +%s) - $(cat "/runs/$RUN_ID/start_epoch") )); fi
LOAD=$(cat "/runs/$RUN_ID/loadavg" 2>/dev/null || echo "")
FINAL_FLAG=false; [ "${FINAL:-}" = "1" ] && FINAL_FLAG=true
LINE=$(jq -c -n --arg run "$RUN_ID" --arg s "$START" --arg e "$END" --argjson code "$CODE" --argjson r "$BODY" \
  --argjson el "$ELAPSED" --arg load "$LOAD" --argjson final "$FINAL_FLAG" \
  '{run_id:$run, started_at:$s, ended_at:$e, elapsed_s:$el, host_loadavg:$load, final:$final, exit_code:$code, pass:$r.pass, score:$r.score, messages:$r.messages, language:($r.language // null), resolved_count:($r.resolved_count // null)}')
mkdir -p "/runs/$RUN_ID"
echo "$LINE" >> "/runs/$RUN_ID/bench.jsonl"
echo "$LINE"
