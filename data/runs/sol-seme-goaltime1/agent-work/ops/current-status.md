# Current competition state

Deadline: 2026-11-18 11:17 UTC (20:17 JST). Keep improving until then; no server operations after deadline.

Best confirmed: 216580, commit 47fc3ef, tag best-216580. Initial score 9401.
Deployment: s1 nginx TLS/static files + custom memory DNS on UDP53 + local MySQL isudns; s2 memory application on8080, synchronous persistent mutations to s3; s3 MySQL isupipe.

2026-11-18 03:38 UTC: rebooted all three servers. All 13 sampled API responses and CHECKSUM TABLE for all12 application tables were byte-for-byte identical before/after restart. Snapshots results/recovery-before.json and recovery-after.json.
A post-reboot benchmark is running, output results/reboot-confirm.json.

Pending local changes: metrics.go and main.go middleware for aggregate request latency measurements; not deployed yet.

Known findings:
- Initial N+1 DB reads replaced by complete memory model, rebuilt at startup/initialize.
- Global mutation lock replaced with per-entity locks. Read-model lock released during SQL waits.
- MySQL READ COMMITTED avoids icon delete/insert predicate gap-lock deadlocks.
- Nginx upstream keepalive is essential. Without it ephemeral port exhaustion generated huge502 volumes and benchmark output jq Argument list too long. Corrected in c675f04.
- Custom DNS stores records in local s1 DB, loads into atomic snapshots, receives synchronous HTTP updates from s2 after persistence.
- bcrypt algorithm/cost unchanged, as required.
- All services enabled for reboot. s2 env archived ops/s2.env.
