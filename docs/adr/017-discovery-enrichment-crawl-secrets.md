# ADR 017: Discovery enrichment — crawl + secret scanning with depth tiers

**Status:** Proposed
**Date:** 2026-06-13
**Related:** [ADR 003](./003-recon-pipeline.md) (recon pipeline — `EnsureTool`,
naabu→httpx→nuclei, scan allowlist, redaction), [ADR 006](./006-asset-first-data-model.md)
(assets + endpoints + findings), [ADR 007](./007-findings-scheduler.md)
(scan_definitions + scheduler), [ADR 013](./013-guided-rollout.md) (guided
rollout), ADR 015 (discovery-target routing — proposed, #385), ADR 016 (agent
pooling + per-chunk lease/runtime budget — proposed, #385).

---

## Context

The discovery pipeline today **narrows**: `naabu` (open ports) → `httpx` (HTTP
responders only) → `nuclei` (CVE/templates against the httpx URLs). Each stage
feeds fewer things to the next. That finds listening services and templated
vulns, but it never looks *past the front page* of a web service and it never
looks for **exposed secrets** (API keys, tokens, credentials embedded in pages,
JS bundles, config files, backups).

Two capabilities would materially raise what a discovery scan finds **inside the
customer network**:

1. **Crawl** — spider each httpx-validated web service to enumerate additional
   URLs / paths / parameters (JS-referenced endpoints, admin panels, API routes,
   `.git`/backup files), so nuclei and the secret stage have real attack surface
   to work on rather than a single root URL.
2. **Secret scanning** — scan crawled content for leaked secrets, surfaced as a
   new finding kind.

Both are governed by **Architectural Principle #1 (data never leaves the customer
network)**: the crawl and the secret scan run **entirely on the agent**; only
structured, redacted findings traverse the tunnel.

A third idea — mining **historical/public** URL sources (gau, waybackurls,
archived snapshots) for *previously exposed and since-removed* secrets — is
deliberately **out of scope here** (see "Deferred: Pipeline B"). It targets a
different asset class (public domains, not the RFC1918 ranges discovery scans
today) and crosses the trust boundary (third-party lookups, external fetches).
This ADR is **internal-first**.

### What already helps (this is not greenfield)

- **Pipeline seams.** `agent/internal/runner/recon/pipeline.go` `Run()` already
  stages naabu→httpx→nuclei behind directive flags (`IncludeHTTPX`,
  `IncludeNuclei`), with a per-stage `Batcher` (chunk-aware) and an `Emit`
  callback. New stages slot between httpx and nuclei.
- **Tool provisioning.** `EnsureTool(name)` (`recon/install.go`) downloads +
  SHA256-verifies a pinned binary into the runtime dir. katana is ProjectDiscovery
  — same family as naabu/httpx/nuclei, so it's a pin entry away (`pdpins.go`).
- **Redaction.** `recon/redact.go` `JSON()` already redacts + truncates nuclei
  evidence before emit. Secret findings reuse and extend this layer.
- **Allowlist vetting.** `vetTargetAgainstAllowlist` (`recon/allowlist.go`) is the
  gate every directive passes; crawl-discovered URLs re-pass it.
- **Runtime budget.** ADR 016's per-chunk lease + runtime budget is the natural
  ceiling for the depth knob below — depth is how a Deep scan stays inside a lease.

## Decision

### D1. One trust boundary, one pipeline (A) in scope

This ADR specifies **Pipeline A** only: crawl + secret scanning that runs wholly
on the agent against allowlisted, in-network targets. Pipeline B (public /
historical enrichment) is named and bounded under "Deferred" but not built until
a public-asset target type exists to hang it on. No conflation: a single scan
never silently mixes on-prem and external-lookup behavior.

### D2. `katana` is a crawl stage between httpx and nuclei

After httpx validates web endpoints, an optional **katana** stage crawls each one
for additional URLs. It is provisioned via `EnsureTool("katana")` + a pin in
`pdpins.go`, run with `-store-response` so response bodies land on disk for the
secret stage. The chain becomes:

