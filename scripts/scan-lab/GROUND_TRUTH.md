# scan-lab — ground truth (expected scan results)

Use this to assert the scanner found what it should. Stock images with default
config already violate many CIS controls; the expected control IDs map to the
live bundles in `bundles/`.

## Discovery (naabu → httpx → nuclei service/version)
| IP | Port | Expect service detected | Expect asset/endpoint |
|---|---|---|---|
| 192.168.0.230 | 5432 | postgresql 16 | 1 asset, 1 endpoint |
| 192.168.0.231 | 27017 | mongodb 8 | 1 asset, 1 endpoint |
| 192.168.0.232 | 1433 | mssql / ms-sql-s 2022 | 1 asset, 1 endpoint |

## Authenticated CIS compliance (requires credential mapping)

### postgres-16 (192.168.0.230, creds postgres/scanlab-pg) — expect FAIL
- `6.8` TLS not enabled (no SSL) · `6.9` TLS min protocol · `6.10` weak SSL ciphers
- `3.2` pgaudit not installed
- `3.1.20` log_connections off · `3.1.21` log_disconnections off · `3.1.25` log_statement none · `3.1.24` log_line_prefix default
- `4.8` set_user extension absent · `5.5` per-account connection limits unset · `7.4` WAL archiving off
- (PASS expected on some logging defaults — record actuals on first run and pin them.)

### mongodb-8 (192.168.0.231, no auth) — expect FAIL
- `2.1` authentication not configured · `2.2` localhost auth bypass
- `4.1`/`4.2`/`4.3` TLS not enabled · `4.4` FIPS off
- `5.1` system activity not audited · `6.1` running on default port 27017

### mssql-2022 (192.168.0.232, creds sa/Scanlab!2022) — expect FAIL
- `2.13` sa login not disabled · `2.14` sa not renamed · `2.16` sa login name present
- `2.11` non-standard port not set (on 1433)
- (other 2.x defaults — record actuals on first run and pin them.)

## Network vuln pass (ADR 019 P2 — default-OFF)
Deferred to the network tier (`.233–.236`, follow-up). Once P2 is activated
(v0.1.109 + flag), the vuln images there should produce `network_vuln` findings;
pin expected CVEs/template IDs when that tier lands.

> First run: treat this as the baseline — capture actual results, reconcile any
> diffs vs. expectations, and lock the numbers so future runs are a regression check.
