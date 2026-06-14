# ADR 019: All-ports enrichment — service detection + network vulns (supersedes ADR 009)

**Status:** Proposed
**Date:** 2026-06-14
**Supersedes:** [ADR 009](./009-service-detection.md) (service detection via
nuclei-network — ratified and extended here).
**Related:** [ADR 003](./003-recon-pipeline.md) (recon pipeline — `EnsureTool`,
naabu/httpx/nuclei), [ADR 006](./006-asset-first-data-model.md)
(`asset_endpoints.service`/`version`/`technologies`), [ADR 012](./012-exploit-validation.md)
(version → exploitability), [ADR 017](./017-discovery-enrichment-crawl-secrets.md)
(depth tiers — the web-crawl axis; this is the non-web/all-ports sibling axis),
issue #377 (nuclei against all listening ports).

---

## Context

The discovery pipeline (`naabu → httpx → nuclei-HTTP`) is **web-only past the
port scan**: httpx fingerprints *web* tech, and nuclei runs the full template set
but only ever *sees* the httpx URLs. So every non-HTTP open port — SSH, MySQL,
Redis, Postgres, SMTP, SNMP, raw TLS — gets a bare port number and nothing else:

1. **No technology map.** `asset_endpoints.service`/`version` (columns that have
   existed since ADR 006) show `-` for everything non-web. Operators can't build
   "all postgres" collections or see what's actually running.
