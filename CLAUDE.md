# SilkStrand

SilkStrand is a SaaS-based CIS compliance scanner that reaches into private customer environments via lightweight edge agents. Sensitive data never leaves the customer network — only structured compliance results traverse the tunnel.

## Architecture Overview

SilkStrand has a three-tier architecture: a **backoffice manager** (control plane), one or more **data centers** (logical "pseudo-DC" deployments), and **edge agents** (customer environments).

Today everything runs on **one self-hosted k3s cluster** (homelab). The backoffice and each data center are **k8s namespaces** (`silkstrand-backoffice`, `dc-us`, future `dc-*`) in that single cluster — not separate GCP projects. The backoffice/DC split is preserved (Option A): the backoffice talks to each DC over **in-cluster service DNS** rather than a public URL.

```
┌─────────────────────────────────────────────────────────────────────┐
│                    k3s cluster (homelab)                            │
│                                                                     │
│  ┌─ namespace: silkstrand-backoffice ──────────────────────────┐   │
│  │  ┌──────────┐  ┌──────────────┐  ┌──────────────────────┐   │   │
│  │  │ React UI │  │ Go API       │  │ CloudNativePG         │   │   │
│  │  │ (Admin)  │──│ (Deployment) │──│ (DCs, tenants, admin) │   │   │
│  │  └──────────┘  └──────┬───────┘  └──────────────────────┘   │   │
│  └───────────────────────┼──────────────────────────────────────┘   │
│         in-cluster HTTP   │  http://silkstrand-api.dc-us.svc.cluster.local:8080
│        ┌──────────────────┼──────────────────┐                      │
│        ▼                  ▼                  ▼                      │
│  ┌────────────┐    ┌────────────┐    ┌────────────┐                │
│  │ ns: dc-us  │    │ ns: dc-eu  │    │ ns: dc-*   │                │
│  └─────┬──────┘    │  (future)  │    │  (future)  │                │
│        │           └────────────┘    └────────────┘                │
│  ┌─────┴──────────────────────────────────────────────────────┐   │
│  │  namespace: dc-us                                          │   │
│  │  ┌──────────┐  ┌──────────┐  ┌────────┐  ┌──────────────┐  │   │
│  │  │ React UI │  │  Go API  │  │ Redis  │  │ bundles      │  │   │
│  │  │ (Tenant) │──│(Deploy-  │──│(Deploy,│  │ (MinIO,      │  │   │
│  │  │          │  │  ment)   │  │ pub/sub│  │  in-cluster) │  │   │
│  │  └──────────┘  └────┬─────┘  └────────┘  └──────┬───────┘  │   │
│  │                ┌────┴───────────┐                │          │   │
│  │                │ CloudNativePG  │                │          │   │
│  │                │ (per-ns Pg 16) │                │          │   │
│  │                └────────────────┘                │          │   │
│  └──────────────────────┬───────────────────────────┼──────────┘   │
└─────────────────────────┼───────────────────────────┼──────────────┘
                          │                           │
   Cloudflare tunnel +    │   WSS 443 (outbound)      │
   traefik ingress ───────┼───────────────────────────┼──────────────
                          │                           │
┌─────────────────────────┼───────────────────────────┼──────────────┐
│   Customer Environment                              │              │
│  ┌──────────────────────┴───────────────────────────┴───┐          │
│  │           SilkStrand Agent (Go binary)               │          │
│  │  ┌────────┐ ┌────────┐ ┌───────┐ ┌────────┐         │          │
│  │  │ Tunnel │ │ Runner │ │ Cache │ │ Vault  │         │          │
│  │  │ (WSS)  │ │(Python)│ │(local)│ │ Client │         │          │
│  │  └────────┘ └───┬────┘ └───────┘ └───┬────┘         │          │
│  └─────────────────┼────────────────────┼───────────────┘          │
│             ┌──────┴──────┐    ┌────────┴────────┐                 │
│             │ Scan Targets│    │  Secret Store    │                 │
│             │ (DB, OS)    │    │ (Vault, CyberArk)│                 │
│             └─────────────┘    └─────────────────┘                 │
└─────────────────────────────────────────────────────────────────────┘
```

