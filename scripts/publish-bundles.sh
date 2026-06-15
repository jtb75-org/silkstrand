#!/usr/bin/env sh
# Usage: scripts/publish-bundles.sh [bundle-name...]
# Default bundles: cis-postgresql-16 cis-mssql-2022 cis-mongodb-8
#
# For each bundle: build the tarball, write a sibling .sha256, upload both to the
# public MinIO host via `mc` (same bucket that serves agent binaries — see
# release-agent.yml), then register the bundle with the DC API so it computes
# bundles.gcs_path (Part A: BUNDLE_PUBLIC_BASE_URL). The off-cluster agent then
# fetches <base>/<name>-<version>.tar.gz (+ .sha256) and verifies the digest.
#
# Required env:
#   MINIO_ACCESS_KEY / MINIO_SECRET_KEY  MinIO creds (same as release-agent.yml)
#   API_URL                              DC API base, e.g. https://app.silkstrand.io
#   JWT                                  tenant JWT for POST /api/v1/bundles/upload
# Optional env (defaults match the homelab + Part A):
#   S3_ENDPOINT            default https://s3.ng20.org
#   BUNDLE_BUCKET          default silkstrand-agent-releases
#   BUNDLE_PREFIX          default bundles/   (object-key prefix within the bucket)
#   BUNDLE_PUBLIC_BASE_URL default https://downloads.silkstrand.io/agent/bundles
#                          (must equal the DC API's BUNDLE_PUBLIC_BASE_URL so the
#                           uploaded key and the registered gcs_path line up)
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

S3_ENDPOINT="${S3_ENDPOINT:-https://s3.ng20.org}"
BUNDLE_BUCKET="${BUNDLE_BUCKET:-silkstrand-agent-releases}"
BUNDLE_PREFIX="${BUNDLE_PREFIX:-bundles/}"
BUNDLE_PUBLIC_BASE_URL="${BUNDLE_PUBLIC_BASE_URL:-https://downloads.silkstrand.io/agent/bundles}"
API_URL="${API_URL:-}"
JWT="${JWT:-}"

DEFAULT_BUNDLES="cis-postgresql-16 cis-mssql-2022 cis-mongodb-8"
if [ "$#" -gt 0 ]; then
  BUNDLES="$*"
else
  BUNDLES="$DEFAULT_BUNDLES"
fi

# --- preflight ---------------------------------------------------------------
fail() { echo "Error: $1" >&2; exit 1; }

command -v mc >/dev/null 2>&1 || fail "mc (MinIO client) not found on PATH. Install: https://dl.min.io/client/mc/release/"
command -v curl >/dev/null 2>&1 || fail "curl not found on PATH"
[ -n "${MINIO_ACCESS_KEY:-}" ] || fail "MINIO_ACCESS_KEY is required"
[ -n "${MINIO_SECRET_KEY:-}" ] || fail "MINIO_SECRET_KEY is required"
[ -n "$API_URL" ] || fail "API_URL is required (DC API base URL)"
[ -n "$JWT" ] || fail "JWT is required (tenant JWT for the upload endpoint)"

# sha256 helper — sha256sum (Linux/CI) or shasum -a 256 (macOS).
sha256hex() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

mc alias set silkstrand "$S3_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null

# --- publish each bundle -----------------------------------------------------
for name in $BUNDLES; do
  manifest="$ROOT_DIR/bundles/$name/bundle.yaml"
  [ -f "$manifest" ] || fail "no bundle.yaml for $name ($manifest)"
  version=$(sed -n 's/^version:[[:space:]]*//p' "$manifest" | tr -d '[:space:]')
  [ -n "$version" ] || fail "could not parse version from $manifest"

  echo "==> $name $version"

  # 1. Build the tarball.
  "$SCRIPT_DIR/build-bundle.sh" "$name"
  tarball="$ROOT_DIR/dist/$name-$version.tar.gz"
  [ -f "$tarball" ] || fail "build did not produce $tarball"

  # 2. Sibling .sha256 — just the 64-hex digest (matches cache.go's
  #    strings.Fields(...)[0] parse).
  digest=$(sha256hex "$tarball")
  printf '%s\n' "$digest" > "$tarball.sha256"

  # 3. Upload tarball + .sha256 to the public host. no-cache so the Cloudflare
  #    edge serves a re-published tarball immediately (same as release-agent.yml).
  obj="silkstrand/${BUNDLE_BUCKET}/${BUNDLE_PREFIX}${name}-${version}.tar.gz"
  mc cp --attr "Cache-Control=no-cache,max-age=0" "$tarball" "$obj"
  mc cp --attr "Cache-Control=no-cache,max-age=0" "$tarball.sha256" "${obj}.sha256"

  # 4. Register with the DC API → upserts the bundle row + controls and sets
  #    gcs_path from BUNDLE_PUBLIC_BASE_URL (Part A). curl's default UA is fine
  #    (the Cloudflare WAF block applies to urllib's default UA, not curl).
  #    slug=$name = the directory name = the mc object key, so the API registers
  #    gcs_path against the reachable slug path, not bundle.yaml's display name
  #    (which has spaces and would 404).
  resp=$(curl -fsS -X POST "${API_URL%/}/api/v1/bundles/upload" \
    -H "Authorization: Bearer $JWT" \
    -F "tarball=@$tarball" \
    -F "slug=$name")

  # 5. Report id / version / gcs_path / control_count from the response.
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$resp" | python3 -c 'import sys, json
b = json.load(sys.stdin).get("bundle", {})
print("    registered: id={} version={} control_count={} gcs_path={}".format(
    b.get("id"), b.get("version"), b.get("control_count"), b.get("gcs_path")))'
  else
    echo "    registered: $resp"
  fi
  echo "    public:     ${BUNDLE_PUBLIC_BASE_URL%/}/${name}-${version}.tar.gz"
done

echo "Done. Published: $BUNDLES"
