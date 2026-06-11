# k3s Migration - Infrastructure and Deployment

**Status:** Draft / investigation
**Owners:** nara (infra / deploy), kimi (account-mgmt / tenancy)
**Decision baseline (bigboss, 2026-06-11):**
- Run SilkStrand on **one k3s cluster**.
- Model each data center as a **pseudo-DC namespace** with its own CNPG-backed database.
- Keep **backoffice** in its own namespace.
- Relax regional data residency for now; preserve a path to promote a pseudo-DC namespace to a real regional cluster later.
- Onboard onto the existing homelab GitOps platform instead of building new cluster services.

This doc covers the infra side: Kubernetes manifests, service topology, database and Redis placement, secrets, NetworkPolicies, image/storage migration, and CI/CD. Account-management behavior lives in [k3s-account-mgmt.md](./k3s-account-mgmt.md).

---

## 0. Existing platform contract

The target cluster already runs the platform pieces this migration needs:

- Argo CD app-of-apps from `~/repo/argocd-apps`.
- `zot` registry with public and LAN ingress: `zot.ng20.org` for customer pulls, `zot.lan.ng20.org` for in-cluster pulls.
- CloudNativePG operator.
- OpenBao + External Secrets Operator, with `ClusterSecretStore/openbao-kv`.
- MinIO/S3 at `s3.ng20.org`.
- Traefik, cert-manager, and cloudflared tunnel ingress.
- Longhorn persistent volumes.
- `jtb75-arc` self-hosted GitHub Actions runners.
- Observability stack.

SilkStrand should follow the `blue-gitops`/`hillco2-gitops` convention:

1. Source CI runs on `jtb75-arc`.
2. Kaniko builds images and pushes them to zot.
3. CI updates quoted `newTag` values in the GitOps overlay.
4. Argo CD reconciles the overlay into the cluster.

Create a dedicated `~/repo/silkstrand-gitops` repo and register it from `~/repo/argocd-apps/applications/silkstrand.yaml`.

---

## 1. Target topology

```
one k3s cluster
├── ns/silkstrand-system
├── ns/silkstrand-backoffice
│   ├── backoffice-api
│   └── backoffice-web
├── ns/dc-us
│   ├── cnpg cluster
│   ├── silkstrand-api
│   ├── silkstrand-web
│   └── redis
└── ns/dc-*
    ├── cnpg cluster
    ├── silkstrand-api
    ├── silkstrand-web
    └── redis
```

Backoffice stores each DC API URL as data, using in-cluster service DNS:

```text
http://silkstrand-api.dc-us.svc.cluster.local:8080
```

The app remains shaped like today's Cloud Run deployment: backoffice and each DC are separate HTTP services with separate databases. The Kubernetes change is where those services live and how they are addressed.

---

## 2. Database topology

Use **one CloudNativePG Cluster per namespace**.

This reverses the earlier shared-Postgres/logical-DB recommendation. The change is driven by the existing homelab platform: the `blue-gitops` template already runs one CNPG Cluster in the app namespace, backed by Longhorn and optionally MinIO backups. Following that convention is cleaner than introducing a cross-namespace shared Postgres pattern just for SilkStrand.

Recommended clusters:

- `silkstrand-backoffice/silkstrand-backoffice-pg`
- `dc-us/silkstrand-api-pg`
- `dc-*/silkstrand-api-pg`

Benefits:

- Matches the established template.
- Keeps the pseudo-DC database boundary as a real stateful boundary.
- Avoids cross-namespace DB credentials and NetworkPolicies for a shared DB service.
- Keeps the future promotion path simple: a namespace's app + DB can move together.

Tradeoff:

- More CNPG clusters, PVCs, and backup objects than a single shared Postgres service.

Use CNPG-generated app secrets to compose `DATABASE_URL` in each namespace, following the `blue` pattern. SilkStrand's URL scheme should remain `postgres://`:

```text
postgres://$(DB_USER):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/$(DB_NAME)?sslmode=disable
```

Use `sslmode=require` only if the CNPG service is configured for TLS and the app image has the needed trust material.

---

## 3. Redis topology

Run **one Redis service per DC namespace**, not per API pod.

Reason: Redis pub/sub routes scan directives and result/progress events across multiple API replicas. A single Redis per DC keeps all replicas in that DC on the same bus while preserving pseudo-DC separation.

Example:

```text
REDIS_URL=redis://redis.dc-us.svc.cluster.local:6379
```

This is an infra-only migration. The Go Redis client already accepts both `redis://` and `rediss://`, so moving from Upstash to in-cluster Redis does not require app code changes.

