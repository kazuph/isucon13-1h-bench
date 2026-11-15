#!/usr/bin/env bash
# 手元のこのフォルダを Mac mini の ~/isucon13-1h-bench/ に送る(秘密・生成物は送らない)
set -euo pipefail
cd "$(dirname "$0")"
rsync -a --delete --exclude .git --exclude keys --exclude pki --exclude src --exclude runs --exclude build ./ mini:isucon13-1h-bench/
