#!/bin/bash
set -eu
sed -i -E 's/worker_connections +[0-9]+;/worker_connections 8192;/' /etc/nginx/nginx.conf
if ! grep -q "^worker_rlimit_nofile" /etc/nginx/nginx.conf; then sed -i "1iworker_rlimit_nofile 65536;" /etc/nginx/nginx.conf; fi
nginx -t
systemctl reload nginx