Platform images are pulled from the in-cluster **zot** OCI registry; the customer agent image is published to a public zot ingress (agents can't reach the LAN registry). Deploys are GitOps via Argo CD.

### Key Driver: Data Residency (relaxed)

Originally each data center was a full regional deployment so EU data stayed in EU. On the homelab k3s cluster, data residency is **relaxed for now**: the backoffice and DCs are logical namespaces in a single cluster (a "pseudo-DC" model). The path back to true per-region residency is to **promote a namespace to its own regional cluster** later. The backoffice still provides cross-DC visibility and management without touching DC databases directly (per-namespace DB isolation is the boundary).

## Tech Stack

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Agent | Go | Single binary, cross-compilation, goroutines for concurrency |
| DC API Server | Go | One language across backend, strong stdlib |
| Backoffice API | Go | Same patterns as DC API, separate deployment |
| Tenant Frontend | React + TypeScript | Rich component ecosystem, standard SPA |
| Backoffice Frontend | React + TypeScript | Same stack, separate app, navy/teal theme |
| Database | CloudNativePG (PostgreSQL 16) | One `Cluster` per namespace (instance-per-DC); `storageClass: longhorn`. App `DATABASE_URL` from the CNPG `<cluster>-app` secret. |
| Real-time | Self-hosted Redis | One Redis Deployment per `dc-*` namespace (pub/sub for scan directives); replaces Upstash. Backoffice uses no Redis. |
| Hosting | k3s (self-hosted homelab) | Single cluster; backoffice + each DC are k8s namespaces. Deployments, not serverless. |
| Object Storage | In-cluster bundles (MinIO) | Compliance bundles served in-cluster. (Customer-facing agent/runtime artifacts NOT yet migrated — still GCS.) |
| Container Registry | zot (OCI) | `zot.lan.ng20.org` (LAN, in-cluster pulls); `zot.ng20.org` (public, customer agent image). |
| Deploy / IaC | Kustomize + Argo CD (GitOps) | App manifests in `jtb75-org/silkstrand-gitops`, auto-synced by Argo CD. OpenTofu retained for any residual GCP infra not yet decommissioned. |
| Secrets | OpenBao + External Secrets Operator | OpenBao (Vault-compatible, auto-unsealed) KV v2; ESO `ClusterSecretStore` syncs into native k8s Secrets per namespace. |
| DNS / ingress | Cloudflare + cloudflared + traefik + cert-manager | DNS + tunnel + ingress + TLS, domain: silkstrand.io |
| Auth (tenant) | In-house: bcrypt + HS256 JWT | Users + memberships live in the backoffice; DCs validate JWTs with a shared `TENANT_JWT_SECRET`. |
| Auth (admin) | In-house: bcrypt + JWT | Backoffice admin login (separate from tenant users). |
| Transactional email | Resend | Invitations, password resets. Pluggable `mailer.Mailer` interface. |

## Project Structure

```
silkstrand/
├── api/                    # Data Center Go API server (k3s Deployment)
│   ├── cmd/silkstrand-api/ # Entry point
│   ├── internal/
│   │   ├── config/         # Environment-based config
│   │   ├── crypto/         # AES-256-GCM for credential encryption
│   │   ├── handler/        # HTTP handlers (health, target, scan, agent, internal)
│   │   ├── middleware/     # Auth (JWT), tenant isolation, internal API key, logging
│   │   ├── model/          # Domain types
│   │   ├── store/          # Postgres data access + migrations
│   │   ├── pubsub/         # Redis pub/sub
│   │   ├── rules/          # ADR 006 rule engine (match {collection_id} + actions)
│   │   ├── notify/         # Notification dispatcher (webhook + slack live; email/pagerduty stub)
│   │   ├── scheduler/      # ADR 007 in-process scan_definition scheduler
│   │   └── websocket/     # Agent WebSocket hub + message types
│   └── Dockerfile
├── agent/                  # Go edge agent binary
│   ├── cmd/silkstrand-agent/
│   └── internal/
│       ├── config/         # Agent configuration (env vars)
│       ├── tunnel/         # WSS connection, reconnect, message types
│       ├── runner/         # Python compliance runner, recon runner
│       │   └── recon/      # ADR 003 R1a: naabu/httpx/nuclei pipeline + PD tool installer + allowlist + redaction
│       ├── prober/         # Per-engine probers (postgres/mssql/mongodb/mysql)
│       └── cache/          # Local bundle cache
├── backoffice/             # Backoffice Manager (separate deployment)
│   ├── cmd/backoffice-api/ # Entry point
│   ├── internal/
│   │   ├── config/         # Config (port 8081, own DB on 15433)
│   │   ├── crypto/         # AES-256-GCM for DC API key encryption
│   │   ├── dcclient/       # HTTP client for DC internal API
│   │   ├── handler/        # Datacenter, tenant, health, auth handlers
│   │   ├── middleware/     # Admin JWT auth, role-based access, logging
│   │   ├── model/          # Backoffice domain types
│   │   └── store/          # Postgres data access + migrations
│   └── web/                # Backoffice React frontend (navy/teal theme)
│       └── src/
│           ├── api/        # API client + types
│           ├── components/ # Layout, StatusBadge, DataCenterCard
│           └── pages/      # Login, Dashboard, DataCenters, Tenants
├── web/                    # Tenant React frontend
│   └── src/
│       ├── api/            # API client + types
│       ├── components/     # Layout, AssetsTable, AssetDetailDrawer, AssetsTopology (xyflow), CredentialModal, etc.
│       └── pages/          # Dashboard, Assets (3-tab), Findings, Collections,
│                           #   ScanDefinitions, ScanActivity, Credentials,
│                           #   CorrelationRules, Agents, Targets, Settings, Team,
│                           #   Scans, ScanResults (+ auth pages)
├── bundles/                # Compliance bundles
│   ├── cis-postgresql-16/
│   ├── cis-mssql-2022/
│   └── cis-mongodb-8/
├── terraform/
│   ├── bootstrap/
│   ├── environments/
│   │   ├── stage/
│   │   └── prod/
│   └── modules/
│       ├── networking/
│       ├── database/
│       ├── cloud-run/
│       ├── storage/
│       └── dns/
├── docs/                   # Architecture, user stories, ADRs, CI/CD
├── docker-compose.yml      # Local dev: Postgres (15432), Redis (16379), Backoffice Postgres (15433)
└── Makefile
```

## Current State

### Roadmap (ADR 006 / 007 asset-first phases)

| Phase | What | Status |
|---|---|---|
| **Legacy R0–R1.5** | Recon pipeline, asset sets, notification channels, one-shots (ADR 003) | ✅ shipped, then **retired** in v0.1.49 (replaced by ADR 006/007) |
| **P1** | Greenfield migration 017: drop old recon tables; add assets, asset_endpoints, collections, findings, scan_definitions, credential_mappings; UUID randomness cleanup | ✅ shipped v0.1.49 |
| **P2** | Ingest pipeline (naabu/httpx/nuclei → assets + endpoints + findings) and new rules engine (`match: {collection_id}`, actions) wired to discovery | ✅ shipped v0.1.49 |
| **P3** | Findings API + Scans reshape + scan_definitions CRUD + in-process scheduler | ✅ shipped v0.1.49 |
| **P4** | Collections (with `scope=finding`), Assets 3-tab UI, detail drawer, coverage roll-ups | ✅ shipped v0.1.49 |
| **P5-a** | Dashboard KPIs + Suggested Actions + Recent Activity widgets | ✅ shipped v0.1.49 |
| **P5-b** | Consolidated Credentials page (sources + mappings; former Channels folded in) | ✅ shipped v0.1.49 |
| **P6-prep** | Asset-first audit gaps closed; migration ordering hardened | ✅ shipped v0.1.49 |
| **P6** | Integration smoke (end-to-end asset-first scan lifecycle on stage) before prod tag | ✅ shipped v0.1.49 (tagged to prod) |
| **v0.1.50** | Fix `snapshot_hash` field on agent allowlist ingestion | ✅ shipped |
| **v0.1.51** | Nginx SPA fallback fix (403 on deep-link refresh) | ✅ shipped |
| **v0.1.52** | Flatten asset API response + Assets/Endpoints rendering fix + `time.ts` date formatting | ✅ shipped |
| **Deferred** | Notification retry worker · email + pagerduty senders · vault resolvers (ADR 004 C1+) · `suggest_target` DB writeback · AWS cloud discovery (R2) | ⏸ |

ADR 004 (credential resolver) is at **C0** (plumbing — `credential_sources.type` now covers `static` plus the integration/vault slots reserved for C1+ resolvers; legacy `credentials` table dropped in migration 014). C1+ resolvers (AWS Secrets Manager / Vault / etc.) are planned but not started.

Implementation plans live in `docs/plans/ui-shape.md` (asset-first nav + page shape), `docs/plans/asset-first-execution.md` (phase-by-phase plan), `docs/plans/onboarding-ux.md`, and `docs/plans/scan-progress-and-sse.md`; design rationale in `docs/adr/`, with ADR 006 (asset-first data model) and ADR 007 (findings + scheduler) as the sources of truth for the current model.

### What's Built

- **Data Center API** — Full Go API server with:
  - User-facing routes (JWT + tenant middleware): assets + endpoints, collections, findings, scan_definitions, correlation_rules, credential_sources + credential_mappings, agents, targets (retained as the "how-to-scan" surface), dashboard widgets
  - HS256 tenant JWT validation using a `TENANT_JWT_SECRET` shared with the backoffice. Stdlib-only crypto. JWT iss/aud claims (`silkstrand-backoffice` / `silkstrand-tenant-api`).
  - Agent WebSocket endpoint with per-agent API key auth (SHA-256, dual-key rotation)
  - Internal API routes (`/internal/v1/`) for backoffice access (API key auth)
  - Tenant status enforcement (active/suspended/inactive with 5s TTL cache)
  - Scan lifecycle: scan_definition or ad-hoc scan → directive via Redis → agent executes → findings streamed back via WSS → stored in Postgres (`findings` table, unified across compliance + network)
  - Credential storage via the ADR 004 `credential_sources` abstraction (today: `static`; pluggable slots for AWS Secrets Manager / Vault / etc. in C1+). AES-256-GCM at rest using `CREDENTIAL_ENCRYPTION_KEY`. `credential_mappings` binds a source to specific assets/endpoints. `credential.fetch` slog audit event on every read.
  - Stuck scan cleanup: running scans fail automatically on agent disconnect
  - Dockerfile: multi-stage (golang:1.25-alpine → distroless)
- **Asset-first pipeline (ADR 006 / 007)** — Discovery scans run end-to-end: agent runs naabu → httpx → nuclei against allowlisted targets, streams batches over WSS, server upserts `assets` + `asset_endpoints` and records `findings` of kind `network_vuln` / `network_compliance`. Authenticated compliance scans emit `bundle_compliance` findings. Correlation rules evaluate on every ingest; rule body is `{match: {collection_id}, actions: [...]}` with `notify`, `run_scan_definition`, `auto_create_target`, and `suggest_target` (log-only) actions. Webhook + Slack senders are live (HMAC-signed webhooks, secrets encrypted at rest); email + pagerduty are stubbed. Scheduler (`internal/scheduler`) fires `scan_definitions` on their cron cadence against a `collection_id` scope.
- **Collections** — saved JSONB predicates over assets/endpoints/findings (`scope` in `asset | endpoint | finding`). Replaces the legacy asset_sets; used as scope by scan_definitions and rules, and as the scoping surface for findings views.
- **Edge Agent** — Go binary with WSS tunnel (exponential backoff reconnect), Python compliance runner, recon runner (naabu/httpx/nuclei via runtime download from `gs://silkstrand-runtimes`), bundle cache, heartbeat. Customer-controlled scan allowlist (`/etc/silkstrand/scan-allowlist.yaml`) gates every recon directive. Docker-mode installer shipped in v0.1.48. Cross-compiled for 6 platforms on release.
- **Compliance Bundles** — three live: `cis-postgresql-16`, `cis-mssql-2022`, `cis-mongodb-8`. Per-engine probers (postgres, mssql, mongodb, mysql) wire credentials through to the Python runtime via FD pipe (no on-disk credential file on Unix).
- **Tenant Frontend** — React + TypeScript SPA with asset-first navigation (v0.1.49+):
  - **Dashboard** — KPI row (total assets, endpoints with findings, coverage %, open findings) + Suggested Actions widget + Recent Activity feed
  - **Assets** — three tabs (Assets / Endpoints / Findings), bulk actions bar, coverage column, save-as-collection; asset detail drawer with 6-section layout (Identity / Lifecycle / Risk / Endpoints / Coverage / Relationships)
  - **Findings** — two tabs (Vulnerabilities / Compliance), suppress/reopen lifecycle, collection-scoped filtering
  - **Collections** — replaces Asset Sets; two tabs, visual predicate builder (scope: asset / endpoint / finding), query preview, scope picker
  - **Scans** — three tabs (Definitions / Activity / Targets), coverage impact strip; scan definitions CRUD + cron schedule + manual execute
  - **Credentials** — consolidated: credential sources + mappings (former Notification Channels UX folded in)
  - **Settings** — tabbed layout (Profile / Team / Credentials / Audit placeholder)
  - **Rules** (correlation rule CRUD with edit + auto-versioning; match by collection_id), **Agents** (install-token one-liner, per-agent Allowlist viewer, Upgrade, Rotate key, Delete), **Targets** (retained as the "how-to-scan" surface per endpoint)
  - In-house auth (login / accept-invite / forgot-password / reset-password), `<TenantSwitcher />` in the topbar. Dockerfile with nginx that splits `/api/v1/tenant-auth/*` → backoffice and `/api/*` → DC API.
- **Backoffice Manager** — Separate Go module + React frontend:
  - Data center registration with AES-256-GCM encrypted API key storage
  - Two-phase tenant provisioning (backoffice DB → DC API call, retry on failure)
  - Tenant suspend/activate with DC sync
  - Health poller (60s) monitors all registered data centers
  - Admin JWT auth with role-based access (viewer/admin/super_admin) + bcrypt login
  - **Tenant user auth** (in-house replacement for Clerk): users, memberships, invitations, password_resets tables; HS256 JWT signed with `TENANT_JWT_SECRET` (same secret every DC uses to validate). Endpoints: `/api/v1/tenant-auth/{login,accept-invite,forgot-password,reset-password,me,switch-org,members,invites}`.
  - **Transactional email** via Resend (`internal/mailer`): invitation emails with single-use tokens, password-reset emails with 1h expiry. Falls back to a noop logger if no API key (local dev).
  - Dashboard with DC health cards, cross-DC tenant management
  - Dockerfile: multi-stage (golang → distroless for API, node → nginx for web)
- **CI/CD** — GitHub Actions on **self-hosted ARC runners** (`jtb75-arc`, autoscaling min1/max4). `build-and-push.yml`: push to main / `workflow_dispatch` → a `Detect Changes` job (paths-filter, runs in the `atlas/builder` container) gates four **kaniko** build jobs → push `zot.lan.ng20.org/silkstrand-{api,web,backoffice-api,backoffice-web}:sha-<short>` (+ `:main-latest`) → a `Bump GitOps Image Tags` job seds the `newTag` in `silkstrand-gitops` and pushes → Argo CD syncs. Build-once-promote-by-SHA preserved. `ci.yml` (PR lint/test/build-verify) remains; `destroy.yml`, `release-agent.yml`, `release-pd-tools.yml` remain. Old GCP `deploy-stage.yml`/`deploy-prod.yml` retired.
- **GitOps** — App manifests in `jtb75-org/silkstrand-gitops` (Kustomize: `apps/<ns>/overlays/onprem` over a `base/components/*`). Argo CD auto-syncs; parent Argo Application registered in `jtb75-org/argocd-apps`. Each `dc-*` self-registers with the backoffice via an Argo **PostSync Job** running `backoffice-api register-dc`.
- **Seed Tooling** — SQL seeds for DC + backoffice databases, runner script, JWT generator, bcrypt hash helper. `make seed`, `make jwt`.
- **Terraform** — OpenTofu modules retained for residual GCP infra (not yet decommissioned). App deploy is now Kustomize + Argo CD, not Terraform-provisioned Cloud Run.

### What's Deployed

Everything runs on one self-hosted **k3s** cluster, Argo CD-synced from `silkstrand-gitops`. Per-namespace workloads:
- `dc-us` namespace: `silkstrand-api`, `silkstrand-web` (DC API + tenant frontend) as k8s Deployments, plus a `silkstrand-redis` Deployment and a CloudNativePG `Cluster`.
- `silkstrand-backoffice` namespace: `backoffice-api`, `backoffice-web` Deployments + its own CloudNativePG `Cluster` (no Redis). One backoffice manages all DCs.

Per-namespace infra: CloudNativePG Postgres 16 (`storageClass: longhorn`), self-hosted Redis (DC namespaces only), in-cluster bundles (MinIO). Ingress via Cloudflare DNS + cloudflared tunnel + traefik + cert-manager. Platform images pulled from `zot.lan.ng20.org` using a per-namespace `zot-pull` dockerconfigjson SealedSecret.

Secrets are managed by **OpenBao** + **External Secrets Operator**: values live under `secret/silkstrand/*` in OpenBao (KV v2), and per-namespace `ExternalSecret`s sync them into native k8s Secrets that the app consumes as plain env vars (DATABASE_URL comes from the CNPG `<cluster>-app` secret instead). GCP Secret Manager and WIF are gone. Sealed-secrets is used only for the `zot-pull` pull-secret and an OpenBao bridge secret.

### What's Not Built Yet

- **CIDR-scope scheduler dispatch** — `scope_kind=cidr` logs and skips; only `collection` scope fires today
- **Endpoints tab per-port API** — currently shows host-level rows; per-port detail not yet wired
- **Full design-system token migration** — `.ss-*` CSS classes partially adopted; not yet project-wide
- **Compliance scan against real DB target** — pipeline proven end-to-end but no compliance findings generated yet
- **Email + PagerDuty notification senders** — stubbed; webhook + Slack are live
- **Notification retry worker** — failed delivery rows stay failed; rule must re-fire to retry
- **Per-agent scan queue** — concurrent scans fail rather than queuing
- **Docker-agent installer UI** — plan + shell script done; install-command generator not built
- **AWS cloud discovery** (`target_type: aws_account`, cloud-native credential auto-binding via `MasterUserSecret`)
- **Vault / CyberArk / AWS Secrets Manager credential resolvers** (ADR 004 C1+)
- **Bundle upload API** — bundles registered in DB but no upload endpoint; currently seeded manually
- **Agent WebSocket origin restriction** for production
- **Audit log UI** (ADR 005 drafted; placeholder tab exists in Settings)

## DC API Routes

```
# Public (no auth)
GET  /healthz                              # Liveness
GET  /readyz                               # Readiness: DB + Redis

# Agent WebSocket (per-agent key auth)
GET  /ws/agent?agent_id={id}               # Authorization: Bearer {agent_key}

# Agent bootstrap (install-token auth)
POST   /api/v1/agents/bootstrap            # Redeem install token → agent id + key
DELETE /api/v1/agents/self?agent_id={id}   # Agent self-deregister (uses agent key)

# Internal (X-API-Key auth, for backoffice)
POST   /internal/v1/tenants                # Create tenant
GET    /internal/v1/tenants                # List all tenants
GET    /internal/v1/tenants/{id}           # Get tenant
PUT    /internal/v1/tenants/{id}           # Update tenant (name, status, config)
DELETE /internal/v1/tenants/{id}           # Soft delete (set inactive)
GET    /internal/v1/agents                 # List all agents (cross-tenant)
GET    /internal/v1/stats                  # DC aggregate stats
POST   /internal/v1/credentials            # Store encrypted credential
PUT    /internal/v1/bundles                # Upsert bundle metadata

# Tenant API (JWT + tenant middleware)

## Assets + endpoints (ADR 006)
GET    /api/v1/assets                              # Filter: service, ip CIDR, source, has_findings, new_since, q
GET    /api/v1/assets/{id}                         # Asset + endpoints + recent findings
GET    /api/v1/assets/{id}/endpoints/{endpoint_id} # Endpoint detail
POST   /api/v1/assets/{id}/promote                 # Create a target bound to this asset

## Collections (ADR 006 — replaces asset_sets; scope: asset | endpoint | finding)
GET    /api/v1/collections
POST   /api/v1/collections                 # body: {name, scope, predicate}
POST   /api/v1/collections/preview         # ad-hoc {scope, predicate} → {count, sample}
GET    /api/v1/collections/{id}
PUT    /api/v1/collections/{id}
DELETE /api/v1/collections/{id}
POST   /api/v1/collections/{id}/preview
GET    /api/v1/collections/{id}/members

## Findings (ADR 007 — unified across network + compliance)
GET    /api/v1/findings                    # Filter: kind, severity, status, asset_id, endpoint_id, collection_id, q
GET    /api/v1/findings/{id}
POST   /api/v1/findings/{id}/suppress
POST   /api/v1/findings/{id}/reopen

## Scan definitions + scheduler (ADR 007)
GET    /api/v1/scan-definitions
POST   /api/v1/scan-definitions            # body: {name, scan_type, bundle_id?, collection_id, schedule_cron, agent_id}
GET    /api/v1/scan-definitions/{id}
PUT    /api/v1/scan-definitions/{id}
DELETE /api/v1/scan-definitions/{id}
POST   /api/v1/scan-definitions/{id}/execute   # Ad-hoc kick
POST   /api/v1/scan-definitions/{id}/enable
POST   /api/v1/scan-definitions/{id}/disable
GET    /api/v1/scan-definitions/{id}/coverage  # Matched endpoints + credential coverage

## Scan runs (activity)
POST   /api/v1/scans                       # Ad-hoc run (usually from a scan_definition)
GET    /api/v1/scans                       # List scan runs
GET    /api/v1/scans/{id}                  # Scan run detail (findings linked via scan_id)
DELETE /api/v1/scans/{id}

## Correlation rules (ADR 006 — new body shape)
GET    /api/v1/correlation-rules
POST   /api/v1/correlation-rules           # body: {name, trigger, body: {match: {collection_id}, actions: [...]}}
GET    /api/v1/correlation-rules/{id}
PUT    /api/v1/correlation-rules/{id}      # auto-versions
DELETE /api/v1/correlation-rules/{id}      # soft (disables latest)

## Credentials (ADR 004 — sources + mappings; Notification Channels folded into this surface)
GET    /api/v1/credential-sources
POST   /api/v1/credential-sources          # type: static (C0); C1+: aws_secrets_manager, vault, ...
GET    /api/v1/credential-sources/{id}     # secrets returned as '(set)'
PUT    /api/v1/credential-sources/{id}
DELETE /api/v1/credential-sources/{id}
GET    /api/v1/credential-mappings         # Bind a source to asset(s) / endpoint(s)
POST   /api/v1/credential-mappings
POST   /api/v1/credential-mappings/bulk
GET    /api/v1/credential-mappings/{id}
DELETE /api/v1/credential-mappings/{id}

## Targets (retained as the "how-to-scan" per-endpoint surface)
GET    /api/v1/targets
POST   /api/v1/targets
GET    /api/v1/targets/{id}
PUT    /api/v1/targets/{id}
DELETE /api/v1/targets/{id}
PUT    /api/v1/targets/{id}/credential     # Set credential_source binding
GET    /api/v1/targets/{id}/credential     # {set, type} (never plaintext)
DELETE /api/v1/targets/{id}/credential
POST   /api/v1/targets/{id}/probe          # Test connectivity (sync via Redis pub/sub)

## Agents + bundles
GET    /api/v1/bundles
GET    /api/v1/agents
POST   /api/v1/agents
GET    /api/v1/agents/{id}
GET    /api/v1/agents/{id}/allowlist       # Agent's most recently reported scan allowlist snapshot
POST   /api/v1/agents/{id}/rotate-key
POST   /api/v1/agents/{id}/upgrade
DELETE /api/v1/agents/{id}
POST   /api/v1/agents/install-tokens
GET    /api/v1/agents/downloads            # Per-platform agent binary URLs

## Dashboard
GET    /api/v1/dashboard                   # Legacy rollup
GET    /api/v1/dashboard/kpis              # KPI tiles
GET    /api/v1/dashboard/suggested-actions
GET    /api/v1/dashboard/recent-activity
```

## Backoffice API Routes

```
# Public (no auth)
GET  /healthz                              # Liveness
GET  /readyz                               # Readiness: DB
POST /api/v1/auth/login                    # Admin login (email + password → JWT)

# Admin (JWT with role-based access)
GET    /api/v1/dashboard                   # Aggregate stats from all DCs
POST   /api/v1/data-centers                # Register DC (name, region, url, api_key)
GET    /api/v1/data-centers                # List DCs with health status
GET    /api/v1/data-centers/{id}           # DC detail (optional ?stats=true)
PUT    /api/v1/data-centers/{id}           # Update DC
DELETE /api/v1/data-centers/{id}           # Soft delete DC
POST   /api/v1/tenants                     # Create tenant (two-phase provisioning)
GET    /api/v1/tenants                     # List tenants (optional ?data_center_id=)
GET    /api/v1/tenants/{id}                # Get tenant
PUT    /api/v1/tenants/{id}                # Update tenant
PUT    /api/v1/tenants/{id}/status         # Toggle active/suspended
POST   /api/v1/tenants/{id}/retry          # Retry failed provisioning
```

## WebSocket Protocol

Agent-to-API messages use `{"type": "string", "payload": {...}}` envelope.

| Type | Direction | Payload |
|------|-----------|---------|
| `directive` | server → agent | scan_id, **scan_type** (compliance\|discovery), bundle_id/name/version, target_id/type/identifier/config, credentials (compliance only) |
| `scan_started` | agent → server | scan_id |
| `scan_results` | agent → server | scan_id, results (standard schema) — compliance only |
| `scan_error` | agent → server | scan_id, error — persisted to `scans.error_message` and rendered on the Scan Results page |
| `asset_discovered` | agent → server | scan_id, batch_seq, stage (naabu/httpx/nuclei), assets[] — discovery; processed inline |
| `discovery_completed` | agent → server | scan_id, assets_found, hosts_scanned — terminal for discovery |
| `probe` / `probe_result` | bidir | One-shot connectivity check (Test Connection button) |
| `upgrade` | server → agent | New version + sha256-by-platform → agent self-updates and exits |
| `heartbeat` | agent → server | version, uptime_seconds |

Server sends WebSocket pings every 30s; agent responds with pong (60s timeout).

## Architectural Principles

1. **Data never leaves the customer network** — raw config data stays on-prem. Only structured results (pass/fail, evidence snippets) traverse the tunnel.
2. **Data residency (relaxed, logical for now)** — DCs are currently logical namespaces in one k3s cluster (pseudo-DC); per-namespace DB isolation is the boundary. True per-region residency returns by promoting a namespace to its own regional cluster. Backoffice manages across DCs without direct DB access.
3. **Outbound-only connectivity** — agents never require inbound firewall rules. WSS over 443, proxy-compatible.
4. **Credential encryption at rest** — Pluggable resolver via `credential_sources` (ADR 004). Today: `static` source — AES-256-GCM in DC database, key from `CREDENTIAL_ENCRYPTION_KEY` (per-namespace, sourced from OpenBao via ESO), decrypted before forwarding to agent over WSS. C1+: per-source agent-side resolution against AWS Secrets Manager / Vault / etc., zero plaintext at rest.
5. **Framework-agnostic execution** — polyglot bundle runtime. Bundle authors choose their assessment language; standardized JSON output schema is the contract.
6. **Thin agent, smart bundles** — agent is tunnel + runner + cache. All compliance logic lives in updateable bundles.
7. **Self-hosted homelab-first** — runs on owned hardware (one k3s cluster) rather than serverless cloud. No per-request cloud billing; capacity is the homelab's. Resource-frugal by default (modest replica counts, scale-down where idle), with managed-operator building blocks (CNPG, OpenBao, Argo CD) keeping ops load low.
8. **Single-person sustainability** — boring technology, minimal dependencies, one language (Go) on the backend.

## Coding Conventions

### Go (Agent + API + Backoffice)

- Go 1.25
- Standard `go fmt` and `go vet`
- Use stdlib where possible; minimize third-party dependencies
- Stdlib `net/http` router (Go 1.22+ enhanced routing, no gorilla/mux)
- Error handling: wrap errors with context using `fmt.Errorf("doing X: %w", err)`
- Logging: structured logging (slog, JSON output)
- Tests: table-driven tests, test files alongside source
- No global mutable state; pass dependencies via constructors
- DC API deps: pgx (Postgres), gorilla/websocket, go-redis, golang-migrate
- Agent deps: gorilla/websocket, gopkg.in/yaml.v3
- Backoffice deps: pgx, golang-migrate, golang.org/x/crypto (bcrypt)

### React + TypeScript (Web + Backoffice Web)

- Functional components with hooks
- TypeScript strict mode
- Plain CSS (no framework)
- One component per file
- @tanstack/react-query for data fetching
- Always run `tsc --noEmit` and `eslint` before committing

### Terraform / OpenTofu

- One module per logical service
- Variables for all environment-specific values
- Remote state in GCS backend
- No hardcoded project IDs, regions, or credentials

### General

- Commits: conventional commits format (`feat:`, `fix:`, `docs:`, etc.)
- Branches: `feature/`, `fix/`, `docs/` prefixes
- PRs: require description of what and why
- No secrets in code — use environment variables (sourced from OpenBao via ESO in-cluster)

## Key Design Decisions

- **Per-agent API keys**: Each agent gets a unique 256-bit key (SHA-256 hashed in DB). Dual-key rotation via `key_hash` + `next_key_hash`. Constant-time comparison.
- **Tenant status enforcement**: Middleware checks tenant status with 5s TTL cache. Suspended tenants get 403 on all API routes and agent WSS connections.
- **Backoffice as a namespace**: Runs in its own `silkstrand-backoffice` namespace in the same k3s cluster as the DCs — not a separate cluster/project. Gets its own CloudNativePG `Cluster`. One backoffice manages all DCs (current `dc-us`, future `dc-*`).
- **Pseudo-DC via namespaces + in-cluster DNS**: The backoffice/DC split (Option A) is kept, but the backoffice addresses each DC by in-cluster service DNS (e.g. `http://silkstrand-api.dc-us.svc.cluster.local:8080`) instead of a public URL. DC self-registration runs as an Argo PostSync Job (`backoffice-api register-dc`), replacing the manual admin POST to `/api/v1/data-centers`.
- **Two-phase tenant provisioning**: Create in backoffice DB first (provisioning_status=pending), then call DC API. On failure, mark as failed with retry option. Returns 202 (not 201) on DC provisioning failure.
- **Credential encryption at rest**: AES-256-GCM with `CREDENTIAL_ENCRYPTION_KEY` env var (per-namespace, synced from OpenBao via ESO). DC API decrypts before sending to agent. No encryption key = passthrough (dev only). Post-MVP: tenant-configurable credential sources (Vault, CyberArk, etc.).
- **In-house tenant auth over Clerk**: Replaced Clerk with bcrypt + HS256 JWTs. Rationale: B2B model with admin-managed invitations, small user counts, Clerk's embedded UI was too hard to customize (CSS hacks to hide "Leave organization", no server-side toggle), external dependency per sign-in, and we already had the Go auth plumbing for backoffice admins. DC API validates `TENANT_JWT_SECRET`-signed tokens; tenant frontend hits backoffice for `/api/v1/tenant-auth/*` (via nginx split) and DC for everything else. Multi-tenant membership is native (one user, many tenants) via the `memberships` table + `<TenantSwitcher />`.
- **Stuck scan cleanup**: Running/pending scans automatically fail when agent disconnects.
- **Self-hosted Redis on k3s**: One Redis Deployment per `dc-*` namespace (replaces Upstash; the original Upstash decision in `docs/adr/001-upstash-over-redis.md` is now superseded by the homelab move). `redis.ParseURL` handles the in-cluster `redis://…svc.cluster.local:6379` URL unchanged — pubsub code untouched.
- **zot OCI registry, split LAN/public**: `zot.lan.ng20.org` for in-cluster platform image pulls (kaniko pushes here); `zot.ng20.org` (public Cloudflare ingress) for the customer agent image `zot.ng20.org/silkstrand-agent`, since agents run in customer networks and can't reach the LAN registry. Pulls use a per-namespace `zot-pull` dockerconfigjson SealedSecret.
- **CNPG per-namespace database**: CloudNativePG runs one `Cluster` per namespace (instance-per-DC; backoffice gets its own), `storageClass: longhorn`. No row-level security — per-namespace DB isolation is the tenant/DC boundary. App reads `DATABASE_URL` from the CNPG-generated `<cluster>-app` secret's `uri` key.
- **Secrets via OpenBao + ESO**: Source of truth is OpenBao (KV v2 at `secret/silkstrand/*`); ESO `ClusterSecretStore` `openbao-kv` projects per-namespace `ExternalSecret`s into native k8s Secrets, so apps consume plain env vars with zero code change. `secret/silkstrand/shared/tenant-jwt` feeds both the backoffice `TENANT_JWT_SECRET` and each DC's `JWT_SECRET`; `secret/silkstrand/dc/<region>/internal-api-key` is read by the DC ns as `INTERNAL_API_KEY` and by the backoffice as `DC_INTERNAL_API_KEY`.
- **Ingress / outbound-only preserved**: Cloudflare DNS + cloudflared tunnel + traefik + cert-manager front the cluster; agents still connect outbound over WSS/443.
- **First benchmark**: CIS PostgreSQL 16 — 8 controls showcasing the authenticated scan pipeline.

## Branching & Deployment

- No direct commits to `main` — all changes via `feature/` or `fix/` branches with PR
- PR triggers `ci.yml`: lint, test, build verify (runs on the `jtb75-arc` self-hosted ARC runners)
- CI uses path-based filtering (`Detect Changes` paths-filter job): build jobs only run when relevant files change
- Merge to `main` (or `workflow_dispatch`) runs `build-and-push.yml`: kaniko builds → push to `zot.lan.ng20.org` (`:sha-<short>` + `:main-latest`) → bump `newTag` in `jtb75-org/silkstrand-gitops` → **Argo CD auto-syncs** to the cluster. No more GCP stage/prod or git-tag promotion for platform services. Build-once-promote-by-SHA preserved.
- Agent binary cross-compiled and attached to GitHub Release on tag (`release-agent.yml`); customer agent image published to public `zot.ng20.org`
- CI auth: GitHub → zot via `ZOT_USERNAME`/`ZOT_PASSWORD`; GitOps repo writes via `GITOPS_DEPLOY_TOKEN`. WIF/service-account keys gone.
- `destroy.yml` retained to tear down residual (not-yet-decommissioned) GCP infra
- See `docs/cicd.md` for full details

## Environments (k3s namespaces)

One self-hosted k3s cluster; "environments" are namespaces, all Argo CD-managed (no stage/prod GCP projects).

| Namespace | Deploy Trigger | Purpose |
|-----------|----------------|---------|
| `silkstrand-backoffice` | Merge to `main` → CI → gitops bump → Argo sync | Backoffice API + web + its own CNPG cluster (manages all DCs) |
| `dc-us` | Same (Argo CD auto-sync) | DC API + tenant web + Redis + CNPG cluster |
| `dc-*` (future) | Same | Additional data centers; each self-registers via Argo PostSync `register-dc` |

GCP infra is not yet decommissioned (`destroy.yml` available to tear it down).

## Local Development

```bash
# 1. Start dependencies
make dev-deps         # Start Postgres (15432) + Redis (16379) + Backoffice Postgres (15433)

# 2. Seed test data (idempotent, safe to run multiple times)
make seed             # Creates: test tenant, agent (key: test-agent-key), target, bundle, admin user

# 3. Run services
make run              # DC API on port 8080
make run-backoffice   # Backoffice API on port 8081

# 4. Run agent
cd agent && SILKSTRAND_AGENT_ID=00000000-0000-0000-0000-000000000010 \
  SILKSTRAND_AGENT_KEY=test-agent-key \
  SILKSTRAND_BUNDLE_DIR=../bundles \
  go run ./cmd/silkstrand-agent/

# 5. Run frontends
cd web && npm run dev              # Tenant UI on port 5173 (proxies to localhost:8080)
cd backoffice/web && npm run dev   # Backoffice UI on port 5174 (proxies to localhost:8081)

# 6. Generate a dev JWT for API testing
make jwt              # Prints a valid JWT token for curl
# Usage: curl -H "Authorization: Bearer $(make jwt)" localhost:8080/api/v1/targets

# Build & test
make build            # Build DC API binary
make test             # Run DC API tests
make lint             # Run golangci-lint on DC API
make build-backoffice # Build backoffice binary
make test-backoffice  # Run backoffice tests

# Docker (full stack)
docker compose up --build   # Builds and runs all services (API, backoffice, frontends, DBs, Redis)

# Cleanup
make down             # Stop containers
make clean            # Stop containers + delete volumes
```

### Quick E2E Test

```bash
make dev-deps && make seed
# Terminal 1: make run
# Terminal 2: cd agent && SILKSTRAND_AGENT_ID=00000000-0000-0000-0000-000000000010 SILKSTRAND_AGENT_KEY=test-agent-key SILKSTRAND_BUNDLE_DIR=../bundles go run ./cmd/silkstrand-agent/
# Terminal 3:
TOKEN=$(make jwt)
curl -s -X POST localhost:8080/api/v1/scans \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"target_id":"00000000-0000-0000-0000-000000000020","bundle_id":"00000000-0000-0000-0000-000000000030"}'
# Wait 1-2 seconds, then:
curl -s localhost:8080/api/v1/scans/<scan_id> -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

### Ports

| Service | Port | Purpose |
|---------|------|---------|
| DC API | 8080 | Data center API server |
| Backoffice API | 8081 | Backoffice API server |
| Postgres (DC) | 15432 | DC database |
| Postgres (Backoffice) | 15433 | Backoffice database |
| Redis | 16379 | Pub/sub for scan directives |

## Environment Variables

### DC API

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `DATABASE_URL` | `postgres://...localhost:5432/silkstrand` | Postgres connection. In-cluster: from the CNPG `<cluster>-app` secret's `uri` key. |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection. In-cluster: `redis://silkstrand-redis.<ns>.svc.cluster.local:6379` (plain env). |
| `JWT_SECRET` | `dev-secret-change-in-production` | JWT signing key. In-cluster: from OpenBao `secret/silkstrand/shared/tenant-jwt` via ESO. |
| `INTERNAL_API_KEY` | (none) | API key for backoffice access. In-cluster: OpenBao `secret/silkstrand/dc/<region>/internal-api-key` via ESO. |
| `CREDENTIAL_ENCRYPTION_KEY` | (none) | 64 hex chars (32 bytes) for AES-256-GCM. In-cluster: OpenBao `secret/silkstrand/dc/<region>/credential-encryption-key` via ESO. |

### Backoffice API

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8081` | Server port |
| `DATABASE_URL` | `postgres://...localhost:15433/silkstrand_backoffice` | Postgres connection. In-cluster: from the backoffice CNPG `<cluster>-app` secret. |
| `JWT_SECRET` | `dev-secret-change-in-production` | Backoffice **admin** JWT signing key. In-cluster: OpenBao `secret/silkstrand/backoffice/jwt` via ESO. |
| `ENCRYPTION_KEY` | (none) | 64 hex chars for DC API key encryption. In-cluster: OpenBao `secret/silkstrand/backoffice/encryption-key` via ESO. |
| `DC_INTERNAL_API_KEY` | (none) | API key the backoffice uses to call each DC's `/internal/v1/`. In-cluster: OpenBao `secret/silkstrand/dc/<region>/internal-api-key` (same path the DC reads as `INTERNAL_API_KEY`). |
| `TENANT_JWT_SECRET` | `dev-secret-change-in-production` | HS256 signing key for tenant user JWTs. Must match DC's `JWT_SECRET`. In-cluster: OpenBao `secret/silkstrand/shared/tenant-jwt` via ESO. |
| `RESEND_API_KEY` | (none) | Resend transactional email API key. Empty = noop mailer (logs to stdout). In-cluster: OpenBao `secret/silkstrand/backoffice/resend` via ESO. |
| `FROM_EMAIL` | `SilkStrand <noreply@silkstrand.io>` | From address for invites / password resets |
| `TENANT_WEB_URL` | `http://localhost:5173` | Base URL used to build invite / reset links in emails |

### Agent

| Variable | Default | Description |
|----------|---------|-------------|
| `SILKSTRAND_API_URL` | `ws://localhost:8080` | DC API WebSocket URL |
| `SILKSTRAND_AGENT_ID` | (required) | Agent UUID |
| `SILKSTRAND_AGENT_KEY` | (required) | Agent API key |
| `SILKSTRAND_BUNDLE_DIR` | `./bundles` | Local bundle directory |
| `SILKSTRAND_LOG_LEVEL` | `info` | Log level |
| `SILKSTRAND_NAABU_SCAN_TYPE` | (none) | Override naabu scan type (e.g. `c` for connect-scan in unprivileged containers) |
| `SILKSTRAND_SCAN_ALLOWLIST_PATH` | `/etc/silkstrand/scan-allowlist.yaml` | Path to customer scan allowlist file |
| `SILKSTRAND_RUNTIMES_DIR` | (none) | Local directory for recon tool binaries (airgapped/test; skips remote download). NOTE: default download still pulls recon runtimes from `storage.googleapis.com` — not yet migrated off GCS. |

## GitHub Secrets & Variables

CI only needs registry + GitOps credentials now; all **application** secrets live in OpenBao (synced into the cluster by ESO), not in GitHub Actions or GCP Secret Manager.

### Secrets
- `ZOT_USERNAME` — zot registry username (kaniko push from CI)
- `ZOT_PASSWORD` — zot registry password
- `GITOPS_DEPLOY_TOKEN` — write token for the `jtb75-org/silkstrand-gitops` repo (the `Bump GitOps Image Tags` job pushes `newTag` updates)

App secrets (`JWT_SECRET`, `INTERNAL_API_KEY`, `CREDENTIAL_ENCRYPTION_KEY`, backoffice `JWT`/`ENCRYPTION_KEY`/`RESEND_API_KEY`/bootstrap-admin, shared `TENANT_JWT_SECRET`) now live in OpenBao under `secret/silkstrand/*` and reach the cluster via ESO — no longer GitHub Actions secrets. The old GCP/WIF, Upstash, and per-env JWT/credential-key secrets are gone.

### Variables
- (none required for deploy — WIF provider/SA variables removed; deploy is Argo CD GitOps, not GitHub→GCP)

## Database Migrations

### DC API (`api/internal/store/migrations/`)

| Migration | Description |
|-----------|-------------|
| 001_initial | tenants, agents, targets, credentials, bundles, scans, scan_results |
| 002_agent_auth | key_hash, next_key_hash, key_rotated_at on agents |
| 003_tenant_status | status, config on tenants |
| 004_target_credential_unique | unique index on credentials(target_id) |
| 005_install_tokens | install_tokens table for agent bootstrap |
| 006_targets_agent_set_null | targets.agent_id becomes ON DELETE SET NULL |
| 007_scans_agent_set_null | scans.agent_id becomes ON DELETE SET NULL |
| 008_cascade_target_deletes | credentials cascades on target delete |
| 009_target_type_engines | per-engine target type constants (postgresql, mssql, mongodb, mysql, etc.) |
| 010_credential_sources | ADR 004 C0: credential_sources table + targets.credential_source_id, backfill from credentials |
| 011_recon_pipeline | ADR 003 R0: discovered_assets, asset_events (monthly partitioned), asset_sets, correlation_rules, notification_channels, notification_deliveries (monthly partitioned), one_shot_scans + targets.asset_id (backfill) + scans.scan_type/parent_one_shot_id/discovery_scope; scans.target_id made nullable |
| 012_agent_allowlists | ADR 003 D11 follow-up: agent_allowlists table (one row per agent) storing customer allowlist snapshot for UI gating |
| 013_asset_allowlist_status | ADR 003 D11 follow-up: allowlist_status (allowlisted/out_of_policy/unknown) + allowlist_checked_at on discovered_assets |
| 014_drop_legacy_credentials | ADR 004 C0 close-out: drop legacy `credentials` table; `credential_sources` (static) is now the sole credential surface |
| 015_discovery_bundle | Seed global `discovery` bundle row (id `11111111-…`) so scan_type=discovery scans have a valid bundles.id to reference; agent ignores bundle contents for discovery per ADR 003 R1a |
| 016_scan_error_message | `scans.error_message TEXT` — persists the agent-reported failure reason so the UI can render it on the Scan Results page |
| 017_asset_first | ADR 006/007 greenfield: drop `discovered_assets`, `asset_events`, `asset_sets`, `notification_channels`, `notification_deliveries`, `one_shot_scans`, `correlation_rules` (rebuilt), `scan_results`, `scans` (rebuilt); add `assets`, `asset_endpoints`, `collections`, `findings`, `scan_definitions`, `credential_mappings`, new `correlation_rules`, new `scans`; UUID randomness cleanup + guard (dev-seed zero-padded UUIDs rewritten to random v4, discovery bundle `11111111-…` preserved) |
| 018_agent_allowlists | Restore per-agent `agent_allowlists` table (snapshot for `asset_endpoints.allowlist_status` stamping + UI allowlist viewer) after the 017 drop |

### Backoffice (`backoffice/internal/store/migrations/`)

| Migration | Description |
|-----------|-------------|
| 001_initial | data_centers, tenants, admin_users |
| 002_dc_environment | environment column on data_centers (stage/prod) |
| 003_clerk_org | clerk_org_id on tenants (historical — dropped in 007) |
| 004_users_auth | users, memberships, invitations, password_resets (tenant auth) |
| 005_membership_status | status column on memberships (active/suspended) |
| 006_user_status | status column on users (active/suspended) |
| 007_drop_clerk_org_id | remove clerk_org_id column |
| 008_user_display_name | display_name column on users |
| 009_audit_log | audit_log table for backoffice admin actions |
