# ADR 013: Guided agent rollout — Deploy → Discover → Prove

**Status:** Proposed
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

### D3. Proxy fields (advanced)

Segmented/enterprise networks reach `api.silkstrand.io` through an egress proxy.
Today **neither `install-agent.sh` nor the agent supports a proxy** — this is
net-new capability, in two parts:

- `install-agent.sh --proxy=URL [--no-proxy=LIST]` → passed to `curl` for the
  install fetch **and** persisted into `agent.env`.
- The agent honors `HTTPS_PROXY` / `NO_PROXY` for its WSS dial *and* runtime
  tool/template downloads (Go's `net/http` + websocket dialer can use proxy env,
  but it must be wired and tested).

The panel exposes a "Proxy (advanced)" field that appends `--proxy=`, but the
underlying support ships as its **own** small feature so it does not gate D2/D5.

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

The agent re-vets every target against its local allowlist before scanning
(ADR 003 D11), so `agent_allowlist` scope can never exceed the host file even if
the snapshot is stale.

### D5. Auto-discover on connect

The install panel gains a checkbox **"Run discovery as soon as it connects"**
(with an optional "then weekly"). Because the install token is minted *before*
the agent exists, this is **not** a curl flag — it is server-side intent on the
token:

- `install_tokens` gains `auto_discover BOOLEAN` (+ optional `discover_cron`).
- On bootstrap + first allowlist snapshot, the server creates a discovery
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

The server already parses CIDRs into `net.IPNet` (`api/internal/allowlist`); only
the interval-intersection math is new (~tens of lines). The modal frames the
ambiguity rather than deciding for the operator:

> ⚠️ `10.0.0.0/24` overlaps with agent **dc-west** (allowlist `10.0.0.0/16`,
> auto-discovery on). If both agents are on the **same network**, you'll scan
> these hosts twice. If they're **separate sites/segments** (common with private
> ranges), this is fine. — [Adjust ranges] [Proceed anyway]

The same endpoint is reused on manual scan_definition creation.

### D7. The install panel shape

```
Install a new agent
  Name             [ acme-scanner        ]   (defaults to hostname)
  Allowed targets  [ 192.168.0.0/24   ] [+]  → --allow-cidr=…
                   [ 10.0.0.0/24      ] [x]
                   "The agent only ever scans these. Edit later on the host or here."
  ☑ Run discovery as soon as it connects        → auto_discover on the token
       └ ☐ then weekly                           → discover_cron
  ▸ Advanced   (HTTPS proxy · rate limit · run as service · docker mode)
  [ Generate install command ]
       → overlap-preview (D6) → [confirm modal if conflicts] →
         mint token (auto_discover) + render:
         curl … --allow-cidr=192.168.0.0/24 --allow-cidr=10.0.0.0/24 --as-service
```

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
- Proxy support is net-new agent capability with its own test surface.

**Scope boundary:**

- Validation/exploit tiering (ADR 012) is explicitly *not* part of this ADR.
- Compliance scan setup (bundle + credential binding) is unchanged.
- Cross-agent scope deduplication beyond the config-time warning (D6) is
  deferred (see open questions).

## Implementation (PR split)

1. **PR 1 — Allowed-targets field** (D2): frontend only; repeatable input →
   `--allow-cidr`; require ≥1 entry to enable auto-discover. Ships immediately.
2. **PR 2 — `agent_allowlist` scope** (D4): model constant, scheduler dispatch
   resolves the agent's current snapshot to targets, agent re-vet unchanged.
3. **PR 3 — Auto-discover on connect** (D5): `install_tokens.auto_discover`
   (+ `discover_cron`); create-on-first-snapshot hook; panel checkbox.
4. **PR 4 — Overlap preview + modal** (D6): `allowlist-preview` endpoint +
   interval-intersection util; confirmation modal; reuse on definition create.
5. **PR 5 — Proxy support** (D3): `install-agent.sh --proxy/--no-proxy`; agent
   honors `HTTPS_PROXY`/`NO_PROXY` for WSS + downloads; panel advanced field.

PRs 1–4 are the funnel collapse; PR 5 is independent.

## Open questions

- **Q1. Network/site/zone tagging.** The precise fix for RFC1918 reuse (D6) is a
  zone tag on agents: same-zone overlap → strong warning; different-zone →
  suppressed. Small data-model addition, large precision win. Defer to a
  follow-on, but design D6's response shape to carry a future `zone` field.
- **Q2. Dispatch-time coalescing.** A more robust layer than the config-time
  modal: the scheduler notices two due discovery scans cover overlapping *live
  assets* (which already dedupe by IP) and skips/merges. Catches overlaps that
  arise after install. More complex; a v2.
- **Q3. One token, many hosts.** An install command pasted on N hosts mints N
  agents from one token today only if the token is multi-use. Should
  `auto_discover` + a multi-use token fan out N discovery scans (one per agent's
  own allowlist)? Likely yes, but confirm the token model first.
- **Q4. Proxy auth.** `--proxy=http://user:pass@host` puts credentials in argv
  and `agent.env`. Prefer a separate `--proxy-credentials-file` or env-only
  injection; resolve in PR 5.
- **Q5. Default discover cadence.** Is "once on connect, then weekly" the right
  default, or should the standing cron be opt-in only? Proposing on-connect
  always; weekly opt-in.
