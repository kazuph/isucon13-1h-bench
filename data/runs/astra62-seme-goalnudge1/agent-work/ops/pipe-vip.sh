#!/bin/sh
set -eu
# Only inbound HTTPS traffic for the public address terminates locally.
ip route replace local 10.99.0.11/32 dev lo table 243
if ! ip rule show | grep -q 'lookup 243'; then
    ip rule add priority 443 iif eth0 to 10.99.0.11/32 ipproto 6 dport 443 lookup 243
fi
