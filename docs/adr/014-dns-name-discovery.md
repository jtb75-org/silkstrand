# ADR 014: DNS-name (virtual-host) discovery

**Status:** Proposed
**Date:** 2026-06-12
**Related:** [ADR 003](./003-recon-pipeline.md) (recon pipeline — naabu→httpx→nuclei,
scan allowlist D11 — resolution/policy stays in the customer network),
[ADR 006](./006-asset-first-data-model.md) (asset model — the IP-keyed identity
this ADR revisits), [ADR 007](./007-findings-scheduler.md) (scan_definitions +
scope_kind), [ADR 009](./009-service-detection.md) (per-service detail),
[ADR 013](./013-guided-rollout.md) (allowlist seeding — the import plumbing to
reuse).

---

## Context

Customers run **many websites behind a shared ingress / reverse proxy** — nginx,
traefik, a k8s ingress controller, a cloud ALB. The defining property: *N* DNS
names (`app.example.com`, `api.example.com`, `status.example.com`, …) resolve to
the **same one or two IPs** (the ingress), and the proxy routes to the right
backend on the **HTTP `Host` header / TLS SNI**, not on IP or port.

SilkStrand's discovery cannot see that surface today, for two **structural**
reasons (both confirmed in the current code, not incidental):

1. **Assets dedup on IP.** Assets carry the unique partial index
   `idx_assets_tenant_ip` on `(tenant_id, primary_ip)`; `UpsertAsset` conflicts
   on that key and treats `hostname` as a `COALESCE`'d *attribute*. So twenty
   vhosts behind one ingress IP **collapse into a single asset** — the last
   hostname seen wins. The model has no way to represent them as distinct things.

2. **Discovery is IP-driven.** The recon pipeline is naabu (`ip:port`) → httpx
   (probes the same `ip:port`) → nuclei. httpx therefore hits the ingress's
   **default backend** with no per-name `Host`/SNI; every named site behind the
   proxy returns the same default response (or a 404/421). The pipeline has no
   input through which the *names* enter.

The consequence is a class of surface that **a CIDR sweep structurally cannot
reach**: you can scan `10.0.0.5/32` all day and never learn that twenty
business-critical sites live behind it. And unlike open ports, vhosts are **not
network-enumerable** — there is no scan that returns "the list of `Host` values
this proxy will route." You have to *know the names*.

Hence the request: **let the operator upload a list of DNS names** and have
SilkStrand discover, model, and scan each as its own thing.

### What already helps (this is not greenfield)

- The scan allowlist already accepts **hostnames** (`host.example.com`,
  `*.example.com`) alongside CIDR/IP/range, and ADR 013 PR 1 already seeds
  hostname entries into it from the install panel. So the **authorization
  plumbing exists** — names can route through the customer-controlled,
  fail-closed allowlist without inventing a new trust path.
- **httpx is natively SNI/`Host`-aware**; per-vhost probing is a matter of
  feeding it names + flags instead of `ip:port`.
- The asset model already has a `hostname` column and a `resource_type`
  discriminator — it is *shaped* for a hostname-keyed asset; it just doesn't key
  on one yet.
- **Resolution belongs agent-side** (ADR 003 D11): the agent resolves the name
  in the customer network and probes with SNI; the server never does DNS. This
  ADR keeps that invariant.

## Decision

### D1. A virtual host is a first-class asset, keyed on the name

Introduce a web/service asset identity keyed on **`(tenant_id, hostname, port)`**
rather than collapsing to IP. Concretely: a new `resource_type` (proposed
`http_service`) with its own **partial unique index** on
`(tenant_id, lower(hostname), port) WHERE hostname IS NOT NULL`, parallel to the
existing IP index. The resolved IP(s) are recorded as **attributes**
(fingerprint / a join), not as the identity — so twenty names on one ingress IP
are twenty assets that *share* a `primary_ip`, and the topology view can render
the fan-out ("these 20 sites all sit on 10.0.0.5"). This is the load-bearing
decision: without it, every later step re-collapses to one IP-keyed row.

### D2. A DNS-name import surface

