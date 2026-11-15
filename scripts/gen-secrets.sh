#!/usr/bin/env bash
# 鍵と自前 CA を1回だけ作る(あれば何もしない)。keys/ と pki/ は git に入れない。
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p keys pki
[ -f keys/isucon ]       || ssh-keygen -q -t ed25519 -N '' -C 'isucon13-1h-bench isucon@servers' -f keys/isucon
[ -f keys/bench_entry ]  || ssh-keygen -q -t ed25519 -N '' -C 'isucon13-1h-bench bench-entry (forced command)' -f keys/bench_entry
if [ ! -f pki/ca.crt ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj '/CN=isucon13-1h-bench CA' \
    -keyout pki/ca.key -out pki/ca.crt 2>/dev/null
fi
if [ ! -f pki/server.crt ]; then
  openssl req -newkey rsa:2048 -nodes -subj '/CN=*.u.isucon.dev' -keyout pki/server.key -out pki/server.csr 2>/dev/null
  printf 'subjectAltName=DNS:u.isucon.dev,DNS:*.u.isucon.dev,DNS:*.t.isucon.dev\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n' > pki/server.ext
  openssl x509 -req -in pki/server.csr -CA pki/ca.crt -CAkey pki/ca.key -CAcreateserial -days 3650 \
    -extfile pki/server.ext -out pki/server.crt 2>/dev/null
fi
chmod 600 keys/isucon keys/bench_entry pki/*.key
