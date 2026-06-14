# silkstrand-agent (Helm chart)

Deploy a SilkStrand edge agent on Kubernetes. The agent self-bootstraps from an
install token, persists its identity across restarts (so a bounced pod reconnects
as the same agent and the server resumes its in-flight chunk), and ships with a
scoped egress NetworkPolicy.

This chart is the productionized form of the manifest validated end-to-end in
the ADR 016 (agent pooling + lease dispatch, proposed — #385) work.

## Install

```bash
helm install lan-agent ./deploy/helm/silkstrand-agent \
  --namespace agents --create-namespace \
  --set auth.installToken=sst_xxxxxxxx \
  --set apiUrl=wss://api.silkstrand.io \
  --set 'allowlist.cidrs={192.168.0.0/24}' \
  --set 'imagePullSecrets[0].name=zot-pull'
```

Mint the install token from the tenant UI (Agents → Add Agent) or the API.

## Key values

| Key | Default | Notes |
|---|---|---|
| `auth.installToken` | `""` | Single-use token → self-bootstrap. Or set `auth.agentId`+`auth.agentKey`, or `auth.existingSecret`. |
| `apiUrl` | `wss://api.silkstrand.io` | DC API WebSocket (front door). |
| `allowlist.cidrs` | `[192.168.0.0/24]` | The agent scans **only** these. Also seeded into the egress policy. |
| `persistence.enabled` | `true` | Creds PVC → identity survives restarts (resume). Disable only for ephemeral agents with a reusable token. |
| `networkPolicy.enabled` | `true` | Egress allow: internet `:443` (Cloudflare WSS + recon download) + LAN scan CIDRs + DNS. Needed in default-deny-egress namespaces. |
| `imagePullSecrets` | `[]` | The agent registry needs auth (#369). |
| `resources` | `req 100m/256Mi · lim 1/1Gi` | Sized from measured usage (nuclei bursts to ~0.5 core/~450Mi). |
| `replicaCount` | `1` | **Keep at 1.** Multi-agent pools are ADR 016 (proposed) (per-member identity + pool-join token), not this chart. |

## NetworkPolicy boundary (read before relying on it)
- The built-in egress allows `0.0.0.0/0:443` (Cloudflare WSS + `downloads.silkstrand.io`) + the scan CIDRs + DNS. FQDN egress isn't expressible in standard NetworkPolicy and Cloudflare's ranges change, so broad `:443` is intentional.
- **It does NOT firewall image pulls** — those are kubelet/node egress. `imagePullSecrets` handles pull *auth*, not node egress.
- **HTTPS proxy / custom CA / NTP**: express via `networkPolicy.extraEgress` (the default only opens `:443`).
- **`hostNetwork=true`**: some CNIs don't apply pod NetworkPolicy to host-network pods — the egress policy may be bypassed.

## Caveats / known follow-ons
- **`in_container` detection**: as of #390 the agent detects k8s pods
  (`KUBERNETES_SERVICE_HOST`, plus podman `/run/.containerenv` and cgroup-v2
  signals), so a freshly built agent correctly reports `in_container=true` in a
  pod. Agents running a build older than #390 still report `in_container=false`
  until upgraded — a live caveat for already-deployed agents, not a code
  limitation. Affects the "Recreate vs Upgrade" UX, not scanning.
- **`replicaCount > 1`**: hard-rejected at template time (single RWO creds +
  single identity). Horizontal pools require ADR 016 (proposed) (lease-based
  claim + pool-join enrollment).
- **Identity source**: with `auth.installToken`, the **PVC** holds the
  bootstrapped identity (resume across restarts). With `auth.agentId`+`agentKey`,
  the **env** is the identity and the PVC only caches runtime state.
- **`hostNetwork`**: off by default. Pod-network SNAT reaches the LAN for
  connect-scan; enable host networking only if you must scan the node's directly
  attached segment (note the kube-proxy/ClusterIP + DNS caveats).
- **Allowlist is CIDR-only** in this chart (the agent allowlist also supports
  IP/range/hostname; not yet surfaced as Helm values).
