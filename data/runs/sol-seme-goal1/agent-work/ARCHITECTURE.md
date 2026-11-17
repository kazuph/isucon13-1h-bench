# Target architecture

Baseline: 2026-11-17 02:27 UTC, PASS 9059, DNS resolved 11583.

- s1: authoritative in-memory DNS with exact registered names, TLS and static frontend proxy.
- s2: authoritative Go application, indexed memory state, incremental statistics; snapshot plus append-only mutation log, with complete file writes before acknowledgment and disk synchronization every 20ms. MySQL only supplies initialization data.
- s3: bcrypt service retaining algorithm and cost 4, offloading registration/login CPU.

All acknowledged writes must survive service restart and normal OS reboot. Power-loss durability has a 20ms sync window. Initialization restores every original table and DNS record. Unknown DNS names return NXDOMAIN. User/icon updates are immediately visible. Ranking tie breakers follow original implementation. Best passing revision is tagged and restored before finishing; reboot verification required.
