#!/usr/bin/env bash
# Remove the scan-lab namespace and everything in it.
set -euo pipefail
kubectl delete namespace scan-lab --ignore-not-found
echo "scan-lab torn down. Remember to remove its IPs from the agent scan-allowlist."