2. **No vulnerability coverage.** nuclei's network/protocol templates
   (exposed-redis, weak-TLS, default-creds, etc.) never fire because the host:port
   pairs are never handed to nuclei (issue #377).

[ADR 009](./009-service-detection.md) already designed the fix — a `nuclei-network`
stage — but it was never built (the columns are still empty) and predates both the
#377 vuln motivation and ADR 017's depth-tier control. This ADR ratifies ADR 009's
stage, folds in #377, resolves the tool question (nuclei-network vs nmap), and adds
the depth/resource controls the OOM work taught us to bake in up front.

### What already helps (this is not greenfield)

- **The tool is already here.** The agent downloads + runs nuclei; the templates
  bundle (`EnsureTemplates`) ships the `network/` templates. No new dependency.
- **The data model is ready.** `asset_endpoints` already has `service`, `version`,
  `technologies` (ADR 006) — waiting to be backfilled.
- **The pipeline seams exist.** `recon/pipeline.go` stages + `recon/nuclei.go`'s
  `runNuclei(urls)` — a sibling `runNucleiNetwork(hostPorts)` slots in (ADR 009 D5/D6).
- **ADR 009 did the hard design** — stage placement, template selection, the
  service-name map, httpx-input narrowing, allowlist interaction. Ratified below.

## Decision

### D1. One `nuclei-network` stage over naabu's open ports (ratify ADR 009)

Between naabu and httpx, run nuclei against the `host:port` list (not URLs) using
the **network** template protocol. This single stage does **both** jobs:

```
naabu → nuclei-network (ALL ports) → httpx (HTTP ports only) → nuclei-HTTP (URLs)
```

It reuses the nuclei binary + the pinned template bundle (no new tool, no separate
download), per ADR 009 D1.

### D2. Broader template selection than ADR 009 — detection **and** vulns (#377)

ADR 009 scoped to `network/detection/` (service ID only). This ADR widens it to the
`network/` tree — **detection** templates (service identification) **plus** the
network **vuln** templates (#377): exposed services, default credentials, weak
TLS/ciphers, info leaks. Detection findings → service/version backfill (D3); vuln
findings → `network_vuln` findings. Same pass, one template root, severity-filtered
by depth (D4).

### D3. Outputs — backfill the tech map + emit vulns

- **Service/version map:** detection hits backfill `asset_endpoints.service` (from
  a static template→service map: `mssql-detect → mssql`, `ssh-detect → ssh`, …) and
  `version` (from `extracted-results`), per ADR 009 D3. Backfill only when `service`
  is NULL — httpx's later HTTP fingerprint wins for web ports.
- **Vulns:** network vuln hits → `findings` (`source_kind=network_vuln`,
  `source=nuclei-network`). Version on the endpoint makes these **exploitability-aware**
  (ADR 012) — a CVE tied to "MySQL 8.0.32", not just a banner.
- **httpx narrowing (ADR 009 D4):** ports identified as non-HTTP are excluded from
  httpx (faster, more accurate); unmatched ports still fall through to httpx.

### D4. Depth-gated (ADR 017 alignment) — the runtime/memory control

The all-ports pass adds nuclei load on top of the web pass we just OOM-fixed at 2Gi.
So it is **gated by depth tier**, not always-on:

| Tier | nuclei-network |
|---|---|
| **Quick** | off (today's web-only chain) |
| **Standard** | detection + low-risk vuln templates, severity ≥ medium |
| **Deep** | full `network/` set, all severities |

Tier resolves server-side into the template/severity flags + caps, clamped to the
per-chunk runtime budget (ADR 017 D4 / the chunk lease).

### D5. Tool choice — `nuclei-network` now, nmap `-sV` deferred

**Use nuclei-network, not nmap, for the base capability.** Rationale: it's already
in the stack (Principle #8 — minimal deps, single-person sustainability), reuses the
pinned template bundle, and covers the common services. nmap `-sV` is *more*
accurate (comprehensive probe DB, arbitrary-protocol version detection) but is a
heavy new dependency (~10–20 MB binary + data, different vendor, slower, more
intrusive). **Defer nmap `-sV` as a Deep-tier accuracy enhancement** — add it only
if nuclei-network's service/version coverage proves insufficient in practice. PD's
`tlsx` is a lighter, in-stack complement for TLS-port fingerprinting if richer TLS
data is wanted before nmap.

### D6. Principles unchanged

Runs **on the agent** (customer network); only structured results (service/version
strings + findings) cross the tunnel (Principle #1). Allowlist-gated like every
recon stage (ADR 009 D9). No new runtime tool in the base design.

## Consequences

- **The tech map fills in** — non-web `service`/`version` populate; service-based
  collections and version-aware findings become possible.
- **Non-web vuln coverage** (#377) — exposed Redis, weak TLS, default creds, etc.
- **More nuclei work** — bounded by D4's depth gating + the chunk runtime budget;
  Standard stays close to today, Deep is opt-in. Validate the memory budget the same
  way the 2Gi fix was validated (no OOM under the all-ports load).
- **Still zero new dependencies** in the base — nuclei + the existing bundle.
- **httpx gets faster** (narrowed input) — partial offset to the new stage's cost.

## Resolved decisions

- **Why supersede ADR 009, not amend?** 009 was Proposed-but-unbuilt and scoped to
  detection-only; this folds in #377 vulns + depth-gating + the tool decision into
  one current source of truth. 009's stage design is ratified, not discarded.
- **Why nuclei-network over nmap?** Minimal-deps + already-in-stack beats nmap's
  extra accuracy for the base; nmap is the deferred Deep enhancement (D5).
- **Why one stage for detection + vulns?** Same input (host:port), same binary,
  same template tree — splitting them would double the nuclei passes.
- **Why depth-gate?** The OOM lesson: don't add unbounded nuclei load. Standard
  stays cheap; Deep is explicit.

## Scope / phasing

- **P1 — service detection** (ADR 009's core): `runNucleiNetwork` + service/version
  backfill + httpx narrowing. Fills the empty columns; immediate operator value.
- **P2 — network vulns** (#377): widen templates to the vuln set, emit `network_vuln`
  findings, wire depth-gating (D4).
- **P3 — nmap `-sV` Deep enhancement** (deferred): only if P1/P2 coverage proves
  insufficient; gated to Deep, validated against the memory budget.
