# ISUCON progress

## Conditions
- Deadline: 2026-11-18 10:15 UTC (19:15 JST). Work continuously until deadline, then reboot/test per CONDITIONS. No subagents authorized.
- Targets s1/s2/s3, 2 cores/4GB each. `bench` only one concurrent, targets s1. Do not access bench server except authorized `bench` command.
- Rules manual/: bcrypt algorithm/cost unchanged; data must persist across reboot; icon changes visible within 2s; conditional GET 304 allowed; DNS TTL allowed.

## Workspace/deploy
- Baseline source/config in ignored `baseline/`. Working Go source `app/`, git initialized.
- Go path remote /home/isucon/local/golang/bin/go (arm64). `scripts/build-app.sh` builds on s3 /home/isucon/build, copies artifacts/isupipe. `scripts/deploy-app.sh` deploys artifact+source to s1 and restarts app. Only run deploy after bench finishes.
- s1 currently serves all traffic: nginx, app :8080, mysql isupipe+isudns, pdns.
- s3 app service temporarily has icons-build binary for smoke testing, not serving benchmark; schema only 002 applied there. Its /home/isucon/build is current source/build workspace.
- s2 app disabled; mysql configured with config/mysql-s2.cnf (2GB buffer, O_DIRECT, skip-log-bin, bind all); remote isucon@10.99.0.% granted isupipe.*. Verified s1 can SELECT 1 on s2. Preparing migration of isupipe data to s2 after benchmark 008, not yet copied/switched.

## Applied s1
- migrations/001 indexes (isupipe), 002 generated stored SHA256 image_hash, 003 DNS records(name,type).
- config/pdns.conf disables verbose DNS logging; cache TTLs still zero.
- Source current d89ce27: aggregate stats SQL preserving tie sort; batch moderation with original general_ci LIKE; icon deadlock fix RC tx+user row lock; persisted icon hashes+ETag304; request-local response cache; interpolate params+idle pool10; disable request debug logs; reservation time upper bound+reuse locked slot value; JOIN tag searches/hydration.
- s1 slow log enabled dynamically .02 sec, /var/lib/mysql/isu13-s1-slow.log; s2 .05 persistent. mysql originally default128MB and binlog enabled s1.

## Benchmarks
- 001 baseline: pass 8959, DNS11621.
- 002 indexes: pass24612, DNS15361.
- 003 stats: pass31282, DNS15585.
- 004 moderation+icon locking: pass29055, DNS16937; DNS timeouts, no icon deadlocks.
- 005 icon hashes/304: pass38378, DNS16700.
- 006 DNS index/logging: pass34842, DNS88866.
- 007 request cache+pool+interpolation: pass90048, DNS82303.
- 008 reservation/tag joins RUNNING session31127 -> logs/008-reservation-joins.json, started ~02:25 UTC.
- Logs ignored under logs/. Benchmark sessions can vanish across goal continuation; check JSON result and process state. Zombie ssh is terminal, do not wait with kill -0 alone. Never run overlapping benches.

## Next
1. Finish 008, move isupipe DB to s2 using consistent dump/restore while s1 app stopped. Change s1 env DB address only to10.99.0.12 (DNS remains local), restart/test. Keep dump rollback. App init script reads env so initializes remote DB automatically.
2. Use s3 as app worker eventually (DNS registration via pdnsutil needs s1 DB configuration; nginx initialize route pinned s1). Cache reset/init semantics matter.
3. Profile, reduce cross-request immutable data queries with correct initialization invalidation; DNS TTL cache; tune pool.
4. Reboot/data durability validation before deadline and final reboot benchmark.

## Update 02:30 UTC
- 008 pass105976. Migrated isupipe DB s1->s2 via backups/isupipe-before-s2.sql, checked comments6921/tip105976/icons973 equal, then s1 env DB host10.99.0.12. API/payment returned105976 before new benchmark.
- 009 DB split pool10: pass63538. 010 pool40 + s1 DNS mysql config: pass79068. All configs mysql-s1.cnf/mysql-s2.cnf installed under /etc/mysql/mysql.conf.d/zz-contest.cnf.
- Nginx sites-enabled/isupipe.conf is a REGULAR FILE, not symlink. First copy to sites-available did not change loaded config, so 010 is only pool40. Fixed actual sites-enabled and nginx -T verifies upstream s1/s3. 011-three-hosts.json RUNNING session2652.
- s3 app enabled and serving via s1 nginx (verified TCP s1->s3:8080). Both apps DB s2. s3 pdns service disabled, pdnsutil uses config gmysql-host10.99.0.11. s1 grants isudns@10.99.0.13 all on isudns.* and listens0.0.0.0. Verified pdnsutil list-zone from s3. Initialization nginx exact route pinned s1; DNS remains s1. s3 env copied s1 incl DNS address10.99.0.11.
- Go toolchain/module cache copied to ignored toolchain/ on agent; scripts/build-app.sh now builds locally, scripts/deploy-app.sh accepts host arg (default s1). Builds no longer consume competition server CPU. Deploy BOTH s1 and s3 for app updates.
- Pending uncommitted userResponseRow JOIN optimization (themes+hash / user+theme+hash), built into artifacts/isupipe but NOT deployed. Current servers have pool40+request cache+reservation joins.

