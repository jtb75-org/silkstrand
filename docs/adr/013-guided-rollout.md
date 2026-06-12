# ADR 013: Guided agent rollout — Deploy → Discover → Prove

**Status:** Accepted (PRs #353–#357 merged 2026-06-12)
**Date:** 2026-06-12
**Related:** [ADR 003](./003-recon-pipeline.md) (recon pipeline — `EnsureTool`,
scan allowlist D11), [ADR 006](./006-asset-first-data-model.md) (assets +
collections), [ADR 007](./007-findings-scheduler.md) (scan_definitions +
scheduler), [ADR 012](./012-exploit-validation.md) (exploit validation — the
value the rollout should sprint toward).

---

## Context

Onboarding a scanner today is **imperative and object-fragmented**. To get one
host scanned, the operator hand-assembles primitives across four surfaces:

1. **Agents** → Generate install command.
2. **a host shell** → run the install (sudo).
3. **the host shell again** → hand-edit `/etc/silkstrand/scan-allowlist.yaml`.
4. **Scans → Definitions** → author a scan_definition (name, `kind`,
   `scope_kind ∈ {asset_endpoint, collection, cidr}`, the matching id/cidr,
   `agent_id`, `schedule`, `enabled` — a conditional 8-field form).
5. **Execute.**
6. (agent changed → **re-point the definition's `agent_id`**.)
7. **Assets / Findings** to see anything.

That is ~7 steps over 4 places, and navigating it requires already understanding
**agent, allowlist, target, collection, scan_definition, scan-run, finding**.
The click count is a symptom; the disease is **too many concepts on the
critical path**. Two specifics are the worst offenders:

- **The allowlist is an off-product artifact.** The single most important
  safety boundary is an SSH-and-editor edit of a YAML file that isn't in the UI
  at all (ADR 003 D11 — customer-controlled, on the host).
- **Scope is specified twice.** The agent already reports its allowlist
  snapshot to the server (`agent_allowlists`, migration 018), yet discovery
  makes you re-author a `scope=cidr` definition covering the same ranges.

[ADR 012](./012-exploit-validation.md) raises the stakes: the product's headline
moves from "a noisy list of maybe-vulns" to **"a reproducible proof this is
exploitable."** That proof is the activation moment — and today it sits at the
bottom of the funnel above. Onboarding should **sprint to the first
exploit-proof**, not bury it.

## Problem

Collapse the happy path to three steps — **Deploy → Discover → Prove** — by
seeding configuration from the install panel and defaulting discovery scope to
the agent's allowlist, **without** weakening the fail-closed, customer-owns-the-
boundary guarantees of ADR 003, and **without** silently double-scanning shared
ranges.

## Decisions

### D1. The happy path is Deploy → Discover → Prove

The primary flow is three steps; the existing primitives become **optional
power-user surfaces**, not the required path.

1. **Deploy** — one install command that carries the agent's scope (and proxy);
   the agent self-registers and seeds its own allowlist.
2. **Discover** — discovery's default scope *is* the agent's allowlist; it runs
   automatically on connect (and on a default cadence).
3. **Prove** — findings stream in; opted-in ranges auto-validate (ADR 012 T2)
   and surface as "Exploitable — here's the repro."

`scan_definitions`, `collections`, and `targets` remain for custom scope, cron,
and per-endpoint control — but the operator never *has* to touch them to get
value.

### D2. Seed the allowlist from the install panel

The "Install a new agent" panel gains an **Allowed targets** input (repeatable:
CIDR / IP / range / hostname). Each entry appends `--allow-cidr=<x>` to the
generated curl string. `install-agent.sh` already supports `--allow-cidr`
(repeatable) and renders `/etc/silkstrand/scan-allowlist.yaml` from it, so this
is largely a frontend change.

Invariants preserved:

- **The host file remains the source of truth.** The UI *seeds* it; the
  customer can edit it later on the host (agent hot-reloads on mtime) or via the
  existing per-agent Allowlist viewer. SilkStrand never raises a boundary
  remotely.
- **Fail-closed.** An empty allowed-targets list yields an `allow: []`
  scaffold (the ADR-003 D11 default) — the agent connects but scans nothing
  until scope is set. The panel should require at least one entry to enable
  "Run discovery on connect" (D5).

This eliminates touchpoint #3 (the off-product SSH edit) entirely.

### D3. Proxy + custom CA (advanced)

Segmented/enterprise networks reach `api.silkstrand.io` through an egress proxy,
and **TLS-inspecting** proxies re-sign the connection with a corporate root CA —
the *real* enterprise blocker. Today **neither `install-agent.sh` nor the agent
supports a proxy or a custom CA.** Net-new capability, shipped as its **own**
feature (PR 5) so it does not gate D2/D5:

**Proxy.** `install-agent.sh --proxy=URL [--no-proxy=LIST]` → passed to `curl`
for the install fetch **and** persisted into `agent.env`; the agent honors
`HTTPS_PROXY`/`NO_PROXY` via `http.ProxyFromEnvironment`. **No proxy credentials
in the generated command** (it lands in shell history + the UI); authenticated
proxies use the host's existing proxy env or a host-side credentials file.

**Custom CA.** `--ca-cert=FILE` / `SILKSTRAND_CA_CERT_PATH` (path only, never
inlined). The primary path is the **host OS trust store** (the customer adds the
corporate CA via `update-ca-certificates`; Go's system pool then works for
free). The escape hatch, for hosts that can't modify the store: the agent loads
`x509.SystemCertPool()`, **appends** the PEM(s), and uses that as `RootCAs` —
note `SSL_CERT_FILE` *replaces* rather than augments the pool, so it is not a
clean answer. Fail loudly if no certs append; never `InsecureSkipVerify`; log
the CA path/fingerprint, never the contents.

**Centralize the agent's TLS (implementation requirement).** The agent today
builds plain `http.Client`s in ~6 places (bootstrap, cache, `runner/recon/install`,
`runner/collector`, updater, uninstall) and dials WSS via
`websocket.DefaultDialer`. PR 5 adds **one internal transport helper** returning
an `*http.Client`/`*http.Transport` (`RootCAs` set when configured +
`ProxyFromEnvironment`) and a `websocket.Dialer` with the same `TLSClientConfig`/
proxy — used by WSS, bootstrap, every download path, the updater, and
self-delete, so they all trust the same CA and honor the same proxy.

### D4. `agent_allowlist` scan scope

Add a fourth `scope_kind`:

```
ScanDefinitionScopeAgentAllowlist = "agent_allowlist"
```

A definition with this scope means *"scan whatever this agent is currently
allowed to scan."* Resolved **at dispatch time** from the agent's most recent
`agent_allowlists` snapshot — so editing the host allowlist automatically
adjusts scope with no UI round-trip. This removes the redundant `scope=cidr`
re-entry (the "scope specified twice" problem) and gives a durable standing
definition: "discover everything I'm allowed to, on a cadence."

Dispatch behavior (fail-safe):

- Requires `agent_id`; no `cidr`/`collection_id`/`asset_endpoint_id`.
- On dispatch, load the agent's latest snapshot. If it is **missing, empty, or
  unparsable, do not silently dispatch a broad scan** — mark the run
  blocked/failed with an actionable message ("agent hasn't reported an allowlist
  yet").
- Dispatch each `allow` entry **as-is** (CIDR/IP/range/hostname). Do **not**
  subtract `deny` server-side in v1 — the agent enforces `deny` locally.
- Stamp the snapshot hash + `reported_at` on the run for transparency. A
  staleness *warning* (snapshot older than the live connection) is allowed; do
  not block solely on staleness.

The agent re-vets every target against its local allowlist before scanning
(ADR 003 D11), so `agent_allowlist` scope can never exceed the host file even if
the snapshot is stale — the worst case is a directive the agent narrows or
rejects, never a policy bypass.

### D5. Auto-discover on connect

The install panel gains a checkbox **"Run discovery as soon as it connects"**
with a recurring selector **(Off / Daily / Weekly, default Off)**. Discovery
**on connect runs always** (that is what the checkbox promises); a *recurring*
schedule is opt-in, because silently installing a standing scan of someone's
network on a cadence they did not choose is a trust/cost surprise. Because the
install token is minted *before* the agent exists, this is **not** a curl flag —
it is server-side intent on the token:

- `install_tokens` gains `auto_discover BOOLEAN` (+ `discover_cron TEXT NULL`
  for the Daily/Weekly choice).
- On bootstrap **and the agent's first allowlist snapshot** (not merely on WSS
  connect — the agent reports a snapshot immediately on startup, so waiting for
  it avoids racing a missing snapshot, per D4), the server creates a discovery
  `scan_definition` with `scope_kind = agent_allowlist` (D4) for the new agent
  and executes it once (and installs the cron if requested).

This collapses touchpoints #4–#6 (author definition → execute → re-point agent)
into one checkbox. The new definition is a normal, editable object — the
operator can later retune or delete it.

### D6. Overlap preview + confirmation (never a hard block)

Seeding scope from the panel makes it easy to point two agents at the same
ranges and **double-scan** — wasted load on the *customer's* targets (IDS noise,
rate pressure), muddied attribution, redundant findings.

**Critical nuance: CIDR overlap is a heuristic for duplication, not proof of
it.** RFC1918 space is reused across sites constantly — `192.168.1.0/24` behind
two branch-office agents are *different machines*. A hard block would break the
common multi-site case. Therefore overlap is surfaced as a **confirmation,
never a block.**

On **Generate**, the panel calls:

```
POST /api/v1/agents/allowlist-preview   { "cidrs": ["10.0.0.0/24", ...] }
→ {
    "overlaps": [
      { "cidr": "10.0.0.0/24",
        "conflicts_with": { "kind": "agent", "name": "dc-west", "id": "...",
                            "range": "10.0.0.0/16", "discovery_enabled": true } }
    ],
    "redundant": ["10.0.0.0/24 is contained in 10.0.0.0/16 (also entered)"]
  }
```

Checked against: **(a)** other agents' stored allowlists *that actually
discover* (auto-discover on, or an active discovery definition — an agent that
never scans its allowlist is a false alarm), and **(b)** existing
`cidr`/`agent_allowlist` discovery definitions. Compliance definitions are
skipped (they target endpoints, not ranges). Intra-input redundancy
(a typed `/24` inside a typed `/16`) is also reported.

**Zone-aware suppression (D10).** The zone tag disambiguates RFC1918 reuse, but
conservatively:

- Suppress a warning **only** when *both* sides have non-empty, normalized zones
  **and** they differ **and** the overlapping range is **private** address space
  (`10/8`, `172.16/12`, `192.168/16`, IPv6 ULA `fc00::/7`).
- **Public** CIDR overlap **always warns**, even across zones — public reuse is
  far more likely a real duplicate or misconfiguration than legitimate reuse.
- If **either** zone is unset, **warn** (unset stays conservative).
- This is **warning/UI logic, not an authorization boundary** (D9). A mislabeled
  zone that hides a real private-space duplicate is an accepted *operator
  labeling* risk, never a security control.

The server already parses CIDRs into `net.IPNet` (`api/internal/allowlist`); only
the interval-intersection + private/public classification is new (~tens of
lines). The modal frames the ambiguity rather than deciding for the operator:

> ⚠️ `10.0.0.0/24` overlaps with agent **dc-west** (allowlist `10.0.0.0/16`,
> auto-discovery on, zone `office-east`). If these agents see the **same
> network**, you'll scan these hosts twice. — [Adjust ranges] [Proceed anyway]

The same endpoint is reused on manual scan_definition creation.

### D7. The install panel shape

```
Install a new agent
  Name             [ acme-scanner        ]   (defaults to hostname)
  Zone / site      [ office-east         ]   (optional; disambiguates reused private ranges — D10)
  Allowed targets  [ 192.168.0.0/24   ] [+]  → --allow-cidr=…
                   [ 10.0.0.0/24      ] [x]
                   "The agent only ever scans these. Edit later on the host or here."
  ☑ Run discovery as soon as it connects        → auto_discover on the token
       └ Recurring: ( • Off ) ( Daily ) ( Weekly ) → discover_cron
  ▸ Advanced   (HTTPS proxy · custom CA · rate limit · run as service · docker mode)
  [ Generate install command ]
       → overlap-preview (D6, zone-aware) → [confirm modal if conflicts] →
         mint token (zone, auto_discover, discover_cron) + render:
         curl … --allow-cidr=192.168.0.0/24 --allow-cidr=10.0.0.0/24 --as-service
```

Zone, auto-discover, and the cron are **server-side token metadata** — they do
not appear as curl flags (the agent learns its zone from the server at bootstrap;
it does not need it locally).

### D8. Power-user surfaces stay

Nothing is removed. `Scans → Definitions` (custom scope/cron), `Collections`
(saved predicates), and per-endpoint `Targets` remain for operators who need
them. They simply leave the required happy path. The auto-created discovery
definition (D5) is a normal object they can inspect and edit.

### D9. Authority + fail-closed (unchanged)

The host `scan-allowlist.yaml` remains the ultimate authority (ADR 003 D11). The
panel only *seeds* it; the agent re-vets every target locally before scanning,
including for `agent_allowlist` scope (D4). Validation tiering (ADR 012) is
out of scope here and continues to layer on top.

### D10. Agent zones

A zone/site label disambiguates reused private address space for the overlap
check (D6) — `192.168.1.0/24` in `office-east` vs `office-west` are different
machines. It is **server-side deployment metadata**, not agent-side config:

- `install_tokens.zone TEXT NULL` and `agents.zone TEXT NULL`.
- `CreateInstallToken` accepts an optional zone; **normalized** to a slug
  (trim, lowercase, bounded length, rejected if empty-after-trim). Keep the
  original label only if presentation needs it.
- Token consumption returns the token record (not just `tenant_id`) so
  `bootstrap` copies `zone` onto the new agent.
- Surface `zone` in `ListAgents`/`GetAgent` so the UI can show and filter it.

The agent does not need to know its zone (it may appear in local logs if useful,
but it is not required for any agent-side decision). Zone is **labeling for the
overlap heuristic only**, never an authorization input (D6, D9).

## Consequences

**Positive:**

- The happy path goes from ~7 steps across 4 surfaces to **fill 2 fields + tick
  1 box → paste one command → assets/findings appear.**
- Eliminates the off-product SSH allowlist edit — the worst touchpoint.
- Removes "scope specified twice"; the allowlist *is* the discovery scope.
- Double-scanning becomes a deliberate, informed choice rather than an accident.
- Reuses existing rails: `--allow-cidr` rendering, `agent_allowlists` snapshots,
  `install_tokens`, scheduler dispatch, `net.IPNet` parsing.

**Negative:**

- New `scope_kind` + a token column + a create-on-connect hook are real server
  changes (contained, but they touch the scheduler dispatch path).
- The overlap check is a heuristic; it will both miss real duplicates (different
  agents, genuinely same wire, ranges that don't textually overlap) and warn on
  false ones (reused private space). It is decision support, not a guarantee.
- Proxy + custom-CA support is net-new agent capability with its own test
  surface (and motivates centralizing the agent's TLS/transport, D3).
- The zone tag is operator-labeled; a wrong label can hide a real private-space
  duplicate warning — accepted as a labeling risk, not a security control (D6).

**Scope boundary:**

- Validation/exploit tiering (ADR 012) is explicitly *not* part of this ADR.
- Compliance scan setup (bundle + credential binding) is unchanged.
- Cross-agent scope deduplication beyond the config-time warning (D6) is
  deferred (see open questions).

## Implementation (PR split)

1. **PR 1 — Allowed-targets field** (D2): frontend only; repeatable input →
   `--allow-cidr` (shell-quoted values). **Shipped** (#353).
2. **PR 2 — `agent_allowlist` scope** (D4): model constant; scheduler dispatch
   resolves the agent's current snapshot to targets; fail-safe on
   missing/empty/unparsable snapshot; stamp snapshot hash/`reported_at`; agent
   re-vet unchanged.
3. **PR 3 — Auto-discover on connect** (D5): `install_tokens.auto_discover` +
   `discover_cron`; create-on-first-snapshot hook; panel checkbox + Off/Daily/
   Weekly selector.
4. **PR 4 — Overlap preview + modal + zones** (D6, D10): `agents.zone` +
   `install_tokens.zone` + normalization + `ListAgents`/`GetAgent` exposure +
   panel zone field; `allowlist-preview` endpoint with zone-aware, private-only
   suppression; confirmation modal; reuse on definition create.
5. **PR 5 — Proxy + custom CA** (D3): `install-agent.sh --proxy/--no-proxy/
   --ca-cert`; a centralized agent TLS/transport helper (`RootCAs` +
   `ProxyFromEnvironment`) used by WSS + every download/bootstrap/updater path;
   panel advanced fields. Independent of PRs 2–4.

PRs 2–4 are the funnel collapse; PR 5 is independent.

## Open questions

- **Q2 (deferred to v2). Dispatch-time coalescing.** A more robust layer than
  the config-time modal: the scheduler notices two due discovery scans cover
  overlapping *live assets* (which already dedupe by IP) and skips/merges.
  Catches overlaps that arise after install, on real assets rather than paper
  CIDRs. More complex; explicitly out of this iteration.
- **Multi-use / fleet tokens.** Tokens stay single-use (one token → one agent →
  one auto-discovery) for this iteration (resolved). "Paste once, deploy to N
  hosts" is a separate fleet-rollout capability with its own design — when built,
  `auto_discover` should fan out one discovery per agent's own allowlist.

### Resolved (folded into decisions)

- **Q1 → D10.** Add the zone tag **now** (not deferred); used by D6 with
  **private-only** cross-zone suppression and conservative unset behavior.
- **Q3 → single-use tokens** (above), one agent per token.
- **Q4 → no proxy auth in the generated command** (D3); custom-CA support added
  for TLS-inspecting proxies (host trust store primary, `--ca-cert` append-to-
  pool escape hatch).
- **Q5 → on-connect always; recurring opt-in Off/Daily/Weekly, default Off**
  (D5).
