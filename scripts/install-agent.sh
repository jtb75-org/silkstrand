#!/usr/bin/env sh
# SilkStrand agent installer.
#
# Two install modes:
#
#   binary (default) — native binary + systemd/launchd service.
#   docker           — unprivileged container. Useful for discovering
#                      targets on Docker bridge networks that aren't
#                      routable from the host (e.g., macOS / Docker
#                      Desktop, where 172.x bridges live inside the
#                      Docker VM).
#
# One-shot flow (binary):
#   curl -sSL https://downloads.silkstrand.io/agent/install.sh | \
#     sudo sh -s -- \
#       --token=sst_<install-token> \
#       --api-url=https://<your DC API host> \
#       --name=$(hostname) \
#       --as-service \
#       --allow-cidr=192.168.0.0/24
#
# One-shot flow (docker):
#   curl -sSL https://downloads.silkstrand.io/agent/install.sh | \
#     sudo sh -s -- \
#       --mode=docker \
#       --token=sst_<install-token> \
#       --api-url=https://<your DC API host> \
#       --name=$(hostname)-docker \
#       --docker-scan-all-bridges
#
# Flags:
#   --mode={binary,docker} Install mode. Default binary.
#   --token=TOK            One-time install token from the SilkStrand UI
#   --api-url=URL          Your DC's HTTPS URL (same host, wss:// is derived)
#   --name=NAME            Friendly name for this agent (default: hostname)
#   --as-service           Install + start a system service (binary mode)
#   --no-service           Skip service install (binary mode default)
#   --uninstall            Remove the agent: notify server, stop service
#                          or container, delete local state.
#   --install-dir=PATH     Where to install the binary (default /usr/local/bin)
#   --version=TAG          Release tag to install. binary mode: binary
#                          download; docker mode: image tag. Default "latest".
#   --release-url=URL      Override the GCS base for binaries (dev / mirrors)
#   --allow-cidr=CIDR      Add CIDR to the rendered scan-allowlist.yaml.
#                          Repeatable. Works in binary and docker modes —
#                          removes the manual allowlist-edit step.
#   --rate-limit-pps=N     Discovery scan rate limit (default 500).
#                          Written into the rendered allowlist.
#   --proxy=URL            Egress HTTPS proxy (e.g. http://proxy.corp:3128).
#                          Used for the install fetch and persisted so the
#                          agent honors it (HTTPS_PROXY). No credentials in
#                          the command — authenticated proxies use the host's
#                          existing proxy env or a host-side credentials file.
#   --no-proxy=LIST        Comma-separated hosts/domains to bypass the proxy
#                          (NO_PROXY).
#   --ca-cert=FILE         Path to a PEM CA bundle to trust for outbound TLS
#                          (TLS-inspecting proxies). Path only — the file must
#                          already exist on the host; it is never inlined. In
#                          docker mode it is bind-mounted into the container.
#                          --proxy / --no-proxy / --ca-cert all work in both
#                          binary and docker modes and persist across upgrades.
#   --docker-network=NAME  Attach container to this Docker network.
#                          Repeatable. First one is passed to `docker run`;
#                          subsequent ones via `docker network connect`.
#   --docker-scan-all-bridges
#                          Auto-enumerate all Docker bridge networks on
#                          this host, filter to RFC1918, add subnets to
#                          the allowlist, and attach the container to each.
#                          docker-mode only.
#   --docker-host-network  (Linux docker mode) Run container with
#                          `--network=host`. Mutually exclusive with
#                          --docker-network / --docker-scan-all-bridges.
#   --docker-volume=NAME   Named volume for /home/nonroot (runtimes +
#                          creds). Default silkstrand-agent-<short-id>-runtimes.
#   --docker-caps=raw      Run container with CAP_NET_RAW + CAP_NET_ADMIN
#                          and switch naabu back to SYN scan. Default: no
#                          caps, naabu CONNECT mode.
#   --image-registry=REG   Override image registry base.
#                          Default zot.ng20.org (public registry for the
#                          customer agent image).
#   --print-compose        Emit a docker-compose.yaml snippet for the
#                          requested config on stdout instead of
#                          running the container. Implies --mode=docker.
#   --upgrade              (docker mode) Pull the requested --version
#                          image and recreate the existing container
#                          with the same flags. Credentials persist via
#                          the named volume — no re-bootstrap needed.

set -eu

MODE="binary"
INSTALL_DIR="/usr/local/bin"
VERSION="latest"
RELEASE_URL="https://downloads.silkstrand.io/agent"
# Public registry for the customer agent image (zot.ng20.org/silkstrand-agent).
# The old GCP Artifact Registry default was a stale leftover from the GCP→zot
# migration that made docker-mode installs fail at `docker pull`.
IMAGE_REGISTRY="zot.ng20.org"
TOKEN=""
API_URL=""
NAME="$(uname -n 2>/dev/null || echo agent)"
AS_SERVICE=0
UNINSTALL=0
UPGRADE=0
CONFIG_DIR="/etc/silkstrand"
CONFIG_FILE="/etc/silkstrand/agent.env"
BUNDLE_DIR="/var/lib/silkstrand/bundles"
ALLOWLIST_FILE="/etc/silkstrand/scan-allowlist.yaml"
ALLOW_CIDRS=""
RATE_LIMIT_PPS="500"
PROXY=""
NO_PROXY_LIST=""
CA_CERT=""
# Fixed in-container path the host CA file is bind-mounted to (docker mode); the
# host path is not visible inside the container, so SILKSTRAND_CA_CERT_PATH must
# point here, not at the host path (ADR 013 D3).
DOCKER_CA_DEST="/etc/silkstrand/ca.pem"
DOCKER_NETWORKS=""
DOCKER_SCAN_ALL_BRIDGES=0
DOCKER_HOST_NETWORK=0
DOCKER_VOLUME=""
DOCKER_CAPS=""
PRINT_COMPOSE=0

log() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --mode=*)        MODE="${1#*=}" ;;
            --token=*)       TOKEN="${1#*=}" ;;
            --api-url=*)     API_URL="${1#*=}" ;;
            --name=*)        NAME="${1#*=}" ;;
            --install-dir=*) INSTALL_DIR="${1#*=}" ;;
            --version=*)     VERSION="${1#*=}" ;;
            --release-url=*) RELEASE_URL="${1#*=}" ;;
            --image-registry=*) IMAGE_REGISTRY="${1#*=}" ;;
            --as-service)    AS_SERVICE=1 ;;
            --no-service)    AS_SERVICE=0 ;;
            --uninstall)     UNINSTALL=1 ;;
            --upgrade)       UPGRADE=1 ;;
            --allow-cidr=*)  ALLOW_CIDRS="$ALLOW_CIDRS ${1#*=}" ;;
            --rate-limit-pps=*) RATE_LIMIT_PPS="${1#*=}" ;;
            --proxy=*)       PROXY="${1#*=}" ;;
            --no-proxy=*)    NO_PROXY_LIST="${1#*=}" ;;
            --ca-cert=*)     CA_CERT="${1#*=}" ;;
            --docker-network=*) DOCKER_NETWORKS="$DOCKER_NETWORKS ${1#*=}" ;;
            --docker-scan-all-bridges) DOCKER_SCAN_ALL_BRIDGES=1 ;;
            --docker-host-network) DOCKER_HOST_NETWORK=1 ;;
            --docker-volume=*) DOCKER_VOLUME="${1#*=}" ;;
            --docker-caps=*) DOCKER_CAPS="${1#*=}" ;;
            --print-compose) PRINT_COMPOSE=1; MODE="docker" ;;
            -h|--help)       grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
            *) fail "unknown flag: $1" ;;
        esac
        shift
    done
    case "$MODE" in
        binary|docker) ;;
        *) fail "--mode must be 'binary' or 'docker' (got '$MODE')" ;;
    esac
    if [ -n "$CA_CERT" ] && [ ! -r "$CA_CERT" ]; then
        fail "--ca-cert file not found or unreadable: $CA_CERT (provide a path that exists on this host)"
    fi
}

detect_os() {
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux|darwin) printf '%s' "$os" ;;
        *) fail "unsupported OS: $os" ;;
    esac
}

detect_arch() {
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) printf 'amd64' ;;
        arm64|aarch64) printf 'arm64' ;;
        *) fail "unsupported arch: $arch" ;;
    esac
}

need() { command -v "$1" >/dev/null 2>&1 || fail "'$1' is required"; }

need_root() {
    if [ "$(id -u)" -ne 0 ]; then
        fail "run this script with sudo (writes to $INSTALL_DIR and $CONFIG_DIR)"
    fi
}

# run_curl wraps curl so every network fetch honors --proxy / --no-proxy /
# --ca-cert when given. Args are prepended via `set --` so paths/URLs keep their
# quoting (POSIX-safe). Proxy credentials are never added here — the host's
# proxy env or a host-side netrc handles auth.
run_curl() {
    [ -n "$CA_CERT" ] && set -- --cacert "$CA_CERT" "$@"
    [ -n "$NO_PROXY_LIST" ] && set -- --noproxy "$NO_PROXY_LIST" "$@"
    [ -n "$PROXY" ] && set -- --proxy "$PROXY" "$@"
    curl "$@"
}

download_binary() {
    suffix="$(detect_os)-$(detect_arch)"
    bin_url="${RELEASE_URL}/${VERSION}/silkstrand-agent-${suffix}"
    sha_url="${bin_url}.sha256"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    log "Downloading silkstrand-agent (${suffix}, ${VERSION})"
    run_curl -fsSL -o "$tmp/silkstrand-agent" "$bin_url"
    run_curl -fsSL -o "$tmp/silkstrand-agent.sha256" "$sha_url"

    log "Verifying checksum"
    expected=$(cut -d' ' -f1 "$tmp/silkstrand-agent.sha256")
    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$tmp/silkstrand-agent" | cut -d' ' -f1)
    else
        actual=$(shasum -a 256 "$tmp/silkstrand-agent" | cut -d' ' -f1)
    fi
    if [ "$expected" != "$actual" ]; then
        fail "checksum mismatch: expected $expected, got $actual"
    fi

    chmod +x "$tmp/silkstrand-agent"
    install -d "$INSTALL_DIR"
    mv "$tmp/silkstrand-agent" "$INSTALL_DIR/silkstrand-agent"
    log "Downloaded silkstrand-agent → $INSTALL_DIR/silkstrand-agent"
}