## Update 02:42 UTC
- 011 three hosts pass90840; 012 user JOIN pass99888; 013 positive DNS cache/TTL60 pass102317; 014 icon JOIN+pprof pass108798 but package install during run auto-added lmdb backend on s3, causing registration failures. Removed package /etc/powerdns/pdns.d/lmdb.conf to /etc/powerdns/lmdb.conf.package-disabled on BOTH s1 and s3.
- Production DNS now LMDB on s1. Config config/pdns-lmdb.conf installed /etc/powerdns/pdns.conf (NO include-dir). File /var/lib/powerdns/isupipe.lmdb, shards4, sync-mode=sync. Existing zone exported backups/dns-before-lmdb.zone and imported as pdns user. API binds8081 allowed private subnet; apps use HTTP PATCH for DNS registration (app/dns.go), removing pdnsutil subprocess. API record add and daemon restart persistence verified using lmdbsmoketest. Init script sudo-u-pdns loads zone, chmods temporary directory755+file644, then restarts pdns. Test instance s3 :5353 was stopped.
- 015 failed initialize due tempdir permissions; fixed. 016 LMDB pass159111, DNS171337. 017 bulk hydration pass154604,DNS165865 (score similar; pending topology comparison).
- Current deployed source 52ee305 plus committed DNS/API fixes; both app hosts same source. Pprof loopback6060 enabled; profiles logs/014*. CPU mostly syscalls,bcrypt; app only ~0.35 cores then.
- RUNNING scripts/compare-upstreams.py session35816: serial benches018 s3only,019 weights1:3,020 balanced. Installs best measured config at end to actual /etc/nginx/sites-enabled/isupipe.conf, writes artifacts/nginx-selected.conf and logs/upstream-comparison.json. Do not start competing bench/deploy/reload until runner ends. config/nginx-s1.conf still balanced template; must sync final selected config after review.
- Pending scripts/differential-read-test.py for comparison to baseline on frozen cloned database, not yet executed. No test servers/tunnel set up yet. Plan clone existing backups/isupipe-before-s2.sql to s3 local unused MySQL under isupipe_dev; run local agent baseline/optimized binaries at18080/18081 with ssh tunnel to s3 mysql, ensure only dev schema affected. This should wait until topology runner finishes to avoid skewing measurements.

## Update 02:51 UTC
- Upstream comparison finished: 018 s3only pass161344;019 weighted1:3 pass151868;020 balanced pass150932. Selected s3only; config/nginx-s1.conf and loaded sites-enabled now s3-only upstream, /api/initialize still s1. Both app services remain active.
- Differential testing on cloned snapshot in s3 local database isupipe_dev compared 38 responses incl active streams7538/7532, all stats, icons and conditional GET; all matched baseline (tag arrays normalized as unordered). Logs differential-read*-results.txt. Local reference/optimized test processes and SSH tunnel stopped. test-app/ sources/binaries remain ignored. Dev schema retained (not used by production; production always s2/isupipe).
- 021 one-second ETag cache pass199511,DNS165151. Cache expires from request/read start, not cache insertion/hit time. Internal reset endpoint IP restricted, initializes clears both apps before/after SQL reload. Cross-host icon propagation test observed1.006sec and validated304/200/currenthash. Unit tests guard expiration and pre-initialize in-flight cache stores.
- 022-user-cache.json RUNNING session76393. Current source adds1-second shared user response cache, only GET results may populate it to avoid leaking uncommitted writes; POST/icon invalidates local cache and cross-host expiration is1sec. Tests pass, cross-host script repeated. New files app/user_cache.go,user_cache_test.go, modified response_cache.go/user_handler.go/icon_cache.go and check-icon-propagation.py are deployed but not yet committed.

## Update 03:01 UTC
- 022 user cache pass206744,DNS157901. 023 immutable stream metadata cache pass211108,DNS147147. Stream model+tags are immutable; owner response is not cached with them. Only GETs populate shared metadata, and request-start generation isolates initialize from in-flight reads. Tests cover nonpublication of uncommitted reservations and generation isolation. Deployed on all three apps.
- Added s2 application service (enabled) alongside authoritative isupipe DB. s2 env uses TCP127.0.0.1 DB and DNS address10.99.0.11. Both initializations and cache reset now include all three app endpoints; /internal/cache-reset permits only loopback and s1/s2/s3 IPs.
- RUNNING scripts/compare-colocation.py session83713, serial024 s2only /025 s2_auth3 /026 s2_s3_balanced /027 s3only. 024 pass263654;025 pass264683. Runner changes nginx routing only, baseline code identical. Will install best config and write artifacts/nginx-colocation-selected.conf + logs/colocation-comparison.json. Do NOT deploy/reload/run another bench until done.
- Pending (built artifacts/isupipe but NOT deployed): direct cache fast paths for GET user/me/theme; tag catalog cache cleared on init; user cache by-name index; configurable ISUCON_DB_MAX_CONNS (default40) and optional ISUCON13_MYSQL_SOCKET override in main.go. All unit tests pass. Build session76177 may need completion poll.
- Profiling023 on s3 shows bcrypt17%, syscalls15%,futex8%, malloc9% cumulative. bcrypt algorithm/cost unchanged. Plan test Unix socket on app2 after current topology comparison, then pool/GOMAXPROCS experiments.
- Colocation comparison completed:026 balanced217222;027 s3only212188. Selected s2_auth3 (best264683) and copied selected nginx config to config/nginx-s1.conf. Current public normal API routes app2 (DB local), login/register app3 (DB remote), initialize app1. All apps enabled.
- Upcoming deploy includes tag/user fast paths and socket/pool configuration support. scripts/run-bench.py saves actual server binary/env/config hashes and nginx config before each run. Use this wrapper for new manual runs (one bench at a time).
