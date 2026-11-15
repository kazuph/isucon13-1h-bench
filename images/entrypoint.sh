#!/bin/bash
# bench / agent VM の入口: 固定 IP を足してから本来のプロセスを動かす
/opt/common-net.sh || exit 1
exec "$@"
