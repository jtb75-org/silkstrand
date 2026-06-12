# Single-process agent image. Supports the install-token bootstrap flow so
# running `docker run ... -e SILKSTRAND_INSTALL_TOKEN=...` is enough to
# register and start scanning. Credentials are persisted to /var/lib/silkstrand
# (mount a volume to survive restarts).

FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/silkstrand-agent \
    ./cmd/silkstrand-agent/

# Runtime: debian-slim rather than distroless. The recon tools (naabu, fetched
# at scan time) are dynamically linked against libpcap, which in turn pulls
# libdbus/libsystemd — a dependency closure that breaks if hand-copied into a
# static/distroless base (and re-breaks on every libpcap bump). Letting apt
# resolve it is the maintainable path; ca-certificates covers HTTPS tool +
# template downloads.
FROM debian:12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends libpcap0.8 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --create-home --home-dir /home/nonroot nonroot
COPY --from=builder /out/silkstrand-agent /usr/local/bin/silkstrand-agent
# Unprivileged containers can't raw-socket SYN-scan; default naabu to
# connect-scan so discovery works out of the box (override to "s" when the
# container is run with CAP_NET_RAW for faster scans).
ENV SILKSTRAND_NAABU_SCAN_TYPE=c
# nonroot's home is writable; use it for creds + the runtime tool cache
# (the default /var/lib/silkstrand isn't writable by nonroot).
ENV SILKSTRAND_CREDS_PATH=/home/nonroot/agent.creds
ENV SILKSTRAND_RUNTIMES_DIR=/home/nonroot/runtimes
USER nonroot
ENTRYPOINT ["/usr/local/bin/silkstrand-agent"]