# Exchange the install token for agent credentials. Sets the shell vars
# AGENT_ID, API_KEY, and WS_URL. Works the same for binary and docker
# modes — only the target of the credentials differs afterward.
bootstrap_agent_api() {
    [ -n "$TOKEN" ] || fail "--token is required"
    [ -n "$API_URL" ] || fail "--api-url is required"

    agent_version="$VERSION"
    if [ "$MODE" = "binary" ] && [ -x "$INSTALL_DIR/silkstrand-agent" ]; then
        agent_version=$("$INSTALL_DIR/silkstrand-agent" version 2>/dev/null || echo "$VERSION")
    fi
    log "Registering agent '$NAME' (version $agent_version)"
    payload=$(printf '{"install_token":"%s","name":"%s","version":"%s"}' "$TOKEN" "$NAME" "$agent_version")

    resp_file=$(mktemp)
    http_code=$(run_curl -sS -o "$resp_file" -w '%{http_code}' -X POST \
        -H 'Content-Type: application/json' \
        -d "$payload" \
        "${API_URL}/api/v1/agents/bootstrap" 2>/dev/null || echo "000")
    resp=$(cat "$resp_file")
    rm -f "$resp_file"

    if [ "$http_code" = "000" ]; then
        fail "bootstrap request failed to reach the server (network error). Verify --api-url is reachable."
    fi
    if [ "$http_code" -ge 400 ]; then
        server_msg=$(printf '%s' "$resp" | sed -n 's/.*"error"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
        if [ -z "$server_msg" ]; then server_msg="${resp:-<empty body>}"; fi
        case "$http_code" in
            401) fail "install token rejected (HTTP 401): $server_msg
Tokens are single-use and expire after 1 hour — generate a new one in the SilkStrand UI." ;;
            *)   fail "bootstrap failed (HTTP $http_code): $server_msg" ;;
        esac
    fi

    AGENT_ID=$(printf '%s' "$resp" | sed -n 's/.*"agent_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    API_KEY=$(printf '%s' "$resp" | sed -n 's/.*"api_key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    [ -n "$AGENT_ID" ] || fail "server did not return agent_id (response: $resp)"
    [ -n "$API_KEY" ]  || fail "server did not return api_key"

    WS_URL=$(printf '%s' "$API_URL" | sed -e 's,^https://,wss://,' -e 's,^http://,ws://,')
}

bootstrap_agent_binary() {
    bootstrap_agent_api
    install -d -m 0700 "$CONFIG_DIR"
    install -d -m 0755 "$BUNDLE_DIR"
    umask 077
    cat > "$CONFIG_FILE" <<EOF
# SilkStrand agent — written by install.sh.
# mode 0600 — do not share.
SILKSTRAND_AGENT_ID=$AGENT_ID
SILKSTRAND_AGENT_KEY=$API_KEY
SILKSTRAND_API_URL=$WS_URL
SILKSTRAND_BUNDLE_DIR=$BUNDLE_DIR
EOF
    # ADR 013 D3: persist egress proxy + custom CA so the running agent honors
    # the same network policy as the install fetch. Paths/URLs only — no
    # proxy credentials are ever written here.
    [ -n "$PROXY" ]         && printf 'HTTPS_PROXY=%s\n' "$PROXY"        >> "$CONFIG_FILE"
    [ -n "$NO_PROXY_LIST" ] && printf 'NO_PROXY=%s\n'    "$NO_PROXY_LIST" >> "$CONFIG_FILE"
    [ -n "$CA_CERT" ]       && printf 'SILKSTRAND_CA_CERT_PATH=%s\n' "$CA_CERT" >> "$CONFIG_FILE"
    chmod 0600 "$CONFIG_FILE"
    log "Credentials written to $CONFIG_FILE"
    log "Agent ID: $AGENT_ID"
}

# Render scan-allowlist.yaml into a given path from ALLOW_CIDRS +
# RATE_LIMIT_PPS. Does nothing if no CIDRs were provided (and does not
# overwrite an existing file in that case either).
render_allowlist() {
    target="$1"
    trimmed=$(printf '%s' "$ALLOW_CIDRS" | xargs)
    if [ -z "$trimmed" ]; then
        # No CIDRs given. Scaffold a commented, fail-closed template (unless one
        # already exists) so the file is present and the format is discoverable
        # — otherwise the agent starts with no allowlist and silently rejects
        # every scan with no hint why.
        if [ ! -f "$target" ]; then
            install -d "$(dirname "$target")"
            cat > "$target" <<'YAML'
# SilkStrand scan allowlist.
# The agent scans ONLY hosts/ranges listed under `allow:`. This file is the
# final authority — the SaaS cannot override it. Until you add entries, every
# scan is rejected (fail-closed). Edit, then no agent restart is needed.
#
# Example:
#   allow:
#     - 192.168.1.0/24        # CIDR
#     - 10.0.0.5              # single IP
#     - 10.0.0.10-10.0.0.50   # inclusive range
#     - host.example.com      # hostname (exact or *.example.com)
#   rate_limit_pps: 200       # optional, capped at 1000
allow: []
YAML
            chmod 0644 "$target"
            log "Scaffolded allowlist → $target (edit to authorize scanning; scans are rejected until you do)"
        fi
        return 0
    fi
    install -d "$(dirname "$target")"
    {
        printf 'allow:\n'
        for cidr in $trimmed; do
            printf '  - %s\n' "$cidr"
        done
        printf 'rate_limit_pps: %s\n' "$RATE_LIMIT_PPS"
    } > "$target"
    chmod 0444 "$target"
    log "Wrote allowlist → $target"
}