Backoffice does not use Redis. Do not deploy Redis or set `REDIS_URL` in `silkstrand-backoffice`; it needs only its own CNPG-backed database plus outbound HTTP to each DC's `/internal` endpoints. Do not share a global Redis across all DCs for agent routing.

---

## 4. Secrets

Baseline secret store: **OpenBao + External Secrets Operator**.

The cluster already has OpenBao, ESO, and `ClusterSecretStore/openbao-kv` live. SilkStrand should add `ExternalSecret` manifests only; the actual values are written out of band to OpenBao KV v2 under `secret/silkstrand/...`.

Do not add SilkStrand SealedSecrets. Sealed Secrets remains only for existing platform bridge material such as OpenBao's transit token. OpenBao is the right fit here because SilkStrand has shared cross-namespace values that should rotate once from a central source.

| Secret | Scope | Notes |
|---|---|---|
| `TENANT_JWT_SECRET` | `silkstrand-backoffice` and every `dc-*` namespace | One OpenBao source: `secret/silkstrand/shared/tenant-jwt`. Backoffice signs tenant JWTs; every DC validates. In DC namespaces expose it as `JWT_SECRET` to preserve existing API env naming. |
| `INTERNAL_API_KEY` / service token | target `dc-*` namespace and `silkstrand-backoffice` | Per-DC OpenBao source: `secret/silkstrand/dc/<region>/internal-api-key`. Both the DC API and backoffice read the same path through namespace-local `ExternalSecret` resources. No backoffice-side DC-key map sync is needed. |
| `JWT_SECRET` | `silkstrand-backoffice` only | Backoffice admin JWT secret from `secret/silkstrand/backoffice/jwt`. Distinct from `TENANT_JWT_SECRET`. |
| `ENCRYPTION_KEY` | `silkstrand-backoffice` only | Backoffice encryption key from `secret/silkstrand/backoffice/encryption-key`. Do not conflate with `CREDENTIAL_ENCRYPTION_KEY`. |
| `RESEND_API_KEY` | `silkstrand-backoffice` only | Transactional email from `secret/silkstrand/backoffice/resend`. Optional where the app allows noop mailer behavior. |
| `BOOTSTRAP_ADMIN_PASSWORD` | `silkstrand-backoffice` only | Optional bootstrap credential from `secret/silkstrand/backoffice/bootstrap-admin`. |
| `CREDENTIAL_ENCRYPTION_KEY` | each `dc-*` namespace only | Per-DC value from `secret/silkstrand/dc/<region>/credential-encryption-key`. Preserves per-DC credential key domain. |
| `DATABASE_URL` | each app namespace | Composed from that namespace's CNPG app secret. |
| `REDIS_URL` | each `dc-*` namespace | Points to that namespace's Redis service. |

Key rotation should be namespace-scoped for `CREDENTIAL_ENCRYPTION_KEY`; rotating one DC must not require re-encrypting every DC.

ExternalSecret manifests should reference the existing cluster store:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: silkstrand-tenant-jwt
  namespace: dc-us
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: openbao-kv
    kind: ClusterSecretStore
  target:
    name: silkstrand-tenant-jwt
    creationPolicy: Owner
  data:
    - secretKey: JWT_SECRET
      remoteRef:
        key: silkstrand/shared/tenant-jwt
        property: value
