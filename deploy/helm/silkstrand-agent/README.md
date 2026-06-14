# silkstrand-agent (Helm chart)

Deploy a SilkStrand edge agent on Kubernetes. The agent self-bootstraps from an
install token, persists its identity across restarts (so a bounced pod reconnects
as the same agent and the server resumes its in-flight chunk), and ships with a
scoped egress NetworkPolicy.

This chart is the productionized form of the manifest validated end-to-end in
[ADR 016](../../../docs/adr/016-agent-pooling-lease-dispatch.md) work.

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
| `replicaCount` | `1` | **Keep at 1.** Multi-agent pools are ADR 016 (per-member identity + pool-join token), not this chart. |

## Caveats / known follow-ons
- **`in_container` detection**: a k8s-deployed agent currently reports
  `in_container=false` (detection is Docker-`/.dockerenv`-specific). Tracked
  separately; affects the "Recreate vs Upgrade" UX, not scanning.
- **`replicaCount > 1`**: not supported here (single RWO creds + single identity).
  Horizontal pools require ADR 016 (lease-based claim + pool-join enrollment).
- **`hostNetwork`**: off by default. Pod-network SNAT reaches the LAN for
  connect-scan; enable host networking only if you must scan the node's directly
  attached segment (note the kube-proxy/ClusterIP + DNS caveats).