install_service_linux() {
    # --as-service assumes systemd. On a host without it (minimal images,
    # containers), fail gracefully instead of erroring on `systemctl: not
    # found` — the binary + creds are already installed, so point the user at
    # the manual-run path.
    if [ ! -d /run/systemd/system ] || ! command -v systemctl >/dev/null 2>&1; then
        warn "systemd not detected — cannot install a system service on this host."
        log "The binary and credentials are installed; start the agent manually:"
        print_manual_run
        return 0
    fi
    unit=/etc/systemd/system/silkstrand-agent.service
    cat > "$unit" <<EOF
[Unit]
Description=SilkStrand compliance scanner agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$CONFIG_FILE
ExecStart=$INSTALL_DIR/silkstrand-agent
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$unit"
    systemctl daemon-reload
    systemctl enable --now silkstrand-agent
    log "silkstrand-agent service started (systemd)"
    log "Tail logs: journalctl -u silkstrand-agent -f"
}

install_service_darwin() {
    plist=/Library/LaunchDaemons/io.silkstrand.agent.plist
    set -a; . "$CONFIG_FILE"; set +a
    # launchd (unlike systemd's EnvironmentFile) embeds env in the plist, so the
    # optional proxy/CA vars from agent.env must be emitted explicitly or the
    # daemon starts without them (ADR 013 D3). Build them conditionally.
    extra_env=""
    [ -n "${HTTPS_PROXY:-}" ] && extra_env="$extra_env
    <key>HTTPS_PROXY</key><string>${HTTPS_PROXY}</string>"
    [ -n "${NO_PROXY:-}" ] && extra_env="$extra_env
    <key>NO_PROXY</key><string>${NO_PROXY}</string>"
    [ -n "${SILKSTRAND_CA_CERT_PATH:-}" ] && extra_env="$extra_env
    <key>SILKSTRAND_CA_CERT_PATH</key><string>${SILKSTRAND_CA_CERT_PATH}</string>"
    cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.silkstrand.agent</string>
  <key>ProgramArguments</key>
    <array><string>$INSTALL_DIR/silkstrand-agent</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>SILKSTRAND_AGENT_ID</key><string>${SILKSTRAND_AGENT_ID}</string>
    <key>SILKSTRAND_AGENT_KEY</key><string>${SILKSTRAND_AGENT_KEY}</string>
    <key>SILKSTRAND_API_URL</key><string>${SILKSTRAND_API_URL}</string>
    <key>SILKSTRAND_BUNDLE_DIR</key><string>${SILKSTRAND_BUNDLE_DIR}</string>${extra_env}
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/var/log/silkstrand-agent.log</string>
  <key>StandardErrorPath</key><string>/var/log/silkstrand-agent.log</string>
</dict></plist>
EOF
    chmod 0644 "$plist"
    launchctl bootout system/io.silkstrand.agent 2>/dev/null || true
    launchctl bootstrap system "$plist"
    log "silkstrand-agent service started (launchd)"
    log "Tail logs: tail -f /var/log/silkstrand-agent.log"
}

install_service() {
    case "$(detect_os)" in
        linux)  install_service_linux ;;
        darwin) install_service_darwin ;;
    esac
}

print_manual_run() {
    cat <<EOF

Next step (manual run — no service installed):

  sudo sh -c 'set -a; . $CONFIG_FILE; set +a; exec $INSTALL_DIR/silkstrand-agent'

(The 'set -a' is required: $CONFIG_FILE holds plain KEY=value lines so it
stays usable as a systemd EnvironmentFile, so sourcing alone won't export
them to the agent.)

Or re-run install.sh with --as-service to install a system service.
EOF
}

# --- docker mode -----------------------------------------------------

docker_short_id() {
    # First dash-delimited chunk of the UUID. Keeps container / volume
    # names short but unique enough for realistic per-host agent counts.
    printf '%s' "$AGENT_ID" | cut -d- -f1
}

docker_container_name() { printf 'silkstrand-agent-%s' "$(docker_short_id)"; }
docker_default_volume() { printf 'silkstrand-agent-%s-runtimes' "$(docker_short_id)"; }

docker_host_allowlist_dir() {
    # Linux: /etc/silkstrand/agents/<id>. macOS (Docker Desktop): home
    # dir — /etc can be read-only under some SIP setups and Docker
    # Desktop bind-mounts from the home dir without extra config.
    if [ "$(detect_os)" = "darwin" ]; then
        printf '%s/.silkstrand/agents/%s' "${HOME:-/Users/$(id -un)}" "$AGENT_ID"
    else
        printf '/etc/silkstrand/agents/%s' "$AGENT_ID"
    fi
}

