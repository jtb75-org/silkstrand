# ADR 005: Audit events surface

**Status:** Accepted
**Date:** 2026-04-14
**Related:** [ADR 003](./003-recon-pipeline.md) (rule engine — fires audit events),
[ADR 004](./004-credential-resolver.md) (credential reads — audited today via slog only)

---

## Context

Several privileged operations on the DC side already emit `slog` events
that are meant to be auditable:

- `credential.fetch` — every time a scan directive decrypts and forwards
  a credential to the agent (`api/internal/handler/probe.go`,
  `api/internal/handler/agent.go`).
- `rule.fired` — every time the ADR 003 rule engine dispatches an action
  (`api/internal/rules/engine.go` + `main.go:runRuleActions`).
- `notification_deliveries` — persisted as structured rows already, but
  not surfaced outside the table.
- Agent lifecycle events (`agent.upgraded`, key rotation) — slog only.

None of this is queryable from the tenant UI. Admins asking "who set
that credential?" or "which rule sent that page?" have no answer short
of a Cloud Run log search, which isn't an experience we expose and
isn't tenant-scoped.

The onboarding-UX plan calls out audit surfacing as O7. The plan's open
question was whether it deserves its own ADR before we add a new table.
It does — partitioning, retention, and write-path volume are the kinds
of choices that cost if we make them wrong.

## Problem

Design a tenant-scoped, read-only-from-the-UI audit record that:

- captures the privileged events we already care about (credential
  reads, rule fires, notification deliveries, agent lifecycle);
- supports common tenant-admin queries (recent activity, filter by
  event type, filter by actor/target, time range);
- scales with the write volume implied by discovery scans + rule
  engine running per-asset (rule fires dominate the volume budget);
- is cheap to retain for 90 days by default, with a knob to extend;
- is trivially extensible when we add cloud discovery / new actions
  in R2+.

## Decisions

### D1. One unified `audit_events` table

Keep the taxonomy flat — one table, one row per event, tenant-scoped.
Alternative considered: per-domain tables (`credential_audits`,
`rule_fires`, etc.). Rejected because the UI query pattern is "show me
everything tagged for this tenant in this window," which either needs
UNIONs across N tables or rewiring every time we add a domain.

### D2. Schema

```sql
CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL,                -- FK handled via application retention
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    event_type TEXT NOT NULL,               -- 'credential.fetch' | 'rule.fired' | ...
    actor_type TEXT NOT NULL,               -- 'user' | 'agent' | 'system' | 'backoffice'
    actor_id TEXT,                          -- UUID as text (user_id / agent_id / null)
    resource_type TEXT,                     -- 'target' | 'rule' | 'channel' | 'asset' | ...
    resource_id TEXT,                       -- UUID as text
    payload JSONB NOT NULL DEFAULT '{}',    -- event-specific structured context
    PRIMARY KEY (tenant_id, occurred_at, id)
) PARTITION BY RANGE (occurred_at);
```

Rationale:

- **Monthly partitioning by `occurred_at`** mirrors
  `notification_deliveries` and `asset_events` (ADR 003). Partition
  prune keeps tenant queries fast; dropping a month is cheap.
- **Composite PK `(tenant_id, occurred_at, id)`** enables the tenant
  filter + range scan we want without a separate index.
- **`actor_*` / `resource_*` as TEXT + JSONB payload** keeps the table
  general. Strongly-typed FKs are inappropriate here because actors
  and resources are heterogeneous and we're logging for audit, not for
  referential integrity.
- **No FK on `tenant_id`** for the same reason partition-heavy tables
  avoid them: the constraint blocks partition pruning and churns when
  partitions detach.

### D3. Event taxonomy (v1)

Initial set, keyed on `event_type` string. Add new types without
migrations.

| event_type | actor | resource | payload highlights |
|---|---|---|---|
| `credential.fetch` | `agent` / `system` | `target` | scan_id, source_type (`static` / future) |
| `rule.fired` | `system` | `rule` | rule_name, asset_id, actions[] (types only — no bundle IDs or secrets) |
| `notification.sent` | `system` | `channel` | rule_name, status, latency_ms |
| `notification.failed` | `system` | `channel` | rule_name, status_code, error |
| `agent.upgraded` | `user` (if UI-triggered) / `system` | `agent` | from_version, to_version |
| `agent.key_rotated` | `user` | `agent` | — |
| `target.credential_set` | `user` | `target` | source_type |
| `target.credential_deleted` | `user` | `target` | — |
| `cloud.assume_role` | `agent` | `target` | account_id, role_arn, region | (R2 reservation) |

Policy: actor_id captures who/what initiated the action;
resource_id captures the object the action happened *to*. Both optional
for system-level events that don't have a clean parent object.

### D4. Write path — feature-flagged, fire-and-forget

Introduce `audit.Writer` interface with a single method
`Emit(ctx, Event)`. Two implementations:

- `PostgresWriter` — writes rows to `audit_events` via a bounded
  in-memory queue and a background flusher (50ms / 100-event batches).
- `NoopWriter` — default in tests and in dev when the feature flag is
  off.

Writer selection is driven by env `AUDIT_EVENTS_ENABLED=true`. Off by
default in dev; on in stage/prod once the migration lands.

`Emit` never blocks the caller beyond a channel send; a full queue
drops events and increments a counter the flusher exposes in its log
line. Correct choice because audit must not become a new failure mode
for scan ingest or rule evaluation.

### D5. Read path — one list endpoint

```
GET /api/v1/audit-events
  ?event_type=credential.fetch
  &actor_id=<uuid>
  &resource_id=<uuid>
  &since=<rfc3339>
  &until=<rfc3339>
  &limit=100
  &cursor=<opaque>
```

