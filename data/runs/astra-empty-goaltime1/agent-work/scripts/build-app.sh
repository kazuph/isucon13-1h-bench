#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work_root=$PWD
mkdir -p artifacts
cd app
export GOMODCACHE="$work_root/toolchain/mod"
export GOCACHE="$work_root/toolchain/cache"
"$work_root/toolchain/golang/bin/go" test ./...
"$work_root/toolchain/golang/bin/go" build -o "$work_root/artifacts/isupipe" .