docker_preflight() {
    need docker
    if ! docker info >/dev/null 2>&1; then
        fail "docker daemon unreachable — is Docker running and is this user in the 'docker' group?"
    fi
    if [ "$DOCKER_HOST_NETWORK" -eq 1 ]; then
        if [ "$(detect_os)" = "darwin" ]; then
            fail "--docker-host-network is Linux-only (Docker Desktop on macOS does not implement host networking)"
        fi
        if [ -n "$(printf '%s' "$DOCKER_NETWORKS" | xargs)" ] || [ "$DOCKER_SCAN_ALL_BRIDGES" -eq 1 ]; then
            fail "--docker-host-network is mutually exclusive with --docker-network / --docker-scan-all-bridges"
        fi
    fi
}

# Print each RFC1918 subnet exactly once, newline-separated, by
# inspecting every Docker bridge network on the host. Used both for
# allowlist rendering and network-attach.
docker_enumerate_bridges() {
    # Walk all networks with the bridge driver (default + user-defined).
    docker network ls --filter driver=bridge -q | while read -r net; do
        [ -n "$net" ] || continue
        name=$(docker network inspect "$net" --format '{{.Name}}')
        # Skip the literal "none" net (no driver bridge entries here in
        # practice, but guard anyway).
        [ "$name" = "none" ] && continue
        subnets=$(docker network inspect "$net" --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}')
        for s in $subnets; do
            case "$s" in
                10.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|192.168.*)
                    printf '%s\t%s\n' "$name" "$s" ;;
            esac
        done
    done
}

# Populate ALLOW_CIDRS and DOCKER_NETWORKS from docker_enumerate_bridges.
# Idempotent — safe even if the user also passed --docker-network /
# --allow-cidr explicitly.
docker_expand_all_bridges() {
    log "Enumerating Docker bridge networks..."
    mapping=$(docker_enumerate_bridges)
    if [ -z "$mapping" ]; then
        fail "no RFC1918 Docker bridge subnets found — nothing to attach to"
    fi
    printf '%s\n' "$mapping" | while IFS=	 read -r name subnet; do
        printf '    %s  →  %s\n' "$name" "$subnet"
    done
    while IFS=	 read -r name subnet; do
        case " $ALLOW_CIDRS " in
            *" $subnet "*) ;;
            *) ALLOW_CIDRS="$ALLOW_CIDRS $subnet" ;;
        esac
        case " $DOCKER_NETWORKS " in
            *" $name "*) ;;
            *) DOCKER_NETWORKS="$DOCKER_NETWORKS $name" ;;
        esac
    done <<EOF
$mapping
EOF
}

# Emit the `docker run` args from the current flag state, ONE argument per
# line. docker_run_built reads them back into positional parameters, so values
# with spaces (a CA path) or glob characters (NO_PROXY=*.internal) are passed
# verbatim — never word-split or pathname-expanded by the shell. A flag and its
# value are two argv entries (`-e` then `FOO=bar`), so each goes on its own line.
docker_build_run_args() {
    cname=$(docker_container_name)
    image="${IMAGE_REGISTRY}/silkstrand-agent:${VERSION}"
    allow_mount="$(docker_host_allowlist_dir)/scan-allowlist.yaml"
    vol="${DOCKER_VOLUME:-$(docker_default_volume)}"

    # Pick the first network for `docker run`; the rest are attached
    # afterwards with `docker network connect`.
    first_net=""
    if [ "$DOCKER_HOST_NETWORK" -eq 1 ]; then
        first_net="host"
    else
        trimmed=$(printf '%s' "$DOCKER_NETWORKS" | xargs)
        first_net=$(printf '%s' "$trimmed" | awk '{print $1}')
        [ -z "$first_net" ] && first_net="bridge"
    fi

    printf -- '-d\n'
    printf -- '--name\n%s\n' "$cname"
    printf -- '--restart\nunless-stopped\n'
    printf -- '--network\n%s\n' "$first_net"
    printf -- '-e\nSILKSTRAND_AGENT_ID=%s\n' "$AGENT_ID"
    printf -- '-e\nSILKSTRAND_AGENT_KEY=%s\n' "$API_KEY"
    printf -- '-e\nSILKSTRAND_API_URL=%s\n' "$WS_URL"
    printf -- '-e\nSILKSTRAND_RUNTIMES_DIR=/home/nonroot/runtimes\n'
    if [ "$DOCKER_CAPS" = "raw" ]; then
        # Raw caps available → override the image's connect-scan default with
        # the faster SYN scan.
        printf -- '--cap-add=NET_RAW\n--cap-add=NET_ADMIN\n'
        printf -- '-e\nSILKSTRAND_NAABU_SCAN_TYPE=s\n'
    else
        printf -- '-e\nSILKSTRAND_NAABU_SCAN_TYPE=c\n'
    fi
    printf -- '-v\n%s:/home/nonroot\n' "$vol"
    if [ -f "$allow_mount" ]; then
        printf -- '-v\n%s:/etc/silkstrand/scan-allowlist.yaml:ro\n' "$allow_mount"
    fi
    # ADR 013 D3: egress proxy + custom CA. Proxy goes in as env; the CA file is
    # bind-mounted from the host (its host path means nothing inside the
    # container) and SILKSTRAND_CA_CERT_PATH points at the in-container path.
    [ -n "$PROXY" ]         && printf -- '-e\nHTTPS_PROXY=%s\n' "$PROXY"
    [ -n "$NO_PROXY_LIST" ] && printf -- '-e\nNO_PROXY=%s\n' "$NO_PROXY_LIST"
    if [ -n "$CA_CERT" ]; then
        printf -- '-v\n%s:%s:ro\n' "$CA_CERT" "$DOCKER_CA_DEST"
        printf -- '-e\nSILKSTRAND_CA_CERT_PATH=%s\n' "$DOCKER_CA_DEST"
    fi
    printf -- '%s\n' "$image"
}