Response: `{ items: Event[], next_cursor?: string }`. Cursor
pagination is `(occurred_at, id)` tuple encoded — avoids OFFSET and
stays stable under concurrent inserts. No count — it's expensive on
partitioned tables and rarely what admins want.

### D6. Retention — 90 days default, per-tenant override in a follow-up

A nightly worker drops month-partitions older than 90 days by default.
Per-tenant retention (e.g. 365 days for a regulated customer) is a
future extension — a small `tenant_audit_settings` table with
`retention_days` overriding the default. Out of scope for v1.

### D7. UI — single Audit page

Navbar entry → table view with filter chips (event_type dropdown,
resource search, date range picker). Detail row expands to show the
`payload` JSON formatted. No write surface — this is a receipts-only
page. Tenant-admin role required; viewers see it too (audit is a
transparency feature, not sensitive).

### D8. Migration order

1. **Migration**: create `audit_events` (partitioned, first partition
   covering the current month).
2. **Writer + env flag**: ship `audit.Writer` plumbing, `NoopWriter`
   wired everywhere. No behavior change.
3. **Emit calls**: annotate the known sites (credential fetch, rule
   fires, notifications). Still flag-gated.
4. **Read API + UI**: ship the list endpoint + Audit page.
5. **Turn the flag on in stage**, soak, flip in prod.
6. **Partition maintenance worker** (nightly monthly-partition create +
   90-day drop). Can ship with step 1–3; required before step 5 to
   cap retention.

### D9. What we're NOT logging (v1)

- **Read operations** other than credential fetches. Admin viewing a
  dashboard doesn't produce an audit row.
- **Write of the audit table itself** (no meta-audit).
- **Raw secret values** — payload must carry references (IDs),
  never ciphertext or plaintext.
- **Bulk scan / result lifecycle** — already represented by the
  `scans` + `scan_results` tables; audit would duplicate.

## Consequences

**Positive**
- Tenant admins get a single place to answer the "who / when / what"
  questions about privileged operations.
- Additive schema; no existing table changes.
- Extensible — new event types are string additions; new filters are
  GET params.
- Decoupled from the hot paths via the writer queue.

**Negative / risks**
- Queue-backed writes mean we can lose audit rows on process crash
  before the next flush. For an auditing surface this is an
  acceptable-but-documented tradeoff — we're not a regulated-books
  logger. If that changes, switch to synchronous writes on the events
  that matter most.
- Taxonomy drift — without discipline the `event_type` strings will
  proliferate. Mitigation: document in a codegen'd constants file
  (`api/internal/audit/types.go`) and review additions.

## Open questions (for implementation PR review)

1. **Retention default**: 90 days is a placeholder. Is 180 days more
   appropriate given typical compliance review cadences?
2. **Role gating**: should viewers (non-admin) see the audit page? The
   data isn't secret, but it's verbose. My current lean: yes, they see
   it — transparency beats silos.
3. **Backfill from existing slog lines**: do we replay historical slog
   events into the table? My lean: no. Audit starts the moment the
   flag flips on, and we document that.
4. **Backoffice visibility**: should super-admins see cross-tenant
   audit? Probably yes for support scenarios, but requires a separate
   endpoint and is out of v1 scope.

---

## Addendum — 2026-06-14: payload enrichment + dedicated Audit Log page

A Stitch design-exploration mock of a dedicated Audit Log page surfaced fields
the shipped surface doesn't carry. Decisions on what to collect:

### A1. Enrich `payload`, don't add columns
Migration 026 settled on a flat schema and **dropped the `ip`/`actor_email`
columns** D2 originally sketched. Rather than re-add columns, the new fields go
in the existing **`payload` JSONB** — no migration; the list API already returns
`payload`, so the UI reads them for free. Promote a key to a real column only if
we ever need to *filter* on it (today we filter by `event_type`/`resource_id`/
time, not by ip/actor).

### A2. Collect ip / user_agent / actor_email — request-scoped, via context
Captured **once in middleware** into the request `context`; `Writer.Emit(ctx, …)`
merges them into `payload` at persist time, so every request-scoped emit is
enriched with **no change to the ~20 call sites**. `actor_email` comes from the
existing JWT `Claims.Email`; `ip` from `RemoteAddr`+`X-Forwarded-For`;
`user_agent` from the request header.

**Coverage caveat:** these exist only for **request-scoped** events. Background/
system events — the high-volume `rule.fired`, scheduler-driven `credential.fetch`,
agent lifecycle — have no request, so their `ip`/`user_agent` are **null by
design**. Correct, not a gap.

### A3. Collect `resource_label` — per emit site
The biggest UX win: a **human-readable target label** (e.g. "Prod Postgres creds"
vs `credential_source:9f8e…`), directly answering this ADR's "who touched *what*"
query. Not in context, so populated per emit site (the handler has the object);
prioritize credential sources, rules, scan defs, agents, collections.

### A4. Deferred / rejected
- **`user_agent`**: collected (near-free once ip plumbing exists) but low-value/
  partial — kept for forensic completeness, not relied on.
- **"Flag entry"**: rejected for v1 — a mutation that cuts against the read-only/
  immutable-audit guarantee. Revisit only as a separate annotation layer.
- **Human `ev_…` id**: cosmetic; the UUID stands.
- **Export CSV**: accepted as a *separate* small follow-up (stream the filtered
  query); not part of this enrichment.

### A5. Surface
Promote the audit log from the Settings sub-tab to a **dedicated page** (own nav
item) using the shipped DataTable + detail-drawer patterns: TIME (rel+abs), EVENT
pill, ACTOR (type badge + email), TARGET (label), SOURCE IP; row-click → drawer
(General Info / Target / Raw Metadata dark JSON block). 90-day retention (D6)
unchanged; ip/email are PII but covered by it.
