# ADR 015: Discovery target routing & scan policy

**Status:** Proposed (draft — design discussion 2026-06-13, kimi + hero)

**Related:** [ADR 003](./003-recon-pipeline.md) (recon pipeline), [ADR 009](./009-service-detection.md) (service detection via nuclei-network), [ADR 006](./006-asset-first-data-model.md) (asset model), issue #377 (service-ID for non-web ports).

## Context

The discovery pipeline is `naabu → httpx → nuclei`, where **nuclei runs only against httpx's confirmed HTTP(S) URLs**. Concretely (observed on a live `/26`): naabu found ~25 open ports → httpx confirmed **16** as HTTP → nuclei scanned **16 URLs**. The other ~9 open ports (SSH `22`, Postgres `5432`, etc.) became `asset_endpoints` with a **bare port number — no service, no version, no vuln coverage**.

The recurring question is *"should nuclei scan all listening ports?"* — the intuition being that we'd get technology mappings and vulnerability data across every service, not just web. [ADR 009](./009-service-detection.md) already proposes a `nuclei-network` **service-detection stage** over all ports. This ADR sits **on top of 009**: it defines *how typed targets are routed to probing engines, and how vuln probing is gated by policy* — so that "all ports get enrichment" does **not** become "all ports get the same vuln scanner."

## Problem

"Run nuclei against all ports, yes/no?" is the wrong framing. It conflates two different needs and ignores intrusiveness:

1. **Inventory quality** (what is this service?) — first-order; wanted for *every* open port.
2. **Vulnerability probing** (is this service vulnerable?) — a second-stage, *policy* decision keyed on the identified protocol.
3. **Blast radius** — ~90%+ of nuclei templates are HTTP. Firing the HTTP catalog at a Postgres/SSH socket is thousands of failed requests for ≈0 yield, plus IDS noise and fragile-service risk. And nuclei's `network`/`ssl` templates are **active probes**, not benign fingerprinting — they can't be lumped with "tech mapping."

## Decision

### D1. Inventory first; vuln probing is a second, policy-gated stage
Every open port gets **service enrichment**. Vulnerability probing is a **separate decision** taken *after* the protocol is identified, gated by policy + confidence. A service fingerprint must **not** depend on a vuln scanner producing a finding.

### D2. A typed target router (not "one list, one scanner")
Model the pipeline as a router that produces **typed targets**, each carrying a per-protocol template/probe allowlist:
```
naabu          → open endpoints (ip:port)
service-ID     → protocol + confidence per endpoint        (ADR 009 nuclei-network / lightweight probes)
httpx          → enrich HTTP(S) endpoints                  (existing)
nuclei (router)→ typed targets + per-protocol allowlist:
                   http   → full HTTP catalog (httpx URLs, as today)
                   tls    → ssl templates,  only for TLS-speaking endpoints
                   ssh    → ssh templates,  only after SSH confidence
                   db/... → service templates, only when policy allows
```
The broad HTTP catalog is **never** run against raw sockets.

### D3. Tiered enrichment (cheap by default, deep on demand)
Enrichment runs in tiers; default stays light to respect customer LANs:
- **T0** — port + lightweight banner / TLS metadata where safe (always).
- **T1** — protocol-specific probes with bounded concurrency + timeouts (default).
- **T2** — deep version detection only when explicitly enabled or asset-criticality warrants.

> `nmap -sV` is directionally right for T2 but **not** a safe default in customer networks (heavier, more signature-noisy, surprising timing/retry). Prefer bounded protocol probes / nuclei-network for T0–T1; reserve `nmap -sV`-class detection for T2-on-demand.

### D4. Classify probes by intrusiveness, not by tag
A nuclei tag (`network`, `ssl`) is **not** a sufficient policy boundary. Each template/probe is classified by **protocol + intrusiveness + request count + auth/unsafe flag**. The router selects probes by `(protocol, allowed-intrusiveness)`, so low-risk detection runs broadly while intrusive probes require explicit policy.

### D5. Confidence + provenance per endpoint
Track *how* a service was identified, building explainable inventory:
`port-heuristic → banner → TLS cert → protocol handshake → version probe → vuln match`. This avoids both failure modes: `tcp/22 open` forever (no enrichment) and an SSH listener drowned in failed HTTP attempts (wrong probe).

### D6. Scan policy knobs (per-scan / per-tenant)
First-class controls, set early rather than retrofitted:
- `max_requests_per_endpoint`, `max_time_per_endpoint`
- `allowed_intrusiveness` (e.g. `passive | safe | active | aggressive`)
- `protocol_allow` / `protocol_deny`
- `inventory_only` mode (enrich, never vuln-probe)

### D7. Store tech-mapping independent of vuln findings
`asset_endpoints.service` + `version` (ADR 009 D3) + a `confidence`/`provenance` field hold the inventory signal **independently** of `findings`. Re-running with `inventory_only` still produces a full service map.

## Relationship to ADR 009
ADR 009 provides the **service-detection stage** (nuclei-network over all ports, backfilling `service`/`version`). ADR 015 provides the **routing + policy + tiering + provenance** layer on top, and constrains how non-web *vuln* templates are selected. 009 D2 ("template selection") is subsumed by 015 D4's intrusiveness model.

## Consequences
- nuclei stays useful for non-web — as a **targeted, typed probe engine**, never the broad HTTP catalog against raw sockets.
- Discovery produces *explainable* inventory (provenance/confidence) usable by `suggest_target` / `auto_create_target` rules (e.g. "`5432` → PostgreSQL 16 → suggest a CIS-postgres compliance scan").
- More moving parts: a probe-classification catalog, policy plumbing, and per-protocol budgets. Scan time/resources grow with intrusiveness; the policy knobs (D6) are the throttle.

## Open questions
1. Engine for non-web service-ID: nuclei-network (ADR 009) vs a dedicated prober vs both per tier.
2. Default `allowed_intrusiveness` for a new tenant/scan (lean conservative).
3. Where the probe-classification catalog lives (bundle vs server config vs template metadata).

## Scope / phasing
- **P1** — service-ID over all ports + inventory storage with confidence/provenance (closes #377). Inventory-only is the safe default.
- **P2** — typed vuln routing (D2/D4) + policy knobs (D6): TLS/SSH/db templates selected by service identity + intrusiveness.
- **P3** — T2 deep version detection on-demand; adaptive per-protocol budgets.
