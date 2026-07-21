# piccolo-perf Grafana Dashboards — Prometheus

Prometheus-native dashboards using PromQL. Use these when your piccolo-perf probes report to Prometheus via `piccolo-perf exporter`.

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
| URL | `https://your-prometheus-host:9090` |
| TLS — Skip TLS verification | Enable if using self-signed certs |
| Basic auth | Enable; enter Prometheus username/password |

The `prometheus_hardened` Ansible role configures Prometheus with TLS and basic auth. Grafana must be configured with matching credentials to scrape `/api/v1/query`.

## Label reference

All dashboards scope queries to `job="piccolo_perf"` to avoid conflicts with other exporters (node_exporter, etc.).

Labels available on every `piccolo_*` metric:

| Label | Source | Example |
|---|---|---|
| `source` | piccolo-perf hostname | `probe-a` |
| `target` | peer hostname from config | `probe-b` |
| `site` | `site` field in config hosts array | `809` |
| `topology` | `topology` field from config | `mesh` |
| `instance` | Added by Prometheus — probe's mesh IPv6 and port | `[fd7a:115c:a1e0:809::12]:9862` |
| `job` | Prometheus job name | `piccolo_perf` |

The `site` template variable in each dashboard lets you filter by site as defined in `piccolo_perf_hosts` in your Ansible `group_vars/all.yml`.

## Template variables

| Variable | Available in | Populated from |
|---|---|---|
| `source` | All dashboards | Probe hostnames |
| `target` | TWAMP, MTU/trace, overview | Peer hostnames |
| `site` | All dashboards | Site labels from config |
| `resolver` | DNS | Resolver IPs from config |
| `name` | DNS | Queried FQDNs |
| `method` | Bandwidth | `native` or `iperf3` |
