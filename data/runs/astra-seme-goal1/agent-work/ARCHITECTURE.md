# Target architecture (decided 2026-11-17 03:11 UTC)

Initial benchmark: PASS 7,532 (logs/bench-initial.log).

- s1: nginx TLS/static files; authoritative DNS with exact registered-name set in memory (unknown names return NXDOMAIN).
- s2: Go API; all domain data in memory, indexed by user/tag/stream; incremental aggregates. Durable snapshot + synchronous append-only event log, replayed at startup. Initialization atomically switches generations.
- s3: bcrypt computation service (same algorithm/cost as original).

All runtime computation and persistence stays on the three competition servers. Password hashing remains bcrypt.MinCost (original value). Icons implement conditional GET using current SHA256. API authorization and reservation capacity remain enforced. Initial SQL is imported once as the canonical baseline; initialize restores that baseline, never benchmark-derived responses.

Before finishing: benchmark repeatedly, tag highest passing commit, deploy highest state; verify data survival on reboot and benchmark after reboot. Competition deadline 2026-11-17 11:09 UTC.