# Run `docker run` with the newline-delimited args from docker_build_run_args,
# read into positional parameters so each is passed verbatim (no word-split, no
# glob). Args never contain newlines (paths/URLs/values), so line-delimiting is
# safe; blank lines are skipped defensively.
docker_run_built() {
    set --
    while IFS= read -r _arg; do
        [ -n "$_arg" ] && set -- "$@" "$_arg"
    done <<EOF
$(docker_build_run_args)
EOF
    docker run "$@" >/dev/null
}

docker_attach_extra_networks() {
    cname=$(docker_container_name)
    [ "$DOCKER_HOST_NETWORK" -eq 1 ] && return 0
    # Skip the first network (already attached via docker run).
    trimmed=$(printf '%s' "$DOCKER_NETWORKS" | xargs)
    [ -z "$trimmed" ] && return 0
    first=$(printf '%s' "$trimmed" | awk '{print $1}')
    for net in $trimmed; do
        [ "$net" = "$first" ] && continue
        log "Attaching to network $net"
        docker network connect "$net" "$cname" >/dev/null 2>&1 || \
            log "  (already attached or network missing — continuing)"
    done
}

docker_wait_for_connected() {
    cname=$(docker_container_name)
    deadline=$(( $(date +%s) + 45 ))
    log "Waiting for agent to register..."
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if docker logs "$cname" 2>&1 | grep -q '"connected to server"'; then
            log "Agent is connected."
            return 0
        fi
        sleep 1
    done
    log "Timed out waiting for 'connected to server' — check: docker logs $cname"
    return 1
}

docker_emit_compose() {
    cname=$(docker_container_name)
    image="${IMAGE_REGISTRY}/silkstrand-agent:${VERSION}"
    vol="${DOCKER_VOLUME:-$(docker_default_volume)}"
    allow_mount="$(docker_host_allowlist_dir)/scan-allowlist.yaml"

    nets=$(printf '%s' "$DOCKER_NETWORKS" | xargs)
    if [ "$DOCKER_HOST_NETWORK" -eq 1 ]; then nets="host"; fi
    [ -z "$nets" ] && nets="bridge"

    cat <<EOF
# SilkStrand agent — generated by install.sh --print-compose
# Save as docker-compose.yaml and run: docker compose up -d
services:
  silkstrand-agent:
    container_name: ${cname}
    image: ${image}
    restart: unless-stopped
    environment:
      SILKSTRAND_AGENT_ID: ${AGENT_ID}
      SILKSTRAND_AGENT_KEY: ${API_KEY}
      SILKSTRAND_API_URL: ${WS_URL}
      SILKSTRAND_RUNTIMES_DIR: /home/nonroot/runtimes
EOF
    if [ "$DOCKER_CAPS" = "raw" ]; then
        # Raw caps → override the image's connect-scan default with SYN scan.
        printf '      SILKSTRAND_NAABU_SCAN_TYPE: s\n'
    else
        printf '      SILKSTRAND_NAABU_SCAN_TYPE: c\n'
    fi
    # ADR 013 D3: proxy env + custom CA path (mounted under volumes below).
    # Quoted because NO_PROXY commonly contains '*' (e.g. *.internal), which is
    # a YAML alias indicator unquoted, and proxy URLs / paths can carry ':'.
    [ -n "$PROXY" ]         && printf '      HTTPS_PROXY: "%s"\n' "$PROXY"
    [ -n "$NO_PROXY_LIST" ] && printf '      NO_PROXY: "%s"\n' "$NO_PROXY_LIST"
    [ -n "$CA_CERT" ]       && printf '      SILKSTRAND_CA_CERT_PATH: "%s"\n' "$DOCKER_CA_DEST"
    printf '    volumes:\n'
    printf '      - %s:/home/nonroot\n' "$vol"
    if [ -f "$allow_mount" ]; then
        printf '      - %s:/etc/silkstrand/scan-allowlist.yaml:ro\n' "$allow_mount"
    fi
    [ -n "$CA_CERT" ] && printf '      - "%s:%s:ro"\n' "$CA_CERT" "$DOCKER_CA_DEST"
    if [ "$DOCKER_CAPS" = "raw" ]; then
        printf '    cap_add:\n      - NET_RAW\n      - NET_ADMIN\n'
    fi
    printf '    networks:\n'
    for n in $nets; do printf '      - %s\n' "$n"; done
    printf '\nnetworks:\n'
    for n in $nets; do
        if [ "$n" = "host" ] || [ "$n" = "bridge" ]; then
            printf '  %s:\n    external: true\n' "$n"
        else
            printf '  %s:\n    external: true\n' "$n"
        fi
    done
    printf '\nvolumes:\n  %s:\n' "$vol"
}

