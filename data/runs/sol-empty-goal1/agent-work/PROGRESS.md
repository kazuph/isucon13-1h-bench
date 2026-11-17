# ISUCON progress

Conditions read: 2026-11-17 09:30–17:30 JST (00:30–08:30 UTC), s1/s2/s3 only, bench targets s1. Final reboot + benchmark required after competition. No delegation authorized. Root AGENTS.md empty.

Initial s1 implementation copied to baseline/go and baseline/sql. Current development files tuning/go, migrations tuning/*.sql. Servers have Go /home/isucon/local/golang/bin/go (not default SSH PATH).

Applied s1:
- tuning/indexes.sql app indexes
- tuning/03-migrations.sql DNS records name/type index, icons stored generated SHA256 image_hash
- tuning/go stats group aggregation, icon hash lookup + conditional GET, bulk SQL moderation
- response_cache.go and middleware: request-local User/Livestream response reuse

Results:
1 baseline PASS 9095, DNS 11646 (stdout only)
2 indexes + stats PASS 29806, DNS 17032; results/02-index-stats.json
3 DNS + icon hash + moderation PASS 32215, DNS 86068; results/03-dns-icon-moderation.json
4 request cache BENCH RUNNING session 68094; results/04-request-cache.json

Not yet deployed: tuning/go/user_handler.go postIconHandler uses READ COMMITTED + parent-user row lock to fix icon delete/insert gap lock deadlocks. Deploy AFTER current bench ends.

s2/s3 still untouched, original running all services. Potential next architecture: s2 app DB, s1 nginx/app/DNS, s3 app, s1 nginx balancing app to s3; app shared sessions secret original. Need evaluate after local optimizations. s1 mysql currently bind 127.0.0.1, binlog ON, flush=1, sync_binlog=1, buffer pool 128M. Preserve durability. DNS logs verbose /etc/powerdns/pdns.conf, cache TTL 0; name index already fixed biggest scan.

All services enabled defaults; MUST verify reboot recovery and persisted data before final. Competition deadline 08:30 UTC. Do not modify environment checker etc prohibited by manual.

Later updates (00:50 UTC):
- s2 now app DB primary, s1 app and s3 app use 10.99.0.12. Data migrated with mysqldump. s2 mysql bind0.0.0.0, 2G buffer, account isucon@10.99.0.% (password original isucon). /etc/mysql/mysql.conf.d/zz-isucon-tuning.cnf from tuning/config/s2-mysql.cnf. s2 isupipe-go disabled.
- s1 nginx uses tuning/config/s1-nginx-balanced.conf. /api/register and /api/initialize pinned local8080, other API round robin s1/s3. DNS records remain local s1, all A=s1.
- main.go InterpolateParams=true, MaxIdleConns10/Open10, Debug=false.
- Request cache now includes ByID helpers to eliminate repeated user/model queries. fallback default icon hash cached once.
- postIcon READ COMMITTED + parent user lock applied. No later icon deadlock messages.
- livecomment spam check one EXISTS with original utf8mb4_general_ci. Bulk moderation explicit same collation. Do not change bcrypt algorithm/cost.
- tags use join one query; batch response prefetch implemented response_batch.go and called from comment/reaction/stream list handlers.
- Current both binaries same full batch; batch versions 08,09,10 PASS but scores 75k->17k->15k, benchmark host_loadavg up to70. Direct API compare full batch vs no batch exact matches; search50 median 7.57ms batch vs96.07ms no batch. variants/without-batch contains earlier alternate sources (not latest report fix).
- reservation scan bounded start_at<end too; verified all reservation slots end>start, EXPLAIN shows4rows instead thousands. Applied both apps.
- PowerDNS config current tuning/config/pdns-cached.conf: query-cache20, negative0, zonecache300, metadata60, packet0, logs quiet. s1 webapp/sql/init.sh now tuning/init.sh (chmod+x required), adds sudo pdns_control purge after zone reset. Regular user pdns_control permission denied, sudo works.
- results/06 PASS66111, 07 PASS77352 (best), 08 PASS75850 (mixed s1 batch/s3 old),09 PASS17797 (both batch),10 PASS15706 (reservation+positive DNS cache).
- Current BENCH session30312 result results/11-report-list.json: report list query joins existing comments to avoid500 when moderated comment removed, preserves report records for stats. No result yet.
- s3 binary mv /tmp next initially failed textfilebusy, fixed atomic mv, subsequent go build handles safely. Both current source and binary synced except gofmt local formatting differences.
Next consider A/B disabling batch calls again if scores remain bad; direct responses already compared in results/08-response-comparison.txt (older comparison). Need final durable data checksum/API persistence then reboot ALL 3 and bench after services recover; final result and solution note still missing.

Latest 01:15 UTC:
- Current app version: shared response cache TTL500ms on User/Livestream; origin anchored request-start expiry, publish only after successful handler return (transactions committed), cache hits do NOT extend TTL, bounds10000 entries/map. Nested owner's icon age <=1sec (two500ms caches), requirement2secs. Init clears s1 locally then s3 HTTP POST /internal/cache/clear, route not exposed via nginx.
- Cache source response_cache.go; tests response_cache_test.go. `go test ./...` and `CGO_ENABLED=1 go test -race ./...` PASS on s3. Needed gcc/libc6-dev installed on s3 for race; normal app built CGO0 and same SHA on s1/s3. Candidate500ms icon check results/16-icon-cache-check.txt: both updated in~0.55sec. Earlier250ms check~0.3sec.
- Connection pool32/Idle32 each app; nginx s1 config/s1-nginx-weighted.conf sends s1:s3=1:3 except register/initialize local only. Current best result16 PASS123783 vs baseline9095. Result15 shared cache250ms PASS109197. Result14 group commit PASS93498.
- DNS config current config/pdns-negative-cache.conf (local MySQL s1, pos20sec/neg1sec, zone300sec/meta60sec/logquiet). Register executes sudo pdns_control purge after pdnsutil add-record; init.sh sudo purge after zone reset. Tested cached NX ->register->immediate valid A. DNS indexes restored via dumps, records_name_type present.
- s2 mysql config current tuning/config/s2-mysql.cnf: bind0.0.0.0, pool2G, binlog_group_commit_sync_delay1000us, no_delay_count10; flush_at_commit1/sync_binlog1 unchanged. Enabled persist.
- Failed experiments: response_batch prefetch (variants/full-batch, removed from production); remote DNS DB on s3 (config/pdns-s3-db*.conf) tried and reverted after low scores/timeouts; gzip JSON tried and removed after no benefit.
- Remote DNS attempt initially overridden by /etc/powerdns/pdns.d/gmysql-host.conf=127, fixed and verified s3 connections, still slow even12distributor threads. All DNS data later copied BACK from s3 to s1. Current /etc/powerdns/pdns.d/gmysql-host.conf local127.0.0.1 from config/pdns-backend-local.conf. s3 MySQL zz-isucon-dns.cnf removed, restartedMySQL+Go, bind back127.0.0.1. Remote grant isudns@10.99.0.11 remains but unused.
- gzip config /etc/nginx/conf.d/json-gzip.conf moved to /etc/nginx/json-gzip.conf.disabled just now. Main nginx no application/json gzip anymore.
- benchmarks:17 ineffective DNS migration PASS27515,18 remote actual PASS53404+DNS timeouts,19 remote12threads+gzip PASS28879,20 DNSlocal+gzip PASS38188. Benchmark hostload varied widely8..70, low scores correlate high load. Best16 hadhostload8.
- NEXT experiment SQLite DNS backend to remove s1 MySQL DNS CPU while preserving pdnsutil CLI/initialization/durable data. apt update+install pdns-backend-sqlite3 sqlite3 RUNNING s1 session4081. No benchmark running now. Need read official installed schema/docs, migrate all isudns tables, config launch=gsqlite3 (remove gmysql settings and include gmysql-host), db at /var/lib/powerdns/... with pdns/isucon group write; WAL/FULL durability. Not yet implemented. Rollback MySQL data in s1 intact; original configfiles saved.
- Final verification STILL NOT DONE: tuning/check-persistence.py save+compare checksums12app+2DNS(MySQL currentlys1), newestuser/stream/icon/payment/DNS. Must adapt if SQLite chosen. Need all3 reboot, health recover, compare prepost state BEFORE bench resets DB, then benchmark after reboot and final docs/result.
- Artifact tuning/deploy-app.sh builds Go on s1/s3 trimpath CGO0 atomic rename and restart, removes obsolete response_batch.go. Local tuning/go sources copied gofmt'd from s1. Removed stale local tuning/go/isupipe copied baseline binary, baseline binary preserved. Root repo no git. AGENTS empty. No agents authorized. Manual prohibits bcrypt changes etc complied.

01:22 UTC update:
- SQLite DNS backend now ACTIVE s1. Installed pdns-backend-sqlite3/sqlite3 (PowerDNS unchanged4.9.18). Official schema copied tuning/schema.sqlite3.sql; migration script tuning/migrate-dns-sqlite.py exported all7 MySQL isudns tables via XML to tuning/isudns.sqlite3, installed /var/lib/powerdns/isudns.sqlite3 pdns:pdns0660, dir0775. isucon added supplementary pdns group; Go restarted and group107 verified. First CLI in same SSH before group refresh failed, new SSH works.
- Current DNS config tuning/config/pdns-sqlite.conf launch=gsqlite3, database above, pragma-journal-mode=WAL, pragma-synchronous=2(FULL). Removed gmysql config lines; /etc/powerdns/pdns.d/gmysql-host.conf moved to /etc/powerdns/gmysql-host.conf.mysql-backup. s1 MySQL DNS data old backup only now (not authoritative). Go pdnsutil CLI unchanged. Cached NX->register->immediateA passed. SQLite PRAGMA wal/2/integrity_checkok verified.
- Benchmark21 SQLite PASS85588, DNS156368, hostload34.74. s1 MySQL DNS connections zero now, CPU lower. SQLite retained so far.
- gzip was moved outside conf.d, OFF; don't describe it as current in final README.
- NEXT benchmark22 currently RUNNING session68474, results/22-no-binlog.json. s2 mysql config changed to skip-log-bin while innodb_flush_log_at_trx_commit=1 explicit. Backup s2-mysql-with-binlog.cnf has oldgroupcommit settings. `mysqld --validate-config` passed, restarted s2mysql and both Go apps, verified log_binOFF and flush1. No replication used. Durability maintained by synced InnoDB redo.
- Persistence checker now DNS logical SHA256 of SQLite .dump on s1, plus app MySQL12tablechecksums and APIlatestuser/stream/icon/payment/DNS. Still must save state, rebootALL3, awaithealth, compare snapshot BEFORE bench, then final postreboot bench.
- Current Go unchanged from16, maxpool32, sharedcache500ms, nginx1:3 weighted. Source gofmt copied froms1 locally. All sources and binary same s1/s3; tests passedrace. Stale local tuning/go/isupipe removed, baseline preserved. deployscript explicit CGO_ENABLED=0.

01:28 UTC final verification progress:
- Result22 no-binlog PASS112066 / DNS169114. Current chosen config: s1 DNS SQLite WAL FULL, s2 appMySQL redo flush1 and binlogOFF, app pool32/cache500ms, nginxweighted1:3; gzipOFF.
- check-persistence.py save results/pre-reboot-state.json selected new bench user shohei440, stream8893, full12app checksums and7tableSQLite logicaldump hash plusprofile/icon/stream/payment/DNS.
- ALL3 sudo reboot performed 01:25:54/55 UTC. BootIDs changed on all3 (results/pre-reboot-s*.txt vs post-reboot-s*.txt). Requiredservices enabledunchanged. Healthpoll30341 completedPASS allservices/APIremoteDB/DNS.
- Persistence comparison PASS, exactallchecksums/APIvalues includingnewbenchdata, results/reboot-persistence-check.txt. This was BEFORE newbench initialization.
- Postreboot query proves SQLite wal, synchronous2 and configgsqlite3full; s2 log_binOFF, flush_at_commit1, buffer2G. Both binary SHA256 5a8a46685fe7fd7b64c6b2368cf2d6706935f74c2a1c9015fe243931fe9a73b0.
- FINAL BENCH NOW RUNNING session94050, results/23-post-reboot.json. AwaitactualJSONpassscore. No more edits planned. Afterpass write RESULT.md with finalscore/baseline9095, finalartifactlinks, then update_goal complete afteraudit. Need not wait untildeadline08:30; condition describes later evaluatorreboot, rehearsalprovedsurvival. Don'tclaimcompetitionalreadyended.
- README rewritten currentchosenarchitecture/config/durability/tests. It has SQLite notMySQLDNS now. Earliercandidatefiles remain comparisonartifacts. Noagentdelegation used.
