#!/usr/bin/env bash
# Spin up the scan-lab targets. Run with kubectl context pointed at the k3s cluster.
# Targets are deliberately insecure + LAN-exposed via kube-vip — see README/GROUND_TRUTH.
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f 00-namespace.yaml
kubectl apply -f 10-postgres-16.yaml -f 11-mongodb-8.yaml -f 12-mssql-2022.yaml
echo "Applied. Waiting for LoadBalancer IPs..."
kubectl get svc -n scan-lab -o wide
echo
echo "NEXT: 1) add the IPs to the agent scan-allowlist (allowlist-snippet.yaml)"
echo "      2) create + map credential_sources in the tenant (README step 3)"
echo "      3) run discovery + CIS scans; compare to GROUND_TRUTH.md"