do_install_docker() {
    docker_preflight
    if [ "$UPGRADE" -eq 1 ]; then
        do_upgrade_docker
        return 0
    fi
    bootstrap_agent_api
    if [ "$DOCKER_SCAN_ALL_BRIDGES" -eq 1 ]; then
        docker_expand_all_bridges
    fi
    # Render allowlist to the per-agent host directory.
    allow_dir=$(docker_host_allowlist_dir)
    render_allowlist "${allow_dir}/scan-allowlist.yaml"

    if [ "$PRINT_COMPOSE" -eq 1 ]; then
        docker_emit_compose
        return 0
    fi

    vol="${DOCKER_VOLUME:-$(docker_default_volume)}"
    docker volume create "$vol" >/dev/null
    log "Pulling image ${IMAGE_REGISTRY}/silkstrand-agent:${VERSION}"
    docker pull "${IMAGE_REGISTRY}/silkstrand-agent:${VERSION}" >/dev/null

    log "Starting container $(docker_container_name)"
    docker_run_built
    docker_attach_extra_networks
    docker_wait_for_connected || true

    cat <<EOF

Docker agent ready.
  Container: $(docker_container_name)
  Agent ID:  $AGENT_ID
  Logs:      docker logs -f $(docker_container_name)
  Upgrade:   re-run with --mode=docker --upgrade --version=<tag>
  Uninstall: --mode=docker --uninstall
EOF
}

do_upgrade_docker() {
    cname=$(docker_container_name 2>/dev/null || true)
    # If we don't have an agent id cached, try to read one from a running
    # container's env to preserve identity across upgrades.
    if [ -z "${AGENT_ID:-}" ]; then
        found=$(docker ps -aq --filter 'name=silkstrand-agent-' | head -1)
        [ -n "$found" ] || fail "no running silkstrand-agent container found — run --mode=docker without --upgrade to install first"
        AGENT_ID=$(docker inspect "$found" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SILKSTRAND_AGENT_ID=//p')
        API_KEY=$(docker inspect "$found" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SILKSTRAND_AGENT_KEY=//p')
        WS_URL=$(docker inspect "$found" --format  '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SILKSTRAND_API_URL=//p')
        cname=$(docker_container_name)
    fi
    [ -n "${API_KEY:-}" ] || fail "could not recover agent credentials from existing container"

    # Preserve proxy/CA across the recreate (like creds + networks above) unless
    # the operator re-passes them this invocation (ADR 013 D3). The CA's host
    # path is recovered from the existing bind mount's source.
    _cenv() { docker inspect "$cname" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | sed -n "s/^$1=//p" | head -1; }
    [ -z "$PROXY" ]         && PROXY=$(_cenv HTTPS_PROXY)
    [ -z "$NO_PROXY_LIST" ] && NO_PROXY_LIST=$(_cenv NO_PROXY)
    if [ -z "$CA_CERT" ]; then
        CA_CERT=$(docker inspect "$cname" --format "{{range .Mounts}}{{if eq .Destination \"$DOCKER_CA_DEST\"}}{{.Source}}{{end}}{{end}}" 2>/dev/null)
    fi

    log "Upgrading $cname to ${VERSION}"
    docker pull "${IMAGE_REGISTRY}/silkstrand-agent:${VERSION}" >/dev/null
    nets=$(docker inspect "$cname" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null || echo "")
    docker rm -f "$cname" >/dev/null 2>&1 || true

    # Rebuild the run with the existing credentials. Keep the
    # previously-attached networks by priming DOCKER_NETWORKS.
    trimmed=$(printf '%s' "$nets" | xargs)
    if [ -n "$trimmed" ]; then DOCKER_NETWORKS="$trimmed"; fi

    docker_run_built
    docker_attach_extra_networks
    docker_wait_for_connected || true
    log "Upgrade complete."
}

# --- uninstall -------------------------------------------------------

uninstall_service_linux() {
    if [ -f /etc/systemd/system/silkstrand-agent.service ]; then
        systemctl disable --now silkstrand-agent 2>/dev/null || true
        rm -f /etc/systemd/system/silkstrand-agent.service
        systemctl daemon-reload
        log "removed systemd unit"
    fi
}

uninstall_service_darwin() {
    if [ -f /Library/LaunchDaemons/io.silkstrand.agent.plist ]; then
        launchctl bootout system/io.silkstrand.agent 2>/dev/null || true
        rm -f /Library/LaunchDaemons/io.silkstrand.agent.plist
        log "removed launchd plist"
    fi
}

uninstall_self_delete() {
    if [ ! -f "$CONFIG_FILE" ]; then return 0; fi
    # shellcheck disable=SC1090
    . "$CONFIG_FILE" || return 0
    [ -n "${SILKSTRAND_AGENT_ID:-}" ] || return 0
    [ -n "${SILKSTRAND_AGENT_KEY:-}" ] || return 0
    [ -n "${SILKSTRAND_API_URL:-}" ] || return 0

    http_url=$(printf '%s' "$SILKSTRAND_API_URL" | sed -e 's,^wss://,https://,' -e 's,^ws://,http://,')
    log "Notifying server: agent ${SILKSTRAND_AGENT_ID}"
    curl -fsS -X DELETE \
        -H "Authorization: Bearer $SILKSTRAND_AGENT_KEY" \
        "${http_url}/api/v1/agents/self?agent_id=${SILKSTRAND_AGENT_ID}" \
        >/dev/null 2>&1 || log "server notify failed (continuing with local cleanup)"
}

do_uninstall_binary() {
    need_root
    uninstall_self_delete
    case "$(detect_os)" in
        linux)  uninstall_service_linux ;;
        darwin) uninstall_service_darwin ;;
    esac
    rm -f "$INSTALL_DIR/silkstrand-agent"
    rm -rf "$CONFIG_DIR"
    rm -rf "$BUNDLE_DIR"
    log "Uninstalled silkstrand-agent"
}

