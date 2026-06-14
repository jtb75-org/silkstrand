# ADR 016: Horizontal agent pooling & lease-based chunk dispatch

**Status:** Proposed (draft — design discussion 2026-06-13, kimi + hero)

**Related:** [ADR 007](./007-findings-scheduler.md) (scheduler), [ADR 013](./013-guided-rollout.md) (agent zones), chunked discovery (PR #380 — `scan_chunks`, `FOR UPDATE SKIP LOCKED` claim, heartbeat-gated reset/lease/reconcile), the `ClaimNextScanChunk` fix (PR #384).

## Context

Chunked discovery (#380) splits a scope into batch-sized chunks (`/22` default) dispatched **serially to one agent**. Live measurement shows the work is **I/O-bound, small-footprint, and embarrassingly parallel**: naabu ~1m CPU/93Mi, httpx ~65m/235Mi, nuclei bursty 1–494m CPU / ~450Mi; a `/26` with 16 web URLs takes **~20 min**, mostly waiting on LAN responses. A `/16` is 64 chunks run one at a time on a single agent — the obvious lever is **horizontal scaling**.

The chunk model already has the right primitive: `ClaimNextScanChunk` uses `SELECT … FOR UPDATE SKIP LOCKED` — the canonical concurrent work-queue claim. But it is scoped to `(scan_id, agent_id)`, and reset/recovery assumes `chunk.agent_id` *is* the owner. This ADR defines how to scale to **N workers** without breaking identity, ownership, or failure semantics.

## Problem

A naive `replicas: N` on the current Deployment doesn't work:
- All replicas would share one `agent_id` + the single-writer creds PVC (RWO).
- Chunks are bound to one `agent_id`; replicas wouldn't share the queue.
- Reset/abandon logic keys on `agent_id` as owner — ambiguous with a pool.

## Decision

### D1. Pool/zone is the *scheduling boundary*; `agent_id` stays the worker identity
Introduce an explicit **execution pool / zone** as the eligibility boundary. **Do not** make `agent_id` nullable or fuzzy — it remains the stable worker identity and **lease owner**. (Builds on ADR 013's agent *zones*.)

### D2. Two dispatch modes
- **Pinned** (current): a scan's chunks are assigned to one `agent_id`. Keep it — it's the right choice for **network locality / route constraints**, deterministic *"agent A scans subnet A"* audit semantics, **per-agent credentials/policy**, and **hard per-agent rate limits** for fragile segments.
- **Pooled**: a scan's chunks are *eligible* for a `pool_id`/`zone_id`; any worker in the pool claims chunks via SKIP-LOCKED **work-stealing**. Best for large dynamic scopes (better load balance + simpler recovery than scheduler-side sharding, which bakes decisions too early and rebalances anyway when chunk runtimes vary).

### D3. Lease-based chunk ownership
Replace "`chunk.agent_id` = owner" with an explicit lease. `scan_chunks` gains:
`pool_id`/`zone_id` (eligibility), `leased_by_agent_id`, `leased_at`, `lease_expires_at`, `lease_generation` (or `claim_token`), `attempt_count`.
- **Claim** (`ClaimNextScanChunk(pool, agent)`): selects an eligible chunk, sets `status=running`, `leased_by_agent_id`, `lease_expires_at`, bumps `lease_generation`.
- **Complete / fail / reset** operate on `chunk_id` **AND** `lease_generation` (or `leased_by_agent_id` + `status`) — so a **stale owner cannot complete a chunk after losing its lease** to a re-claim. This is the generalization of the P2 "active-parent guard" from #380, now also guarding *ownership*.

### D4. Heartbeat-driven lease liveness
Agent heartbeats **extend leases** for that agent's active chunks; the server derives liveness from `heartbeat + max_age`. Running chunks whose lease **expired** return to `pending` (or `failed`-retry by `attempt_count`). This generalizes #380's heartbeat-gated `ResetStaleRunningScanChunks` from "reset for a dead agent_id" to "reclaim expired leases."

### D5. Idempotent chunk writes (prerequisite for safe retry)
The agent **streams `asset_discovered` mid-chunk** (not only at the boundary), so a re-scanned chunk re-emits observations. Chunk retries are therefore safe **only with idempotent upserts** keyed by `(scan/asset identity)`. Assets already upsert by identity (ADR 006) — **endpoints and findings must be audited/enforced for the same** before pooling lands.

### D6. Enrollment: pool-join token mints a *per-agent* identity
Never a shared credential across replicas. A **pool-join token** authorizes *enrollment* — at startup each runtime instance mints its **own** `agent_id` + keypair, bounded by `max_members` + TTL (+ namespace/attestation where available). The token is **not** the agent's long-lived key; **avoid** "multi-use token becomes the agent key." Pinned agents keep the existing single-use install token.

### D7. Runtime topology
- **StatefulSet** with per-pod identity + per-pod PVC — pragmatic v1; durable identities (`lan-east-0..N`), UI groups them as one logical pool.
- **Ephemeral self-bootstrap** (pool-join token, no PVC) — better cloud-native UX; identities are disposable. Long-term target.

### D8. Capacity is a pool property, not a member count
`N replicas ≈ Nx` only until the LAN, target devices, DNS, or memory pressure dominate. The scheduler tracks **pool capacity**:
- `max_concurrent_chunks_per_pool`, `max_nuclei_concurrency_per_agent`
- per-subnet / per-target **request budgets** (protect fragile segments)
- **backpressure** from agent-reported CPU/mem/load
- chunk **weighting** by IP count / open-port count / prior runtime → **adaptive chunk sizing** (fast-follow): fixed batch size is fine to start, but runtime variance is huge (a chunk with 16 web URLs vs 400).

## Consequences
- Large scopes parallelize: a `/16` = 64 chunks across 8 pooled agents ≈ ~8× wall-clock, until a real bottleneck (LAN/target/budget) caps it.
- Pinned mode preserved for small customers, auditability, constrained routes, and per-agent credentials.
- More state + correctness burden: the **lease generation check (D3) must land before horizontal scaling**, or stale-completion/reset races get messy fast. Recommend implementing the lease model even for the *pinned* path first (it's strictly more correct).
- The "agent = one customer-network footprint" model still holds: pooling is for *large scopes / scanning fleets*; a single customer agent already parallelizes naabu/httpx/nuclei within a chunk.

## Open questions
1. Pool membership source of truth: declared (StatefulSet replicas) vs discovered (agents that joined with a pool token).
2. Cross-pool scan eligibility (can a scan target multiple zones?) and credential scoping per pool.
3. Lease duration + heartbeat-extension cadence vs nuclei's long (~20 min) silent stretches — leases must outlast a slow chunk without masking a dead worker.

## Scope / phasing
- **P1** — add lease columns + `lease_generation`; convert complete/fail/reset to lease-checked ops; keep **pinned** mode (strictly more correct, no behavior change). Audit endpoint/finding idempotency (D5).
- **P2** — `pool_id`/`zone_id` eligibility + pooled `ClaimNextScanChunk`; pooled dispatch mode on scans.
- **P3** — pool-join enrollment (D6) + StatefulSet topology (D7).
- **P4** — pool capacity, backpressure, adaptive chunk sizing (D8).
