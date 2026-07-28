#!/bin/sh
set -e

if [ -z "$PICCOLO_CONFIG_URL" ]; then
    echo "ERROR: PICCOLO_CONFIG_URL is required" >&2
    exit 1
fi

if [ "$PICCOLO_MODE" = "agent" ]; then
    exec /usr/local/bin/piccolo-perf agent \
        -config-url "$PICCOLO_CONFIG_URL"
else
    exec /usr/local/bin/piccolo-perf exporter \
        -config-url "$PICCOLO_CONFIG_URL" \
        -probe-mode "${PICCOLO_PROBE_MODE:-background}" \
        -metrics-addr "${PICCOLO_METRICS_ADDR:-[::]:9862}"
fi