do_uninstall_docker() {
    need docker
    # Find any silkstrand-agent-* container; notify server, then remove.
    for cname in $(docker ps -a --format '{{.Names}}' | grep '^silkstrand-agent-' || true); do
        aid=$(docker inspect "$cname" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SILKSTRAND_AGENT_ID=//p')
        akey=$(docker inspect "$cname" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SILKSTRAND_AGENT_KEY=//p')
        aurl=$(docker inspect "$cname" --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SILKSTRAND_API_URL=//p')
        if [ -n "$aid" ] && [ -n "$akey" ] && [ -n "$aurl" ]; then
            http_url=$(printf '%s' "$aurl" | sed -e 's,^wss://,https://,' -e 's,^ws://,http://,')
            log "Notifying server: agent $aid"
            curl -fsS -X DELETE \
                -H "Authorization: Bearer $akey" \
                "${http_url}/api/v1/agents/self?agent_id=${aid}" \
                >/dev/null 2>&1 || log "  server notify failed (continuing)"
        fi
        log "Removing container $cname"
        docker rm -f "$cname" >/dev/null 2>&1 || true
        if [ -n "$aid" ]; then
            dir="/etc/silkstrand/agents/$aid"
            [ -d "$dir" ] && rm -rf "$dir" && log "  removed $dir"
            hdir="${HOME:-}/.silkstrand/agents/$aid"
            [ -d "$hdir" ] && rm -rf "$hdir" && log "  removed $hdir"
        fi
    done
    # Orphan volumes named silkstrand-agent-*-runtimes.
    for vol in $(docker volume ls --format '{{.Name}}' | grep '^silkstrand-agent-.*-runtimes$' || true); do
        log "Removing volume $vol"
        docker volume rm "$vol" >/dev/null 2>&1 || true
    done
    log "Docker agent uninstall complete."
}

cleanup_partial_install() {
    [ "${INSTALL_SUCCEEDED:-0}" -eq 1 ] && return 0
    [ -z "${INSTALL_STARTED:-}" ] && return 0
    printf '\n' >&2
    printf -- '--- Install failed — cleaning up ---\n' >&2
    if [ "$MODE" = "binary" ]; then
        if [ -f "$INSTALL_DIR/silkstrand-agent" ]; then
            rm -f "$INSTALL_DIR/silkstrand-agent" 2>/dev/null || true
            printf 'removed %s/silkstrand-agent\n' "$INSTALL_DIR" >&2
        fi
        if [ -d "$CONFIG_DIR" ]; then
            rm -rf "$CONFIG_DIR" 2>/dev/null || true
            printf 'removed %s\n' "$CONFIG_DIR" >&2
        fi
        if [ -d "$BUNDLE_DIR" ]; then
            rm -rf "$BUNDLE_DIR" 2>/dev/null || true
            printf 'removed %s\n' "$BUNDLE_DIR" >&2
        fi
    else
        # docker mode — tear down anything we named after AGENT_ID.
        if [ -n "${AGENT_ID:-}" ]; then
            docker rm -f "silkstrand-agent-$(printf '%s' "$AGENT_ID" | cut -d- -f1)" >/dev/null 2>&1 || true
            docker volume rm "silkstrand-agent-$(printf '%s' "$AGENT_ID" | cut -d- -f1)-runtimes" >/dev/null 2>&1 || true
        fi
    fi
    printf 'Host is clean. Fix the error above and re-run the install command.\n' >&2
}

main() {
    parse_args "$@"
    need curl

    if [ "$UNINSTALL" -eq 1 ]; then
        case "$MODE" in
            binary) do_uninstall_binary ;;
            docker) do_uninstall_docker ;;
        esac
        return 0
    fi

    if [ "$MODE" = "docker" ]; then
        INSTALL_STARTED=1
        trap cleanup_partial_install EXIT
        do_install_docker
        INSTALL_SUCCEEDED=1
        log "Install complete."
        return 0
    fi

    # --- binary mode ---
    need_root
    INSTALL_STARTED=1
    trap cleanup_partial_install EXIT

    download_binary
    if [ -n "$TOKEN" ] || [ -n "$API_URL" ]; then
        bootstrap_agent_binary
        render_allowlist "$ALLOWLIST_FILE"
        if [ "$AS_SERVICE" -eq 1 ]; then
            install_service
        else
            print_manual_run
        fi
    else
        cat <<EOF

Binary installed. You still need credentials to run the agent.
Generate an install token in the SilkStrand UI and re-run:

  curl -sSL ${RELEASE_URL}/install.sh | sudo sh -s -- \\
    --token=<token> --api-url=<DC url> --name=\$(hostname) --as-service
EOF
    fi
    INSTALL_SUCCEEDED=1
    log "Install complete."
}

main "$@"
