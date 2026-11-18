# Target architecture

- s1 (10.99.0.11): nginx TLS/static files, authoritative PowerDNS. DNS records persisted in local isudns MySQL.
- s2 (10.99.0.12): Go application, complete in-memory read model. Synchronous durable SQL writes on s3 before updating memory and acknowledging requests. DB reload on startup and initialize.
- s3 (10.99.0.13): authoritative isupipe MySQL persistent data.

Initial benchmark: pass, 9401 points. Initial commit tagged baseline-9401.
Competition ends 2026-11-18 11:17 UTC. No operations after deadline.
