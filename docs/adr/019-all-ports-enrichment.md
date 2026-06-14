# ADR 019: All-ports enrichment — service detection + network vulns (supersedes ADR 009)

**Status:** Accepted
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

## Decision

### D1. One logical `nuclei-network` stage over naabu's open ports (ratify ADR 009)

Between naabu and httpx, run nuclei against the `host:port` list (not URLs) using
the **network** template protocol. One logical stage (one progress unit, one
`stage=nuclei-network` batch stream), reusing the nuclei binary + the pinned
template bundle — no new tool, no separate download (ADR 009 D1).

```
naabu → nuclei-network (ALL ports) → httpx (HTTP ports only) → nuclei-HTTP (URLs)
```

### D2. Two sub-passes within the stage — detection and vuln are NOT one invocation

The `network/` tree is heterogeneous: `network/detection/` templates are almost
entirely `severity=info`, while the rest (`cves`, `exposures`, `misconfig`,
`default-login`, `enumeration`, `c2`, `honeypot`, …) span all severities and very
different risk/parsing/persistence profiles. So a single invocation with a global
`-severity` filter is wrong — `-severity medium+` would drop *all* service
detection. The stage therefore runs **two nuclei invocations** over the same
host:port list:

- **Detection sub-pass** — `-t network/detection/`, **no global severity filter**
  (info allowed). Output → service/version backfill (D3). **Never** written as findings.
- **Vuln sub-pass (#377)** — a **curated** set of network vuln template paths/tags
  (D4's category allowlist), severity/category-filtered by tier. Output →
  `network_vuln` findings.

One logical stage for pipeline shape + progress; two passes because the outputs are
handled differently.

### D3. Outputs — detection backfills, vuln becomes findings, httpx stays authoritative for web

- **Detection → `service`/`version` backfill (NOT findings).** This explicitly
  corrects ADR 009 D3's ambiguity: a pure service-ID hit must not create a
  `network_vuln` finding.
  - `service` from a static `template-id → service` map (`mssql-detect → mssql`,
    `ssh-detect → ssh`, `postgres-detect → postgresql`, …).
  - **httpx-precedence fix** (nuclei-network runs *before* httpx): an HTTP-family
    detection hit must **not persist** `service` — it is used **only** to route the
    port into httpx, so httpx's richer HTTP fingerprint sets the web service. Only
    **non-HTTP** detection hits backfill `service`/`version`. Net: httpx owns web
    ports, nuclei-network owns non-web ports — no first-writer-wins collision.
  - `version`: set only when currently NULL; normalize empty-string → NULL (never
    persist an empty version).
- **Vuln → `network_vuln` findings** (`source=nuclei-network`). Endpoint `version`
  makes these exploitability-aware (ADR 012) — opportunistically (see D5).

### D4. Depth-gated with CONCRETE per-tier limits (the real OOM bound)

The all-ports passes add nuclei load on top of the web pass we just OOM-fixed at
2Gi. Memory is **not** bounded by runtime alone, so each depth tier resolves
server-side into explicit nuclei limits — not just a severity:

| Tier | detection | vuln templates | limits |
|---|---|---|---|
| **Quick** | off | off | — (today's web-only chain) |
| **Standard** | on (info) | **curated category allowlist** — e.g. `exposures`, `misconfig`, weak-TLS; **excludes** active `default-login`, `enumeration`, `c2`, `honeypot`, `javascript` | per-chunk target cap · `-c` concurrency · bulk-size · rate-limit · timeout/retries · max-output cap |
| **Deep** | on | broader `network/` set **minus** the same active/intrusive categories | higher caps, still bounded |

"Full `network/` set" is explicitly **not** all categories — intrusive/active
templates (default-login, enumeration, c2, honeypot, javascript) stay excluded
unless a future tier deliberately opts in. Memory is bounded by the **target cap +
concurrency + bulk-size + max-output**, validated against the agent memory budget
the same way the 2Gi fix was (no OOM under the all-ports load). Standard's vuln set
is a **curated allowlist**, not "severity ≥ medium" — severity alone is not a risk
model.

### D5. Tool choice — nuclei-network now, nmap deferred (with an honest coverage caveat)

Use nuclei-network for the base: it's already in the stack (Principle #8 — minimal
deps, single-person sustainability), reuses the pinned bundle, and gives useful
service labels + network vuln coverage. **Stated plainly: nuclei-network's
service/version coverage is best-effort and materially thinner than nmap `-sV`** —
good labels for *common* services, *opportunistic* version. So:

- **P1 success** = common services labeled well enough to build collections;
  reliable version-aware exploitability is opportunistic until P3.
- **P3 trigger** = if reliable arbitrary-protocol *version* detection becomes a hard
  product requirement, `nmap -sV` moves into **Deep** (not base). PD's `tlsx` is a
  lighter, in-stack TLS-port complement to consider before nmap.

### D6. Principles unchanged

Runs **on the agent** (customer network); only structured results (service/version
strings + findings) cross the tunnel (Principle #1). Allowlist-gated like every
recon stage (ADR 009 D9). No new runtime tool in the base design.

## Consequences

- **The tech map fills in** — non-web `service`/`version` populate (best-effort per
  D5); service-based collections and version-aware findings become possible.
- **Non-web vuln coverage** (#377) — exposed Redis, weak TLS, etc., from the curated
  vuln sub-pass.
- **Two nuclei passes per chunk** — bounded by D4's explicit caps + category
  allowlist + depth gating; Standard stays close to today, Deep is opt-in. Validate
  the memory budget (no OOM) before shipping each tier.
- **Still zero new dependencies** in the base — nuclei + the existing bundle.
- **httpx gets faster** (narrowed input) — partial offset to the new passes' cost.

## Resolved decisions

- **Why two sub-passes, not one invocation?** Detection templates are ~all `info`;
  a global severity filter that admits vulns would exclude detection (D2).
- **Why don't detection hits create findings?** They're service-ID, not vulns —
  corrects ADR 009 D3. Detection → backfill; vuln → findings only.
- **Why does httpx win for web ports?** nuclei-network runs first, so persisting an
  HTTP-family service would block httpx's richer fingerprint — HTTP-family detection
  hits route to httpx instead of persisting (D3).
- **Why supersede ADR 009?** 009 was Proposed-but-unbuilt and detection-only; 019
  folds in #377 vulns, depth-gating, the tool decision, and fixes the precedence +
  severity issues into one current source of truth.
- **Why nuclei-network over nmap?** Minimal-deps + already-in-stack for the base;
  nmap is the deferred Deep enhancement, with explicit coverage caveats (D5).
- **Why category allowlist, not severity, for the OOM/risk bound?** Severity isn't a
  risk model and doesn't bound memory; explicit category + count + concurrency caps do (D4).

## Scope / phasing

- **P1 — service detection** (ADR 009's core): the **detection sub-pass** +
  service/version backfill (non-HTTP only) + httpx narrowing. Fills the empty
  columns; immediate operator value. No vuln pass, no depth logic yet — but the
  `IncludeNucleiNetwork` directive flag goes in, wireable.
- **P2 — network vulns** (#377): the curated **vuln sub-pass** → `network_vuln`
  findings + the depth-tier gating/limits (D4).
- **P3 — nmap `-sV` Deep enhancement** (deferred): only if P1/P2 version coverage
  proves insufficient against D5's success criteria; gated to Deep, memory-validated.
