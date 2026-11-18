# Competition status

Deadline 2026-11-18 12:19 UTC (21:19 JST), started 04:19 UTC. Must work until deadline, keep best verified deployment at finish. No subagents authorized. Conditions/manual fully read. No password bcrypt algorithm/cost changes; persistent data required across reboot. Protected envcheck/aws scripts untouched.

## Layout
- Original implementation `go/` (baseline tag baseline-9190).
- Rewritten complete application `memory/`: Go Echo, authoritative in-memory state, disk snapshot plus journal, read indexes and incremental stats. Every successful mutation durable before response. Initial data loaded from local MySQL after original sql/init.sh. All changes local git, deployed binary separately.
- s1 10.99.0.11: nginx TLS proxies API s2:8080; `edgedns` custom DNS port53 plus HTTP :8053 names update; stores DNS names JSONL and restores on reboot. Initial names include ALL original zone A records as well as users. nginx live config `/etc/nginx/sites-enabled/isupipe.conf` is regular file. pdns disabled. Original Go/MySQL still running idle.
- s2 10.99.0.12: `isupipe-memory` service /home/isucon/webapp/memory/isupipe, env /home/isucon/env.sh; data /home/isucon/webapp/state; original Go disabled. MySQL used only initial load. pprof :6060, /metrics since latest version. Snapshots use CURRENT generation pointer + gen-N/{snapshot.gob,journal.jsonl}, legacy files retained but ignored. Replays tail, drops incomplete final line. DNS sync on app startup and initialize.
- s3 10.99.0.13: `isupipe-auth` service /home/isucon/webapp/authworker/authworker :8090 runs bcrypt generate/compare, original bcrypt.MinCost=4 preserved. Also serves as initial diagnostic MySQL host; original services idle.
- Local toolchain copied to `/tmp/isucon-toolchain/golang/bin`, Go1.21.2 arm64, modules downloaded in /home/isucon/go. Build here avoids competing with benchmark CPU. Source copied to s2 for operational reproducibility but build local.

## Results
baseline 9190; memory-01 failed missing initial DNS names silc etc; fixed full zone import.
memory-02 229377 tag best-229377 at55593a8.
memory-03 249013 tag best-249013 at71c79af (bcrypt split, compact JSON, DNS persist).
memory-04 startup race 502, MUST wait for curl /api/payment after restart before bench.
memory-05 263684 HIGHEST tag best-263684 at40fcda1 (incremental stats & sorted stream indexes).
memory-06 failed double deferred middleware caused duplicated icon bytes; fixed f8b849b.
memory-07 252876 (group sync and generation snapshots).
memory-08 254742 (exact MySQL general_ci LIKE weights).
memory-09 running, logs/memory-09.log. Current deployed commit48356a8 adds metrics and moves DNS updates outside global lock.
Bench one at a time via `bench > logs/name.log 2>&1`. Logs JSON final. ~68secs. Await actual process before another. Use Python summarize logs instead of huge messages.

