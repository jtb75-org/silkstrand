# k3s Migration — Account Management Revamp

**Status:** Draft / investigation
**Owners:** kimi (account-mgmt / tenancy), nara (infra / deploy)
**Decision baseline (bigboss, 2026-06-11):**
- Migrate to a **single k3s cluster**.
- Realize each data center as a **"pseudo-DC": its own namespace + its own database**, with the backoffice in its own namespace.
- **Regional data residency relaxed for now** (revisit later by promoting a namespace to a real regional cluster).

This doc covers only the account-management / tenancy / auth surface. Infra (manifests, NetworkPolicies, Postgres topology, secret store, CI) is nara's half; cross-references are marked **[infra]**.

---

## 1. The pseudo-DC model

A "data center" stops being a GCP project and becomes a **namespace** in one k3s cluster, with its own database. The backoffice lives in its own namespace and continues to manage DCs as remote-ish HTTP endpoints — except "remote" now means "another namespace," reachable via in-cluster service DNS instead of a public URL.

```
┌──────────────────────────── one k3s cluster ────────────────────────────┐
│                                                                          │
│  ns: silkstrand-backoffice          ns: dc-us            ns: dc-eu       │
│  ┌───────────────────┐              ┌─────────────┐      ┌─────────────┐ │
│  │ backoffice-api     │── HTTP ───▶ │ dc-api      │      │ dc-api      │ │
│  │ backoffice-web     │  (svc DNS)  │ dc-web      │      │ dc-web      │ │
│  │ backoffice DB      │             │ DC DB (us)  │      │ DC DB (eu)  │ │
│  └───────────────────┘             └─────────────┘      └─────────────┘ │
│         │  NetworkPolicy: backoffice → dc-* :internal only               │
│         (each namespace runs its own CNPG Postgres Cluster — no sharing)  │
└──────────────────────────────────────────────────────────────────────────┘
```

**Why this fits the decision.** The existing codebase assumes DCs are separate, remote, separately-databased services. The pseudo-DC keeps that assumption *true* (separate namespace, separate DB), so the account/tenancy model ports almost unchanged — while a single cluster + relaxed residency keeps ops simple. It is also the **most reversible** path: when residency is re-hardened, a namespace is promoted to its own regional cluster and the backoffice barely notices.

---

## 2. Consequence: keep the backoffice/DC split (Option A)

The earlier draft recommended collapsing the split (Option C) — that was conditional on a **single shared database**. Under pseudo-DC with **db-per-DC**, that reasoning inverts and **Option A (keep the split as-is) is correct**:

| Machinery | Status under pseudo-DC |
|---|---|
| Backoffice→DC HTTP via `dcclient` | **Keep.** Still bridges two real databases; just point at in-cluster service DNS instead of a public URL. |
| Two-phase tenant provisioning (BO DB → DC call → store `dc_tenant_id`) | **Keep.** Still doing genuine cross-database work. |
| Dual identity `bo_tenant_id` / `dc_tenant_id` | **Keep.** Models two real DBs with two PKs. |
| `INTERNAL_API_KEY` / `X-API-Key` on `/internal/v1/` | **Keep, re-scope.** Becomes intra-cluster service identity; **[infra]** prefers mTLS/service-token + NetworkPolicy over a shared secret. |
| Encrypted per-DC API key in `data_centers.api_key_encrypted` | **Optional.** The key is now intra-cluster; can stay encrypted-at-rest for defense-in-depth, or simplify to a service token **[infra]**. |
| Health poller (60s `/readyz` per DC) | **Keep.** Now polls in-cluster services instead of cross-project endpoints. |

Net: the backoffice/DC code is **largely unchanged**. The change is *where it points* (service DNS, k8s secrets), not *how it works*.

---

## 3. Consequence: NO row-level security needed

The scariest part of the shared-DB path is gone. With **db-per-DC**, tenant data stays isolated by the database boundary exactly as today. A missed `WHERE tenant_id` leaks only within one DC's tenant set — the same blast radius we have on GCP, not a platform-wide leak.