```

---

## 5. Backoffice to DC auth

Keep the HTTP boundary and `/internal/v1/` route shape, but change trust assumptions:

- Use in-cluster service DNS for the DC URL.
- Use NetworkPolicies to allow backoffice-to-DC internal traffic and deny unrelated cross-namespace traffic.
- Use a service token as the compatibility baseline for `X-API-Key`.
- Prefer mTLS if the chosen ingress/service-mesh stack already provides it without a large operational dependency.

The migration can keep `INTERNAL_API_KEY` as the app-level compatibility mechanism while renaming the operational concept to "service token" and scoping it per DC.

---

## 6. NetworkPolicies

Every application namespace should default-deny ingress. Add explicit allows:

- Public ingress controller -> `silkstrand-web` / `silkstrand-api` public ports as needed.
- `silkstrand-backoffice/backoffice-api` -> `dc-*/silkstrand-api` on port `8080` for `/internal/v1/` and health checks.
- `dc-*` API pods -> same-namespace Redis on `6379`.
- App pods -> same-namespace CNPG service on `5432`.
- Backoffice web -> backoffice API, if served as separate in-cluster services.

Kubernetes NetworkPolicy cannot restrict HTTP paths such as `/internal` by itself. Path-level enforcement belongs in the application, ingress, or service mesh. NetworkPolicy should enforce the namespace and port boundary.

Also add egress policies where practical:

- DC API egress to same-namespace Postgres, same-namespace Redis, MinIO/S3 endpoint, and required outbound HTTPS.
- Backoffice egress to same-namespace Postgres, DC API service DNS, mail provider, and required outbound HTTPS.

---

## 7. Object storage and static assets

Current GCP storage dependencies:

- `BUNDLE_GCS_BUCKET`
- `AGENT_RELEASES_URL`
- `SILKSTRAND_RUNTIMES_BASE_URL` / runtime artifact URLs, where configured
- public `storage.googleapis.com` URLs in release and install flows

Use two artifact classes:

1. **Platform-internal artifacts:** use the existing MinIO/S3 platform.
2. **Customer-agent artifacts:** use a public HTTPS artifact store.

Platform-internal artifacts include compliance bundles uploaded/served by the DC API. The API already supports local-only bundle mode via `BUNDLE_STORAGE_PATH` and `BUNDLE_CONTROLS_DIR`; production still needs durable object storage for uploaded bundles. If `BUNDLE_GCS_BUCKET` remains GCS-specific in code, add a MinIO/S3-compatible env contract before cutting over production bundle storage.

Customer-agent artifacts cannot move to LAN-only MinIO because agents run in customer environments. These must stay public:

- Agent self-update binaries: `AGENT_RELEASES_URL` / `SILKSTRAND_AGENT_RELEASES_URL`, currently defaulting to `https://storage.googleapis.com/silkstrand-agent-releases`.
- Recon runtime tools: `SILKSTRAND_RUNTIMES_BASE_URL`, currently defaulting to `https://storage.googleapis.com/silkstrand-runtimes`.
- Curated `nuclei-templates` tarballs, fetched from the same runtimes base URL.

Recommended public artifact target: **Cloudflare R2** behind stable public HTTPS hostnames. A cloudflared-exposed MinIO bucket is acceptable only if it is deliberately made public, stable, and suitable for customer agents. Do not point agent defaults or GitOps env vars at LAN-only MinIO.

Example public URLs:

```text
AGENT_RELEASES_URL=https://downloads.silkstrand.io/agent
SILKSTRAND_RUNTIMES_BASE_URL=https://downloads.silkstrand.io/runtimes
```

---

## 8. Images and registry

Move runtime images off GCP Artifact Registry and into the existing `zot` registry.

Recommended baseline:

- `zot.lan.ng20.org/silkstrand-api`
- `zot.lan.ng20.org/silkstrand-web`
- `zot.lan.ng20.org/silkstrand-backoffice-api`
- `zot.lan.ng20.org/silkstrand-backoffice-web`
- `zot.ng20.org/silkstrand-agent`
- `imagePullSecrets` using platform-provided zot pull credentials or an ESO-managed registry secret.
- Immutable tags from the short commit SHA, plus `main-latest` as a convenience tag.

Images to publish to zot:

- `silkstrand-api`
- `silkstrand-web`
- `backoffice-api`
- `backoffice-web`
- `silkstrand-agent`

The same zot registry has two hostnames. Use the LAN hostname for Argo-managed platform images and the public hostname for customer-side agent installs:

- Platform pulls: `zot.lan.ng20.org/...`
- Customer agent pulls: `zot.ng20.org/silkstrand-agent`

The existing customer-side agent manifest at `deploy/kubernetes/silkstrand-agent.yaml` is not a platform manifest. It must be pullable from customer environments, so it should use `zot.ng20.org/silkstrand-agent`, not the LAN hostname.

Recommended customer-agent distribution:

- `image:` repointed away from Artifact Registry to `zot.ng20.org/silkstrand-agent`.
- `api-url` examples changed away from Cloud Run to the chosen public DC API URL.

---

## 9. Workloads and probes

Map Cloud Run services to Kubernetes Deployments:

| Cloud Run service | Kubernetes target |
|---|---|
| `silkstrand-api` | Deployment + Service in each `dc-*` namespace |
| tenant web | Deployment + Service in each `dc-*` namespace |
| `backoffice-api` | Deployment + Service in `silkstrand-backoffice` |
| backoffice web | Deployment + Service in `silkstrand-backoffice` |

Carry over these runtime expectations:

- API container port: `8080`
- Backoffice API container port: `8081`
- Web containers: `80`
- API long-lived WebSocket support requires ingress/proxy timeout settings longer than the default HTTP timeout.
- Health checks should use existing `/healthz` for liveness/startup and `/readyz` where external readiness is needed.