## Deployment procedure
`cd memory && /tmp/isucon-toolchain/golang/bin/go test -race ./... && .../go build -o /tmp/memory-next .`
scp /tmp/memory-next s2:/home/isucon/webapp/memory/isupipe.next
scp memory/*.go memory/general-ci.bin s2:/home/isucon/webapp/memory/
ssh s2 'mv .../isupipe.next .../isupipe && sudo systemctl restart isupipe-memory && curl --retry 20 --retry-connrefused --retry-delay 1 -sf localhost:8080/api/payment'
Then bench. Restart can take2secs replaying400MB journal. Source best tags predating CURRENT can't restore current state without migration, but initialize can reset appropriately. Finish must choose best valid actual deployment, not blindly old binary ignoring CURRENT.

## Tests and evidence
- Incremental stats random1000 mutation comparisons vs full aggregate passed.
- 400 parallel mutations synced, decoded all400; Go race tests passed.
- Atomic generation snapshot, incomplete journal tail recovery, appended log and second snapshot reopen all passed.
- Application restart payment equality229377 verified semantically (JSON indentation changed).
- DNS standalone restart kept original names verified `dig silc...`.
- Full 3-server reboot NOT yet tested; must do before finish and ensure mutation data persists without initialize.
- SQL LIKE compatibility: measured actual Go mysql driver collation utf8mb4_general_ci, not client default0900. Extracted all BMP weight table from s3 MySQL into memory/general-ci.bin; nonBMP weightfffd. Tests match case/accent/wildcards/backslash. Tools in tools/collation*.

## Observations and next work
CPU profile old full search sorting took15%, eliminated with allStreams/tagStreams ascending indexes. Runtime still CPU idle: s1 nginx ~80% of one core plusDNS14%, s2 API~60%, s3 bcrypt26%; all 2core budgets. API WAL~400MB/bench dominated icon payloads. Group sync150us didn't yet improve score. Need API latency metrics /metrics and mutex/block pprof from memory09 to locate serialization/latency. Metrics cumulative since process startup includes initial startup curl/initialize.
General errors: expected400 actual201 spam is explicitly nonpenalized manual. GET reports often500 because original semantics deletes comments then report FK dangling. Could preserve tombstones for reporting while excluding timeline/stats; not done. One expected201 actual400 spam appeared after LIKE fix; may be manual benchmark issue or semantics difference, analyze if relevant. Reservation initial capacity5 each hourly slot, occasional expected201 actual400.
Potential improvements: durable icons content-addressed binary objects to avoid JSON base64 IO under global lock; response cache encoded per stream/user but icon changes must reflect<=2sec; DNS updates HTTP/fsync move done; improve WAL handling failures (currently applies then response fails but process remains), malformed tail startup, atomic DNS init persistence. Preserve correctness and measure.

Goal remains active; nowhere near deadline. Continue autonomously, don't stop at early score.

## Update at 05:09 UTC

Highest verified score now **339566**, tag `best-339566` at `8fc1eb4`, bench memory-18-tcpproxy. Current running bench memory-19-jsoncache.log (exec session51442) on commit c9c4296.

Results since above: memory10 275241; memory11 278922; reboot02 279712; memory12-buffered 305143; memory13-gzip 296373 (JSON gzip reverted); memory14-dnsbuffered312509; memory15-authcache313431; memory16-cas317470; memory17-directtls FAIL because benchmark whitelist ONLY permits DNS IP10.99.0.11, despite manual3server statement; memory18-tcpproxy339566. NEVER return s2/s3 from DNS with current bench.

Current architecture changed: s1 `edgedns` authoritative DNS53 names HTTP8053, DNS_ADDRESS10.99.0.11, DNS_SYNC_MODE=buffered. nginx now **TCP stream passthrough443 to s2:443**, no TLS termination on s1. Root /etc/nginx/nginx.conf includes /etc/nginx/stream-conf.d/isupipe.conf. Configs tracked nginx-main.conf, nginx-tcp.conf, nginx-http-only.conf (live sites-enabled/isupipe.conf). Original root preserved nginx-main-original.conf; old HTTP proxy site config nginx.conf remains for rollback. s2 `isupipe-memory` now directly handles TLS443 plusHTTP8080, private TLS copies ../tls/server.{crt,key}, service AmbientCapabilities CAP_NET_BIND_SERVICE, DIRECT_TLS=1. nginx s2 disabled. s3 authworker withAUTH_CACHE=1. bcrypt Generate still fresh random salt, original MinCost4; only known hash/password verification pair cached60secs (SHA256 transient cache key), wrong passwords invoke realbcrypt and reject. Auth unit tests verify freshsalt/cost/valid/wrong. Cache hits4013 of4016 compares, scoregain small.

WAL now kernel-buffered acknowledgements: fully written/Flush toOS before response, periodic fsync during activity100ms, explicit fsync barrier on initialize/shutdown. ServiceWAL_SYNC_MODE=buffered, defaultstrict available. Group sync has no artificial timer, drains only queued batch, reusesbufio.Writer. Only mutation waits own ticket; reads no longer wait unrelateddiskcommit. SIGTERM graceful shuts down TLS andHTTP andflushesWAL. Normalreboot andkill9 persistence proven forbuffered mode beforeCAS. Snapshot CURRENT+gen directories atomic; partialtrailingJSON recordrecovery. Strict andbuffered race tests pass.

CAS image storage: directory /home/isucon/webapp/state/blobs/<first2hex>/<sha256>, fsynced immutablefiles, sharedmemorybytes via sync.Map. POSTicon computeshashoutsideglobal lock, dedups actualimages. Event containsImageHash, noImagepayload; State.IconRefs references blob, hydrateIcons loads onceperhash onstartup. LegacyImageevents supported/migrated. Measured4415uploads304MB butonly190distinct13MB; log shrank~400MB to13MB; totalblob14MB; API RSS~109MB. Tests validatepointerdedup, compactevent, replay, corruptblob detection. Oldbest versions beforeCAS cannot interpretnewImageHashlog; preferbest tags frome4c547conwards ifrollingback.

JSON caches c9c4296: cachedUser/Livestream structs withprivate rawJSON andMarshalJSON; rebuildallinitial, refreshnewuser/newstream, refreshuser+ownedstreams oniconupdate. Immutablebytes andtags permitencodingoutsideglobalRWlock. Test validatesnewhash andoldinflightresponse snapshot immutability. Measurement pending.

Persistence audit tools/reboot_audit.py create/verify usesHTTP10.99.0.12:8080, storescookie+expecteduser/iconhash/stream/comment/reaction in logs/reboot-audit.json. Has passed3serverreboot beforebuffering, pluskill9 ands2reboot afterbuffering. First3serverpostrebootbench failed transientUDPtimeout; waitedforactualexternalDNSandthenreboot02 passed279712. Need re-run persistence audit andfull3serverreboot afterlatestCAS/TLS changes beforefinish.

Localcurl MUST use --noproxy '*' when directprivateIPs; environmentproxy otherwisecausedfalse readinesscheck. tools/wait_ready.py usesPythonHTTPclientdirect, waitsAPIup to60s. GET /api/tag curl -I gets405 becauseHEADnotregistered, use-D - -o/dev/null GET. `curl --noproxy '*' -ksD - --resolve pipe.u.isucon.dev:443:10.99.0.11 https://pipe.u.isucon.dev/api/tag -o /dev/null` returnsHTTP/2 withoutnginxServerheader inTCPmode afterreloadquiesces(~1s); firstimmediatecurl maystillhitoldworker.

New measured opportunities: currentGoTLS APIprofile~77% ofonecore, RSA addMul~7.6%, JSONencoding~15%, plentyCPUleft onallnodes. DirectTLS+TCP proxyimproved7%. Consider movingAPI+TLS froms2tos1 toremoveTCP relay entirely (s1currentlyDNS+relay~0.3core, totalAPI~0.8fits2cores); s2couldsubsequentlyserveimageblobs/processing ifneeded, s3bcrypt. This is NOT yetdone. Muststopoldisupipe-go on s1(listens8080) andnginxon s1(443) beforemoving, copyauthoritativecurrentstate/blobs fromstopped/flusheds2, copyTLSkeys on s1userisucon, installmemoryservice, disableon s2. DNS mustremain11. Tools/audit hostcurrentlyhardcoded12 mustparameterizeifmoving. Keepbestsource/configtags andactualdeploymentrolemanifest torestore. DonotchangeprotectedAWS/envcheck files.

Remainingdeadline12:19UTC still>7hours. Continueautonomously; do notstopatcurrentearlyscore. Userrequiresworkingthroughdeadline.
