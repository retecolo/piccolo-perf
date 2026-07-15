# piccolo-perf Grafana Dashboards — Prometheus

Prometheus-native dashboards using PromQL. Use these when your piccolo-perf probes report to Prometheus (via `piccolo-perf exporter`).

For InfluxDB (Flux) dashboards, see the parent `../` directory.

## Dashboards

| File | UID | Coverage |
|---|---|---|
| `overview.json` | `piccolo-prom-overview` | Fleet-wide summary: RTT, loss, jitter, bandwidth, DNS, MTU, traceroute |
| `twamp.json` | `piccolo-prom-twamp` | RTT min/avg/max, jitter, std deviation, packet loss, reflected packet counter |
| `bandwidth.json` | `piccolo-prom-bandwidth` | TX/RX throughput over time, method breakdown (native vs iperf3) |
| `dns.json` | `piccolo-prom-dns` | Resolver RTT, p50/p95/p99 percentiles, success rate, per-resolver table |
| `mtu-trace.json` | `piccolo-prom-mtu-trace` | Path MTU over time, MTU state timeline, hop count, trace completeness, per-hop RTT |

## Import

1. Go to **Dashboards → Import** in Grafana
2. Click **Upload JSON file** and select a file from this directory
3. Select your **Prometheus** datasource from the dropdown (mapped to `DS_PROMETHEUS`)
4. Click **Import**

## Prometheus datasource setup

In Grafana → **Connections → Data sources → Add new data source → Prometheus**:

| Field | Value |
|---|---|
| URL | `http://localhost:9090` (or your Prometheus address) |
| Scrape interval | Match your `piccolo-perf exporter` scrape interval (default 60s) |

## Template variables

Every dashboard exposes `source` and `target` dropdowns (plus measurement-specific ones like `resolver` for DNS, `method` for bandwidth). Values are populated automatically from your Prometheus label set.
