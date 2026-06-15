# scan-lab — deliberate scan targets for validating the LAN scanner

A self-contained k3s namespace of **known-ground-truth** targets so we can validate
SilkStrand's scan pipeline end-to-end and as a regression harness:

- **Discovery** (naabu → httpx → nuclei service/version detection)
- **Authenticated CIS compliance** (the live bundles: PostgreSQL 16, MongoDB 8, MSSQL 2022)
- **Network vuln pass** (ADR 019 P2 — *default-OFF until v0.1.109 + flag*; targets are ready for when it's on)

> ⚠️ These are **deliberately insecure** workloads. They are LAN/cluster-only,
> network-isolated (NetworkPolicy in `00-namespace.yaml`), and meant to be **ephemeral** —
> `apply.sh` to spin up for a test run, `teardown.sh` after. Never expose to the internet.

## How targets are reachable (kube-vip LoadBalancer)

The cluster uses **kube-vip** (pool `192.168.0.220–240`; `.220` traefik, `.221` plex in use).
Each target is a `type: LoadBalancer` Service with a **pinned** LAN IP via the
`kube-vip.io/loadbalancerIPs` annotation, on its **standard port** (realistic for
service/version detection). The lan-agent scans these like any other LAN host.

| Target | LAN IP | Port | Image | Creds (for authenticated scan) |
|---|---|---|---|---|
| postgres-16 | `192.168.0.230` | 5432 | `postgres:16` | `postgres` / `scanlab-pg` |
| mongodb-8   | `192.168.0.231` | 27017 | `mongo:8` | *(no auth — intentional)* |
| mssql-2022  | `192.168.0.232` | 1433 | `mcr.microsoft.com/mssql/server:2022-latest` | `sa` / `Scanlab!2022` |
| *(network tier — follow-up)* | `.233–.236` | — | vuln web/service images | n/a |

## Setup checklist

1. `./apply.sh` — creates the `scan-lab` namespace, NetworkPolicy, and the targets.
2. **Allowlist** — add the target IPs to the lan-agent's scan-allowlist (see `allowlist-snippet.yaml`),
   else every recon directive is gated out.
3. **Credentials** — in the tenant UI create `credential_sources` (static) with the creds above
   and map them to the DB targets, so the authenticated CIS bundles can run.
4. Kick a discovery scan, then the CIS scan-definitions. Compare against `GROUND_TRUTH.md`.
5. `./teardown.sh` when done.

See `GROUND_TRUTH.md` for the expected findings/CIS-control failures per target.