- **Keep** today's app-level `tenant_id` filtering (`api/internal/store/postgres.go`). No RLS migration, no schema surgery.
- *Optional hardening, decoupled from this migration:* a lint/test that fails a tenant-scoped query lacking a `tenant_id` predicate. Nice to have; not required by the move.

---

## 4. The "own DB" sub-decision — DECIDED: one CNPG Postgres Cluster per namespace

**Decision (bigboss, 2026-06-11): per-namespace CNPG.** Each `dc-*` namespace and the backoffice namespace runs its **own CloudNativePG `Cluster`** (its own Postgres instance, PVC, and backups) — not a shared instance with logical databases.

Rationale: the homelab platform already runs `cnpg-operator`, and the established convention (the `blue` template) is one CNPG `Cluster` per app-namespace. That makes per-namespace Postgres cheap and self-healing, so the "operate N instances" cost that would otherwise argue for a shared instance is largely absorbed by the operator. The payoff is the **strongest** tenant-data isolation — instance-level, not just logical — which makes §3 (no RLS) and the promote-a-namespace-to-a-cluster reversibility story even cleaner.

Each `Cluster` is declared in its namespace overlay as a `db-cluster.yaml` (CNPG `postgresql.cnpg.io/v1`), `storageClass: longhorn`, with `initdb` creating that DC's database + owner. `DATABASE_URL` for each component points at its namespace-local CNPG service. **[infra: nara owns the manifests.]**

> The app needs **zero code change** for this — it already connects per-database via `DATABASE_URL`. The only difference from the GCP model is N managed instances instead of one Cloud SQL instance with two databases.

---

## 5. What pseudo-DC does NOT give (call out, don't oversell)

- **Not true data residency.** One cluster = one control plane / etcd / node pool. EU-namespace data physically lives wherever the cluster's nodes are. Do not market "EU-isolated" until `dc-eu` is a real regional cluster.
- **Not a hard security boundary.** Namespaces need **NetworkPolicies** to stop cross-namespace traffic **[infra]**; a cluster-admin or container escape still crosses them. Stronger than shared-DB-same-namespace, weaker than separate GCP projects.
- **Shared key domains by default** — see §7.

---

## 6. Auth surfaces — impact summary

| Surface | Mechanism today | Pseudo-DC impact |
|---|---|---|
| **Tenant users** | bcrypt + HS256 JWT signed by backoffice with `TENANT_JWT_SECRET`, validated by every DC; iss `silkstrand-backoffice` / aud `silkstrand-tenant-api` | **Unchanged logic.** Secret moves Secret Manager → k8s secret **[infra]**. Backoffice signs once; each DC namespace validates with the same secret. |
| **Admin** | bcrypt + HS256 JWT, separate `JWT_SECRET`, roles viewer/admin/super_admin | Unchanged. Secret → k8s. |
| **Agent identity** | per-agent 256-bit key (SHA-256 + dual rotation), install tokens, WSS bearer | **Unaffected** — agents are customer-side over WSS/443. `deploy/kubernetes/silkstrand-agent.yaml` only needs `image:` repointed off Artifact Registry + the DC API URL set **[infra]**. |
| **Backoffice→DC** | `INTERNAL_API_KEY` `X-API-Key`; DC key AES-encrypted in BO DB | Re-scope to intra-cluster service identity (mTLS/token + NetworkPolicy) **[infra]**. Logic stays. |

---

## 7. Shared key domains (relaxed-residency caveat)

`TENANT_JWT_SECRET` is shared across all DCs by design; `CREDENTIAL_ENCRYPTION_KEY` is per-DC. Both are now sourced from **OpenBao via External Secrets Operator (ESO)** — see §7a. ESO syncs OpenBao KV into native k8s `Secret`s, so the app reads env exactly as before (zero app awareness).

- Mark the residency-sensitive keys (`CREDENTIAL_ENCRYPTION_KEY`, `TENANT_JWT_SECRET`) in code comments now; they're what must re-isolate when namespaces become clusters.
- Confirm the `credential.fetch` slog audit event carries `tenant_id` so cross-tenant access is detectable.

## 7a. Secret store: OpenBao + ESO (DECIDED)