A bulk **import**: paste / CSV / file upload of DNS names →
normalize (trim, lowercase, strip scheme/path, validate) →
**(a)** generate **allowlist hostname entries** (reusing the ADR 013 seed
plumbing — the host file stays authoritative; the import *proposes*, the
customer's agent *enforces*), and **(b)** create/seed the web assets (D1) and a
scope to scan them (D4). The server stores the imported list as tenant data; it
is **not** an authorization input — the agent allowlist remains the boundary.

### D3. Vhost-aware probing in the agent

The recon runner gains a **name-driven path**: feed **hostnames** (not
`ip:port`) to httpx with SNI + `Host` set per name, capture the **per-vhost**
response (status, title, cert/SAN, security headers, redirects). naabu still
does port discovery against the *resolved* IP, but the asset that results is
keyed on the **name** (D1). DNS resolution happens **in the agent** (D11); the
agent reports `{hostname, resolved_ip, port, …}` upward.

### D4. A hostname / `dns_list` discovery scope

`scan_definitions` can target the imported names — either a new
`scope_kind = dns_list` (resolved at dispatch like `agent_allowlist` in ADR 013
D4) or a **Collection** whose predicate selects the `http_service` assets. Reuse
the ADR 007 scheduler; no new dispatch machinery.

### D5. Resolution & shared-IP semantics

The agent resolves each name; **many names → many assets**, deduped only on
identical `(name, port)`. Assets that resolve to the same IP are **cross-linked**
by that IP (for topology and for "what else is on this host"), but never merged.
A name that fails to resolve is recorded as an asset in an `unresolved` state
rather than dropped (it is still real attack surface / a dangling-DNS signal).

### D6. Findings are per-vhost

The point of the whole exercise: TLS/cert posture (expiry, SAN mismatch, weak
config), security headers, redirect chains, and nuclei templates are evaluated
**per name**, because they genuinely differ across vhosts sharing an IP. A
wildcard-cert misconfig or a missing HSTS on *one* of twenty sites is exactly
what IP-first scanning misses today.

### D7. Authorization is unchanged

Everything still flows through the agent's `/etc/silkstrand/scan-allowlist.yaml`
(ADR 003 D11). The import generates **proposed** allowlist entries; the host
file remains the final, customer-owned authority. No name is ever scanned
because it was uploaded — only because the allowlist authorizes it. This keeps
the "data never leaves / customer owns scope" posture intact.

## Consequences

- **Closes the ingress/reverse-proxy/k8s surface** — the most common shape for
  "we have lots of internal web apps" — which CIDR discovery cannot reach.
- **Touches the core asset model** (the D1 keying change). This is the
  deliberate, review-worthy part: it adds a second identity axis (name) beside
  the existing IP axis. Existing IP-keyed discovery is unaffected.
- **Complementary, not a replacement** — CIDR discovery still finds the hosts and
  open ports; DNS import finds the *sites*. The two cross-link by IP.
- **New input to harden** — the import must validate/normalize names, bound list
  size, and never trigger server-side DNS resolution or SSRF-style fetches
  (resolution + probing stay agent-side).
- **Nice topology payoff** — shared-IP fan-out becomes visible, and dangling DNS
  (name resolves, nothing answers) becomes a first-class finding.

## Open questions

1. **New `resource_type` vs. relax the IP-only unique index?** A dedicated
   `http_service` type is cleaner and keeps host assets untouched; relaxing the
   index is less code but blurs "host" and "site." (Leaning new type.)
2. **Port in the identity?** Is `app.example.com:443` distinct from `:8443`, or
   is the site the name and ports are endpoints under it (mirroring
   `asset_endpoints`)? (Leaning: name = asset, ports = endpoints — consistent
   with ADR 006.)
3. **Wildcards** — does an uploaded `*.example.com` expand (needs a name source)
   or stay a single allowlist pattern with no asset until a concrete name is
   observed? (Leaning: pattern-only; concrete names come from explicit import or
   observed SNI/SAN.)
4. **Auto-seeding** — should IP scans that observe a **cert SAN** or an SNI
   redirect *suggest* names back into the import (log-only, à la ADR 006
   `suggest_target`)? High-value, but a later phase.
5. **Cloud-DNS / CT-log import** — pulling a Route53 / Cloudflare zone or
   certificate-transparency logs to populate the list automatically. Clearly
   valuable, clearly out of v1 scope.

## Scope / phasing

- **v1 (lean):** import DNS names → proposed allowlist hostname entries
  (ADR 013 plumbing) → hostname-keyed `http_service` assets (D1) → vhost-aware
  httpx with SNI/`Host` (D3) → per-vhost findings (D6). Scope via a Collection or
  `dns_list` (D4).
- **Later:** cloud-DNS zone pull, wildcard expansion, CT-log seeding, and
  cert/SNI-observed name auto-suggestion (open questions 3–5).
