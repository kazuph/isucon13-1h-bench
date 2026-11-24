#!/bin/bash
# usage: cmp.sh outdir  (run on s1) -- fetch a set of endpoints after initialize
out=$1; mkdir -p $out
H="--resolve pipe.u.isucon.dev:443:127.0.0.1 -sk"
U=https://pipe.u.isucon.dev
curl $H -XPOST $U/api/initialize > /dev/null
curl $H -c /tmp/ck -XPOST $U/api/login -d '{"username":"test001","password":"test"}' -H 'Content-Type: application/json'
for p in /api/tag /api/user/me /api/user/test001 /api/user/test001/theme /api/user/test001/statistics /api/user/ayamazaki0/livestream /api/livestream "/api/livestream/search?limit=5" "/api/livestream/search?tag=FPS" /api/livestream/1 /api/livestream/1532/livecomment "/api/livestream/1532/livecomment?limit=1" /api/livestream/5849/reaction /api/livestream/1/statistics /api/livestream/2782/ngwords /api/payment /api/livestream/1/report; do
  f=$out/$(echo $p | tr '/?=' '___'); curl $H -b /tmp/ck "$U$p" > $f
done
# writes
curl $H -b /tmp/ck -XPOST $U/api/livestream/1532/livecomment -d '{"comment":"hello <b>&","tip":10}' > $out/w_comment
curl $H -b /tmp/ck -XPOST $U/api/livestream/1532/reaction -d '{"emoji_name":"tada"}' > $out/w_reaction
curl $H -b /tmp/ck -XPOST $U/api/livestream/1532/livecomment/1/report > $out/w_report
curl $H -b /tmp/ck -XPOST $U/api/register -d '{"name":"zzcmp","display_name":"x","description":"d","password":"p","theme":{"dark_mode":true}}' > $out/w_register
curl $H -b /tmp/ck "$U/api/livestream/1532/livecomment" > $out/w_after_comments
curl $H -b /tmp/ck "$U/api/livestream/1532/reaction" > $out/w_after_reactions
curl $H -b /tmp/ck "$U/api/livestream/1/report" > $out/w_after_report
md5sum $out/* | sed "s|$out/||" 