**Decision (2026-06-11): use the cluster's existing OpenBao + ESO, not sealed-secrets, for silkstrand's secrets.** Verified live: `openbao-0` + `openbao-unsealer-0` Running (transit auto-unseal — no manual unseal burden), ESO operator up, `ClusterSecretStore openbao-kv` is `Valid`/`Ready`. KV v2 mounted at `secret/`, ESO authenticates via k8s ServiceAccount.

Why OpenBao here (it reverses the earlier "Vault is overkill" note): the operational tier-0 cost is **already paid** (installed, bootstrapped, auto-unsealed, part of the platform), and our matrix has **cross-namespace shared values** — exactly what a central KV solves and what sealed-secrets handles badly (a SealedSecret is sealed per namespace+name, so a shared value must be re-sealed N times and rotated N times).

Model — all silkstrand secrets live in OpenBao KV under `secret/silkstrand/...`; each consuming namespace gets an `ExternalSecret` that ESO syncs to a local `Secret`:

| OpenBao KV path | Consumed by (ExternalSecret in…) | Notes |
|---|---|---|
| `secret/silkstrand/shared/tenant-jwt` | backoffice **and every `dc-*`** | One source of truth; backoffice signs, DCs validate. Rotate once. |
| `secret/silkstrand/dc/<region>/internal-api-key` | the `dc-*` ns **and** backoffice | **Kills the dc→key map sync** — both sides read the same path. |
| `secret/silkstrand/dc/<region>/credential-encryption-key` | the `dc-*` ns only | Per-DC key domain (residency-sensitive). |
| `secret/silkstrand/backoffice/{jwt,encryption-key,resend,bootstrap-admin}` | backoffice ns only | Admin JWT, DC-api-key encryption, email, seed admin. |

`DATABASE_URL` stays **CNPG-generated** in each namespace (the operator owns it) — not in OpenBao. Sealed-secrets remains only for the one OpenBao bridge secret that already exists; silkstrand adds no new sealed secrets.

Roadmap tie-in (now zero marginal infra cost): OpenBao **transit** can later replace the app-side AES `CREDENTIAL_ENCRYPTION_KEY` (this *is* the ADR 004 C1+ `vault` credential-resolver slot), and the **DB secrets engine** can issue dynamic per-DC CNPG creds.

---

## 8. Migration-sensitive code inventory (account-mgmt half)

Mostly **repoint**, not rewrite:

- `backoffice/internal/dcclient/client.go` — point at in-cluster service DNS; auth → service token **[with infra]**
- `backoffice/internal/handler/datacenter.go` — DC registration; `api_url` becomes a cluster service name; key handling per §2
- `backoffice/internal/handler/tenant.go` — two-phase provisioning unchanged in logic
- `api/internal/middleware/internal.go` — `INTERNAL_API_KEY` re-scoped to intra-cluster
- `api/internal/config` + `backoffice/internal/config` — `DATABASE_URL`, `REDIS_URL`, secrets now from k8s **[infra]**
- `deploy/kubernetes/silkstrand-agent.yaml` — image repoint + DC URL **[infra]**
- Storage URLs (`AGENT_RELEASES_URL`, `SILKSTRAND_RUNTIMES_BASE_URL`, `BUNDLE_GCS_BUCKET`) — off `storage.googleapis.com` **[infra]**

No RLS migration. No tenant-identity schema change.

---

## 9. Next steps

1. ✅ **DECIDED** Postgres topology: one CNPG `Cluster` per namespace (§4).
2. **[infra, nara]** Per-namespace CNPG `db-cluster.yaml` overlays (storageClass longhorn, initdb per DC).
3. **[infra, nara]** NetworkPolicies for namespace isolation; backoffice→DC service identity (mTLS/token).
4. **[infra, nara]** Per-namespace `CREDENTIAL_ENCRYPTION_KEY` sealed secret (§7).
5. **[account-mgmt, kimi]** Repoint `dcclient` + DC registration to in-cluster service DNS (started); confirm two-phase provisioning works namespace-to-namespace.
6. **[account-mgmt, kimi]** DC-registration bootstrap Job (seed `data_centers` rows so each `dc-*` self-registers) — `migrate-job.yaml` pattern from the blue template.
7. **[account-mgmt, kimi]** Optional: tenant-scoped-query lint (§3), decoupled from the migration.
