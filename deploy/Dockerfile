# syntax=docker/dockerfile:1
#
# Multi-stage build for piccolo-perf.
# The final image is Alpine-based so CAP_NET_RAW (for MTU/trace) can be granted
# via Docker/RouterOS container capabilities without requiring a setuid binary.
#
# Supported platforms: linux/arm/v6, linux/arm/v7, linux/arm64, linux/amd64
#
# Build:
#   docker buildx build --platform linux/arm/v6,linux/arm64 -t piccolo-perf .
#
# Run (RouterOS container or plain Docker):
#   docker run --cap-add NET_RAW --cap-add NET_BIND_SERVICE \
#     -e PICCOLO_CONFIG_URL=https://your-config-server/piccolo-perf-config.json \
#     -p 862:862/udp -p 5201:5201/tcp -p 9862:9862/tcp \
#     piccolo-perf

# ── Stage 1: download release binary ─────────────────────────────────────────
FROM alpine:3.20 AS downloader
ARG TARGETARCH
ARG TARGETVARIANT

# Map Docker platform → goreleaser asset name
# arm/v6 → linux_arm_6, arm/v7 → linux_arm_6 (best compat), arm64 → linux_arm64
RUN apk add --no-cache curl tar && \
    VERSION=$(curl -fsSL https://api.github.com/repos/retecolo/piccolo-perf/releases/latest \
              | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/') && \
    case "${TARGETARCH}${TARGETVARIANT}" in \
      arm64)   ASSET="piccolo-perf_${VERSION}_linux_arm64.tar.gz" ;; \
      armv6)   ASSET="piccolo-perf_${VERSION}_linux_arm_6.tar.gz" ;; \
      armv7)   ASSET="piccolo-perf_${VERSION}_linux_arm_6.tar.gz" ;; \
      amd64)   ASSET="piccolo-perf_${VERSION}_linux_amd64.tar.gz" ;; \
      386)     ASSET="piccolo-perf_${VERSION}_linux_386.tar.gz" ;; \
      *)       echo "Unsupported platform: ${TARGETARCH}${TARGETVARIANT}" && exit 1 ;; \
    esac && \
    curl -fsSL "https://github.com/retecolo/piccolo-perf/releases/latest/download/${ASSET}" \
      | tar -xz -C /usr/local/bin piccolo-perf && \
    chmod 755 /usr/local/bin/piccolo-perf

# ── Stage 2: minimal runtime image ───────────────────────────────────────────
FROM alpine:3.20

# ca-certificates: needed for HTTPS config fetches and TLS metrics endpoint.
# libcap: provides setcap so the entrypoint can grant NET_RAW at runtime
#         without running the process as root.
RUN apk add --no-cache ca-certificates libcap && \
    adduser -D -H -s /sbin/nologin piccolo

COPY --from=downloader /usr/local/bin/piccolo-perf /usr/local/bin/piccolo-perf

# Grant file capabilities so the process can bind port 862 and open raw sockets
# without running as root. CAP_NET_RAW is required for MTU discovery and
# traceroute; CAP_NET_BIND_SERVICE for TWAMP on port 862.
RUN setcap cap_net_raw,cap_net_bind_service+ep /usr/local/bin/piccolo-perf

USER piccolo

# TWAMP-Light reflector (UDP)
EXPOSE 862/udp
# Native bandwidth sink (TCP)
EXPOSE 5201/tcp
# Prometheus metrics (TCP, HTTPS if TLS configured)
EXPOSE 9862/tcp

# PICCOLO_CONFIG_URL — required. Set via -e or RouterOS env= container option.
# PICCOLO_MODE      — optional. Defaults to "exporter". Set to "agent" for
#                     InfluxDB-push-only mode without Prometheus /metrics.
ENV PICCOLO_CONFIG_URL="" \
    PICCOLO_MODE="exporter" \
    PICCOLO_METRICS_ADDR="[::]:9862" \
    PICCOLO_PROBE_MODE="background"

COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
