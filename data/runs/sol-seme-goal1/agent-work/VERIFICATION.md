# Verified result

- Initial implementation: PASS, 9,059 points (2026-11-17 02:27 UTC).
- Selected implementation: PASS, **247,665 points** after rebooting **all three servers** (2026-11-17 03:04 UTC), 27.34 times baseline.
- Authoritative benchmark output: `results/reboot-periodic.json`.
- Boot IDs, active services and identical binary SHA256 across all hosts: `results/boot-periodic.json`.
- All 12 API responses matched exactly before and after the full reboot, including user, theme, icon, reservation, viewer count, reactions, comments, moderation, report history, rankings and total payment. Run `python3 tests/durability.py create`, reboot all hosts, then `python3 tests/verify_boot.py` and `python3 tests/durability.py check`. A benchmark initialization intentionally clears these fixtures.
- `go test ./...` passed, covering persistent generations, interrupted log tails, subsequent writes after recovery, reported comments retained after moderation, indexes, ID counters and SQL wildcard patterns.

# Selected roles

- s1: nginx TLS/static frontend and authoritative in-memory DNS, exact-name responses, unknown names return NXDOMAIN.
- s2: authoritative Go memory state, indexed searches, incremental statistics, persistent snapshot and append-only log. MySQL supplies only initial data.
- s3: bcrypt service retaining the original algorithm and cost 4.
- All services are enabled at boot. A separate service advertises each server's own competition address after reboot.

Every acknowledged mutation has been written to its log file. Disk synchronization runs every 20ms and at initialization barriers; normal OS reboot and process restart preserve acknowledged data, as tested. Sudden power loss can lose up to the synchronization window. Initialization atomically selects a matching snapshot/log generation.

Deployment: `./deploy/deploy.sh`. This builds and tests the source, installs the same binary on all three hosts and configures enabled services without deleting persisted application data.

Direct DNS distribution to s3 was rejected by the provided benchmark's server list. Internal TLS distribution, JSON gzip and lower frontend file limits were measured and not selected. Their results remain in `results/` and git history.

The selected benchmark reports general errors, principally the moderated-spam status discrepancy explicitly described in the supplied application manual and exhausted reservation capacity. It reports PASS, with no missing-field specification errors or DNS timeout.