Set resource requests/limits explicitly; Cloud Run's CPU-idle behavior does not map directly to Kubernetes.

---

## 10. Ingress and DNS

Use the existing Traefik + cert-manager + cloudflared ingress path. There is no inbound firewall dependency; public access flows through the tunnel.

Public routes:

- Tenant web per DC: `https://<dc-or-tenant-host>/`
- Tenant API per DC: `https://<dc-api-host>/`
- Backoffice web/API: `https://backoffice.<domain>` and API route as needed.

Internal DC URLs stored in backoffice remain service DNS, not public ingress URLs:

```text
http://silkstrand-api.dc-us.svc.cluster.local:8080
```

Use the existing cert-manager/Cloudflare certificate automation. The current Terraform Cloudflare DNS records should be replaced by Argo-managed ingress/tunnel configuration or a smaller DNS-only Terraform layer if an external record still has to be managed outside the cluster.

---

## 11. CI/CD

Replace the current GCP deploy path:

- Remove Workload Identity Federation and Artifact Registry auth from runtime image publishing.
- Publish images to zot. Platform image references use `zot.lan.ng20.org`; customer agent image references use `zot.ng20.org`.
- Replace Terraform Cloud Run deploy with GitOps tag bumps consumed by Argo CD.
- Replace GCS Terraform backend if Terraform remains only for DNS/external storage.

Preferred deployment shape:

- `~/repo/silkstrand-gitops/base/components`: reusable Kustomize components for API, web, Redis, CNPG, NetworkPolicies, and shared labels.
- `~/repo/silkstrand-gitops/apps/backoffice/overlays/onprem`: backoffice namespace, API/web, CNPG, `ExternalSecret` resources, ingress.
- `~/repo/silkstrand-gitops/apps/dc-us/overlays/onprem`: DC namespace, API/web, Redis, CNPG, `ExternalSecret` resources, ingress.
- Additional `apps/dc-*/overlays/onprem` directories for more pseudo-DCs.
- `~/repo/argocd-apps/applications/silkstrand.yaml`: parent Argo app or app-of-apps registration.

Because SilkStrand has four Argo-managed platform images, source CI should use path filters and build only changed components, then update the corresponding quoted `newTag` entries in the GitOps repo:

- `api/**` -> `zot.lan.ng20.org/silkstrand-api`
- `web/**` -> `zot.lan.ng20.org/silkstrand-web`
- `backoffice/**` excluding `backoffice/web/**` -> `zot.lan.ng20.org/silkstrand-backoffice-api`
- `backoffice/web/**` -> `zot.lan.ng20.org/silkstrand-backoffice-web`

Agent image publishing is a separate release/distribution path because it is not Argo-managed platform state. It still publishes to the same zot registry, but the customer-facing reference is `zot.ng20.org/silkstrand-agent`; the GitOps overlay should not depend on the customer agent image.

Do not put plaintext production secrets into GitHub Actions variables or GitOps. CI should apply `ExternalSecret` manifests only; the values live in OpenBao and are written with `bao kv put`.

---

## 12. Migration sequence

1. Create `~/repo/silkstrand-gitops` from the existing GitOps conventions.
2. Add backoffice and one pilot DC overlay with CNPG, Deployments, Services, Redis for the DC, ExternalSecrets, ingress, and NetworkPolicies.
3. Register SilkStrand in `~/repo/argocd-apps/applications/silkstrand.yaml`.
4. Port CI to `jtb75-arc` + Kaniko + zot + GitOps `newTag` bump.
5. Move platform bundle storage to MinIO/S3; move agent releases, recon runtimes, and nuclei templates to the public artifact host and update URL env vars.
6. Run the backoffice-side DC-registration bootstrap job to insert DC records with service-DNS `api_url` values and encrypted per-DC service tokens.
7. Repoint the customer-agent manifest image and public DC API URL examples.
8. Add more `dc-*` namespaces by repeating CNPG, Redis, secrets, NetworkPolicy, ingress, and registration bootstrap.

The DC-registration job writes backoffice DB rows and can run before DC API pods are Ready. The health poller will mark DCs active after each `dc-api` answers `/readyz`. Tenant provisioning is runtime work and still requires the target DC API to be up.

---

## 13. Open decisions

- Exact `silkstrand-gitops` app structure: one parent app pointing at all overlays vs separate Argo Applications for backoffice and each DC.
- Whether MinIO/S3 compatibility needs app code before production bundle uploads can leave GCS.
- Exact public object-storage path for customer-agent HTTP artifacts.
- Whether mTLS is provided by a mesh or deferred in favor of service tokens plus NetworkPolicies.
