#!/bin/sh
set -eu
target=/etc/isupipe/tls
install -d -o root -g isucon -m0750 "$target"
for kind in crt key; do
    install -o root -g isucon -m0440 "/etc/nginx/tls/_.u.isucon.dev.$kind" "$target/pipe.$kind"
    install -o root -g isucon -m0440 "/etc/nginx/tls/_.t.isucon.dev.$kind" "$target/default.$kind"
done