```
naabu → httpx ─┬─────────────────────────────► nuclei            (as today)
               └─► katana (crawl, store-resp) ─┬► nuclei  (templated vulns on paths)
                                               └► trufflehog (secrets in content)
```

katana's URL output **widens** the surface; nuclei and trufflehog consume the
widened set. The crawl is bounded by the depth tier (D4).

### D3. `trufflehog` is a secret stage; secrets are a new finding kind

A **trufflehog** stage scans the on-disk response bodies katana stored
(`trufflehog filesystem <dir> --json`). Hits become findings of a new kind
**`exposed_secret`** (default severity **high**), linked to the asset + endpoint
+ the source URL, deduped by `(detector, redacted-hash, endpoint)`. trufflehog is
**not** a ProjectDiscovery tool, so it gets its own pinned, SHA256-verified
download path alongside the PD installer (same runtime-dir pattern).

### D4. Depth tiers are the single control — and a hard runtime ceiling

Crawl multiplies URLs, and we measured that **nuclei time is URL-driven, not
host-driven** (validation: the sparsest /26 chunk took longest because it had the
most live URLs). So depth is not a UX nicety — it is the dial that bounds runtime
and keeps a scan inside its ADR-016 lease. One enum drives sane defaults across
every stage:

| Tier | katana | trufflehog | nuclei | Bounds |
|---|---|---|---|---|
| **Quick** | off | off | current templates | — (today's behavior) |
| **Standard** (default) | depth 2, known-files (robots/sitemap), no JS | off (opt-in) | enriched URL set, med+ | per-host URL cap + crawl-time cap |
| **Deep** | depth 5, JS crawl, store-response | on | enriched set, incl. low/info (opt-in) | higher caps, still lease-bounded |

The tier resolves server-side into concrete flags + caps (`-d`, `-jc`, `-kf`,
`-ct` duration, a total-URL cap, nuclei `-severity`/tags, trufflehog on/off). The
caps are clamped to the remaining per-chunk runtime budget so Deep degrades
gracefully rather than blowing the lease.

### D5. Crawl-discovered URLs are re-gated against the allowlist

katana follows links and **will** wander off-host (CDNs, third-party embeds,
links to other internal hosts). Crawl-scope flags (`-fs`/`-cs`) constrain it, but
defense-in-depth: **every URL katana emits is re-vetted via
`vetTargetAgainstAllowlist` before it reaches nuclei or trufflehog.** Off-allowlist
URLs are dropped (optionally logged as a suggested asset — never scanned). The
allowlist remains the sole authorization surface; the crawler cannot expand it.

### D6. Secret redaction at the agent; verification OFF by default

Two non-negotiables, both from Principle #1:

- **Never ship the raw secret.** An `exposed_secret` finding carries the detector
  type, a **masked** snippet (e.g. first/last 4 chars), the source URL, and a
  hash — never the plaintext. This extends `recon/redact.go`; raw matches stay on
  the agent.
- **No live verification by default.** trufflehog can *verify* a secret by calling
  its provider (AWS/GitHub/…). That is an outbound request that (a) leaks the
  finding off the customer network and (b) takes an unauthorized action with a
  found credential. Default `--no-verification`. Verification is an explicit,
  per-scan opt-in with a clear warning, for customers who want live-validity.

### D7. Asset-model extension: enrich endpoints, add the secret finding

- Crawled URLs **enrich the existing endpoint** (known paths/params as endpoint
  metadata) — we do **not** create an asset per URL (that would explode the asset
  table; an asset is still a host/service).
- `findings.kind` gains `exposed_secret`. No schema reshape beyond the new kind +
  whatever endpoint-path metadata column the enrichment needs.
- Correlation rules (ADR 006) see `exposed_secret` like any other finding kind —
  `notify`/`run_scan_definition` actions work unchanged.

### D8. Tool packaging: katana via PD pins, trufflehog via a parallel pinned path

- **katana**: add to the PD pin set (`pdpins.go`); `EnsureTool("katana")`.
- **trufflehog**: new pinned, SHA256-verified download (it's a separate vendor),
  staged into the same runtime dir. Note the runtime source is still
  `gs://silkstrand-runtimes` (not yet migrated off GCS — tracked separately).
- Airgapped installs use `SILKSTRAND_RUNTIMES_DIR` for both, unchanged.

### D9. Config surface: a `depth` on scan definitions + a directive flag set

- `scan_definitions` gains a **`depth`** column (`quick|standard|deep`, default
  `standard`). The scheduler/handler resolves it into the directive's stage flags
  (`IncludeKatana`, `IncludeTrufflehog`, `Depth`, caps) — the agent stays thin and
  obeys the resolved directive.
- UI: a **depth selector** on the discovery scan config (the scan-definition form
  and the auto-discover option in the Add-Agent modal), with one-line copy on the
  speed/coverage trade and a note that Deep enables secret scanning.

## Consequences

- **More findings, more signal** — admin panels, JS-referenced APIs, exposed
  `.git`/backups, and leaked secrets that the front-page-only chain never saw.
- **Runtime grows with depth** — bounded by D4's caps + the ADR-016 lease;
  Standard stays close to today, Deep is opt-in and clamped.
- **New tool surface** — two more binaries to pin, verify, and keep current; one
  (trufflehog) outside the PD family.
- **Secret-handling liability is real** — D6's redaction + no-verification default
  are load-bearing; a regression there would ship customer secrets over the tunnel.
- **Pure on-prem** — no new external dependency or egress; the trust boundary is
  unchanged from today's discovery.

## Resolved decisions

- **Why not crawl on all ports, not just web?** Crawl is a web concept; non-web
  ports are a different widening axis (nuclei-against-all-ports, the #377 thread)
  that composes with this but is separate.
- **Why endpoint enrichment, not an asset-per-URL?** Keeps the asset model a
  host/service model; URLs are attributes of an endpoint, not assets.
- **Why is secret scanning gated to Deep (opt-in below)?** Storing + scanning
  response bodies is the most expensive stage; tiering it keeps Standard fast and
  makes the cost explicit.
- **Why no verification by default?** It crosses the trust boundary and takes
  unauthorized action with a found credential — opt-in only.

## Scope / phasing

- **P1 — Crawl + depth tiers.** katana stage, depth enum end-to-end (directive →
  agent stage flags/caps → scan_definition column → UI selector), allowlist
  re-gating, endpoint enrichment. No secrets yet. Lands the runtime control first.
- **P2 — Secret scanning.** trufflehog stage, `exposed_secret` finding kind,
  redaction extension, no-verification default + opt-in verification, findings UI.
- **Fast-follow** — correlation-rule presets for `exposed_secret`; per-tier
  template/detector tuning from real-scan data.

## Deferred: Pipeline B (public / historical surface)

Named here so it isn't reinvented, and bounded so it can't leak into Pipeline A:

- **What:** gau / waybackurls to enumerate a **public domain's** historical URLs,
  then fetch the **archived snapshot** (`web.archive.org/web/<ts>/<url>`) and run
  trufflehog/nuclei on the *archived content* — that is how you find a secret that
  was exposed and **since removed** (the live URL is now 404; only the archive
  holds it).
- **Why deferred:** (1) it targets **public assets**, a target class discovery
  doesn't model yet (today's scope is RFC1918); (2) it makes **third-party
  lookups** (web.archive.org, OTX, URLScan) that leak which domains the customer
  cares about — requires explicit opt-in, not a default; (3) it needs an
  archived-content fetch stage that lives firmly on the external side of the
  boundary.
- **Gate for building it:** a `public_domain` (or similar) asset type + an
  explicit "allow external historical lookups" consent on that asset. Until then,
  Pipeline B stays on paper.
