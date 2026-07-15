# piccolo-perf — Lightweight Network Performance Toolkit

A self-contained, resource-conscious network performance toolkit for constrained devices — routers, embedded systems, low-power SBCs, and anything else that can run a static Go binary. Inspired by [perfSONAR](https://www.perfsonar.net/), built for environments where perfSONAR won't fit.

One binary. No runtime dependencies. IPv4 and IPv6.

## Measurements

| Subcommand | What it measures | Protocol |
|---|---|---|
| `twamp` | RTT, jitter, packet loss | TWAMP-Light (RFC 5357) |
| `bw` | TCP throughput | Native TCP or iperf3 (when present) |
| `trace` | Per-hop RTT, path topology | ICMP TTL/HopLimit probing |
| `mtu` | Effective path MTU | ICMP DF-bit binary search |
| `dns` | Resolver latency, success rate | DNS over UDP |
| `agent` | All of the above, scheduled | — |

TWAMP-Light is the RFC 5357 §5 profile — no TCP control session, pure UDP test packets. Interoperable with other TWAMP-Light endpoints but not with full TWAMP (Cisco, Juniper, IXIA) that require the Control handshake.

## Installation

### One-line installer (recommended)

Detects OS and architecture, downloads the latest release, verifies the checksum, and installs to `/usr/local/bin`:

```sh
/bin/sh -c "$(curl -fsSL https://raw.githubusercontent.com/retecolo/piccolo-perf/main/install.sh)"
```

Supported platforms: Linux, macOS, FreeBSD, OpenBSD, NetBSD, DragonFly BSD, Solaris — across amd64, arm64, arm, 386, mips, mips64, mipsle, ppc64le, riscv64, s390x.

### Pre-built binaries

Download from the [releases page](https://github.com/retecolo/piccolo-perf/releases/latest):

```sh
# Linux arm64 example
curl -fsSL https://github.com/retecolo/piccolo-perf/releases/latest/download/piccolo-perf_linux_arm64.tar.gz \
  | tar -xz
sudo install -m 755 piccolo-perf /usr/local/bin/
```

### Build from source

```sh
git clone https://github.com/retecolo/piccolo-perf.git
cd piccolo-perf
go build -o piccolo-perf .
sudo install -m 755 piccolo-perf /usr/local/bin/
```

Requires Go 1.25+. Builds are fully static (`CGO_ENABLED=0`).

## Quick Start

```sh
# One-shot latency test
piccolo-perf twamp -mode client -server 2001:db8::1

# DNS resolver timing
piccolo-perf dns -resolver 2620:fe::fe -name example.com

# Traceroute with hop RTTs
piccolo-perf trace -target 2001:db8::1

# Path MTU discovery (requires CAP_NET_RAW or root)
sudo piccolo-perf mtu -target 192.0.2.1

# Bandwidth test (start server on target first)
piccolo-perf bw -mode server          # on target
piccolo-perf bw -target 192.0.2.1    # on probe

# Multi-measurement agent daemon
piccolo-perf agent -config-url http://config-server/piccolo-config.json
```

## Subcommands

### `piccolo-perf twamp`

RFC 5357 §5 TWAMP-Light client and reflector.

```sh
# Server / reflector
sudo piccolo-perf twamp -mode server
piccolo-perf twamp -mode server -port 8620       # high port, no root

# Client
piccolo-perf twamp -mode client -server 2001:db8::1
piccolo-perf twamp -mode client -server 192.168.1.1 -count 100 -interval 100ms
piccolo-perf twamp -mode client -server 192.168.1.1 -no-sync  # unsynchronized clock

# Agent (reflector + prober + InfluxDB push)
piccolo-perf twamp -mode agent -config-url http://config-server/piccolo-config.json

# Exporter (reflector + prober + Prometheus /metrics)
piccolo-perf twamp -mode exporter \
  -config-url http://config-server/piccolo-config.json \
  -probe-mode background \
  -metrics-addr :9862
```

**TWAMP flags:**

| Flag | Default | Description |
|---|---|---|
| `-mode` | `client` | `client`, `server`, `agent`, or `exporter` |
| `-server` | `localhost` | Server address (client mode) |
| `-port` | `862` | UDP port |
| `-count` | `10` | Packets to send |
| `-interval` | `1s` | Interval between packets |
| `-timeout` | `5s` | Per-packet receive timeout |
| `-padding` | `0` | Extra zero-padding bytes per packet |
| `-no-sync` | `false` | Assert clock is NOT NTP-synchronized (clears S-bit) |
| `-source` | `""` | Local address to bind the client socket (pins source address, see IPv6 note below) |
| `-rate-limit` | `0` | Max packets/sec per source IP on server (0 = unlimited) |
| `-allowed` | `""` | Comma-separated CIDR allowlist for server (empty = all) |
| `-daemon` | `false` | Run server as background daemon |
| `-logfile` | `""` | Log file path (stdout if empty) |

**Example output:**
```
[TWAMP-Light-Client] 2025/06/01 12:00:00.000000 Starting TWAMP-Light test to 2001:db8::1 (count=10 interval=1s timeout=5s padding=0)
[TWAMP-Light-Client] 2025/06/01 12:00:00.012345 seq=1 RTT=0.823ms
...
[TWAMP-Light-Client] 2025/06/01 12:00:09.015432 === TWAMP-Light Test Statistics ===
[TWAMP-Light-Client] 2025/06/01 12:00:09.015433 Packets sent:     10
[TWAMP-Light-Client] 2025/06/01 12:00:09.015434 Packets received: 10
[TWAMP-Light-Client] 2025/06/01 12:00:09.015435 Packet loss:      0.0%
[TWAMP-Light-Client] 2025/06/01 12:00:09.015436 RTT min/avg/max:  0.778 / 0.812 / 0.857 ms
[TWAMP-Light-Client] 2025/06/01 12:00:09.015437 Std deviation:    0.024 ms
[TWAMP-Light-Client] 2025/06/01 12:00:09.015438 Mean jitter:      0.018 ms
```

**How RTT is calculated:**
```
RTT = (T4 − T1) − (T3 − T2)

T1 = Client send timestamp
T2 = Reflector receive timestamp
T3 = Reflector send timestamp
T4 = Client receive timestamp
```

Subtracting (T3 − T2) removes reflector processing time from the measurement.

### `piccolo-perf bw`

TCP throughput measurement. The native tester is always available; iperf3 is used when found in `$PATH` and `-prefer-iperf3` is set.

```sh
# Server (TCP sink on port 5201)
piccolo-perf bw -mode server
piccolo-perf bw -mode server -port 9000

# Client
piccolo-perf bw -target 192.168.1.1           # native, port 5201
piccolo-perf bw -target 192.168.1.1 -prefer-iperf3
piccolo-perf bw -target [2001:db8::1]:5201
piccolo-perf bw -target 192.168.1.1 -duration 10s
```

**bw flags:**

| Flag | Default | Description |
|---|---|---|
| `-mode` | `client` | `client` or `server` |
| `-target` | — | Target address[:port] (client mode) |
| `-port` | `5201` | Listen port (server mode) |
| `-duration` | `5s` | Test duration |
| `-prefer-iperf3` | `false` | Use iperf3 when available; fall back to native |

### `piccolo-perf trace`

Per-hop RTT using ICMP TTL/HopLimit-increment probing. Supports IPv4 (ICMPv4 Time Exceeded) and IPv6 (ICMPv6 Time Exceeded). Requires `CAP_NET_RAW` or root; degrades gracefully without it (returns `skipped=true`).

```sh
sudo piccolo-perf trace -target 2001:db8::1
sudo piccolo-perf trace -target 192.0.2.1 -max-hops 20 -probes 3
```

| Flag | Default | Description |
|---|---|---|
| `-target` | — | Target address (required) |
| `-max-hops` | `30` | Maximum TTL |
| `-probes` | `1` | Probes per hop (min RTT reported) |
| `-timeout` | `2s` | Per-hop timeout |

### `piccolo-perf mtu`

Effective path MTU discovery via ICMP binary search with the DF bit set (IPv4) or implicit fragmentation prevention (IPv6). Supports both address families. Requires `CAP_NET_RAW` or root.

```sh
sudo piccolo-perf mtu -target 192.0.2.1
sudo piccolo-perf mtu -target 2001:db8::1 -ceiling 9000
```

| Flag | Default | Description |
|---|---|---|
| `-target` | — | Target address (required) |
| `-ceiling` | `1500` | Upper bound for binary search (bytes) |
| `-timeout` | `2s` | Per-probe timeout |

### `piccolo-perf dns`

DNS resolution latency per resolver per name. Bypasses the system resolver to measure each target independently.

```sh
piccolo-perf dns -resolver 2620:fe::fe -name example.com
piccolo-perf dns -resolver 9.9.9.9 -name google.com -timeout 1s
```

| Flag | Default | Description |
|---|---|---|
| `-resolver` | `2620:fe::fe` | Resolver IP (Quad9 IPv6 default) |
| `-name` | `example.com` | Name to resolve |
| `-timeout` | `2s` | Query timeout |

### `piccolo-perf agent`

Daemon mode: runs a TWAMP-Light reflector and schedules all configured measurement types, pushing results to InfluxDB and/or exposing them via Prometheus. Config is fetched over HTTP and live-reloaded.

```sh
piccolo-perf agent -config-url http://config-server/piccolo-config.json
piccolo-perf agent -config-url http://config-server/piccolo-config.json -hostname probe-a
```

| Flag | Default | Description |
|---|---|---|
| `-config-url` | — | HTTP URL of topology JSON (required) |
| `-hostname` | auto-detected | Override hostname used for topology lookup |
| `-config-refresh` | from config | Config re-fetch interval override |
| `-port` | `862` | TWAMP-Light UDP port |
| `-no-sync` | `false` | Assert clock is NOT NTP-synchronized |
| `-logfile` | `""` | Log file path (stdout if empty) |

## Agent Mode: Distributed Measurement

Agent mode turns piccolo-perf into a lightweight perfSONAR-style mesh prober. Each host runs one agent process that simultaneously reflects TWAMP packets from peers and actively probes all configured targets.

### Architecture

```
┌─────────────────────────────────────────┐
│            Each Probe Host              │
│                                         │
│  piccolo-perf agent                     │
│  ├── TWAMP-Light reflector (UDP)        │
│  ├── BwServer (TCP sink, port 5201)     │
│  ├── Per-measurement schedulers         │
│  │   ├── TwampMeasurer                  │
│  │   ├── BwMeasurer                     │
│  │   ├── TraceMeasurer                  │
│  │   ├── MtuMeasurer                    │
│  │   └── DnsMeasurer                   │
│  ├── Config poller (HTTP, live-reload)  │
│  ├── InfluxDB writer                    │
│  ├── Prometheus /metrics               │
│  └── Local JSONL resilience store      │
└──────────┬──────────────────┬───────────┘
           │ pull config      │ push metrics
           ▼                  ▼
  ┌──────────────┐    ┌──────────────┐
  │ Config Server│    │   InfluxDB   │◀─── Grafana
  │ (static JSON │    └──────────────┘
  │  via HTTP)   │    ┌──────────────┐
  └──────────────┘    │  Prometheus  │ (scrape /metrics)
                      └──────────────┘
```

### Config File

Served as JSON from any HTTP server (nginx, caddy, a static file host):

```json
{
  "topology": "mesh",
  "config_refresh": "5m",
  "hide_skipped": false,
  "hosts": [
    { "name": "probe-a", "address": "10.0.0.1", "site": "us-east" },
    { "name": "probe-b", "address": "10.0.0.2", "site": "us-west" }
  ],
  "hub_spoke": { "enabled": false, "hub": "probe-a" },

  "measurements": [
    {
      "type": "twamp", "interval": "60s", "targets": "all",
      "burst_size": 5, "burst_interval": "200ms", "packet_timeout": "5s"
    },
    {
      "type": "bw", "interval": "300s", "targets": "all",
      "duration": "5s", "prefer_iperf3": true
    },
    {
      "type": "trace", "interval": "600s", "targets": "all",
      "max_hops": 30, "probes_per_hop": 1, "timeout": "2s"
    },
    {
      "type": "mtu", "interval": "600s", "targets": "all", "ceiling": 1500
    },
    {
      "type": "dns", "interval": "120s",
      "resolvers": ["2620:fe::fe", "2606:4700:4700::1111"],
      "names": ["example.com"]
    }
  ],

  "influxdb": {
    "url": "http://influxdb.example.com:8086",
    "token": "your-token",
    "org": "myorg",
    "bucket": "piccolo"
  },

  "local_store": {
    "enabled": false,
    "path": "/var/lib/piccolo-perf/results.jsonl",
    "max_lines": 10000
  }
}
```

**Topology modes:**

- `"topology": "mesh"` — every host probes every other host
- `"topology": "hub-spoke"` with `"hub_spoke": { "enabled": true, "hub": "probe-a" }` — spokes probe only the hub; hub probes all spokes

**`hide_skipped`:** when `true`, results with `skipped=true` (e.g. MTU/trace without `CAP_NET_RAW`) are suppressed from InfluxDB and Prometheus. They are always logged locally regardless.

**`local_store`:** flat JSONL ring buffer on disk. Results are written before upstream send and replayed to InfluxDB when connectivity returns. Safe on read-write filesystems; silently skipped on read-only. Useful on intermittently-connected edge devices.

### Deployment: systemd

```sh
sudo cp deploy/piccolo-perf-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now piccolo-perf-agent
```

Edit `/etc/systemd/system/piccolo-perf-agent.service` to set your `-config-url`. The unit grants `CAP_NET_BIND_SERVICE` (port 862) and `CAP_NET_RAW` (MTU/trace probing) via `AmbientCapabilities`, so root is not required.

### Deployment: OpenWrt

```sh
cp piccolo-perf /usr/local/bin/
cp deploy/procd-init /etc/init.d/piccolo-perf
chmod +x /etc/init.d/piccolo-perf
/etc/init.d/piccolo-perf enable
/etc/init.d/piccolo-perf start
```

Use a static binary from the releases page matching your router's architecture (mips, mipsle, mips64, arm, arm64).

### Deployment: Docker

```sh
docker build -f deploy/Dockerfile -t piccolo-perf .

# --network host required for accurate RTT on Linux
docker run --network host \
  --cap-add NET_RAW \
  piccolo-perf agent \
  -config-url http://config-server/piccolo-config.json
```

`--cap-add NET_RAW` is needed for MTU discovery and traceroute. Without it those measurements degrade to `skipped=true`.

## Prometheus Exporter

piccolo-perf can expose all measurement results as Prometheus metrics on a `/metrics` HTTP endpoint. Every configured measurement type (TWAMP, bandwidth, traceroute, MTU, DNS) is exported — not just TWAMP.

### How it works

The exporter runs three things simultaneously on each probe host:

1. **TWAMP-Light reflector** — reflects UDP packets from other probes (passive)
2. **Bandwidth sink** — TCP listener on port 5201 accepting test connections from peers (passive)
3. **Measurement schedulers** — actively probes all configured targets on their configured intervals, caches the results, and serves them on `/metrics` when Prometheus scrapes

Prometheus scrapes `/metrics` on each probe host. The probe host does not push to Prometheus — Prometheus pulls from it.

### Step 1 — Create a config file

The exporter uses the same config file as agent mode. Create it on a web server accessible from all probe hosts (nginx, caddy, or any static file host):

```json
{
  "topology": "mesh",
  "config_refresh": "5m",
  "hosts": [
    { "name": "probe-a", "address": "2001:db8::1", "site": "site-a" },
    { "name": "probe-b", "address": "2001:db8::2", "site": "site-b" }
  ],
  "hub_spoke": { "enabled": false, "hub": "" },
  "measurements": [
    {
      "type": "twamp", "interval": "60s", "targets": "all",
      "burst_size": 5, "burst_interval": "200ms", "packet_timeout": "5s"
    },
    {
      "type": "dns", "interval": "120s",
      "resolvers": ["2620:fe::fe", "9.9.9.9"],
      "names": ["example.com"]
    }
  ],
  "influxdb": { "url": "", "token": "", "org": "", "bucket": "" }
}
```

The `influxdb` block can be left blank if you are only using Prometheus.

### Step 2 — Start the exporter on each probe host

```sh
# Run in the foreground (test it first)
sudo piccolo-perf twamp -mode exporter \
  -config-url http://config-server/piccolo-config.json \
  -probe-mode background \
  -metrics-addr :9862
```

Verify it is working:

```sh
curl http://localhost:9862/metrics | grep piccolo_twamp
```

You should see output like:

```
piccolo_twamp_rtt_avg_ms{source="probe-a",target="probe-b",...} 1.234
piccolo_twamp_loss_pct{source="probe-a",target="probe-b",...} 0
```

### Step 3 — Install as a systemd service

```sh
sudo cp deploy/piccolo-perf-exporter.service /etc/systemd/system/

# Edit the service file to set your config URL
sudo nano /etc/systemd/system/piccolo-perf-exporter.service

sudo systemctl daemon-reload
sudo systemctl enable --now piccolo-perf-exporter

# Check it started correctly
sudo systemctl status piccolo-perf-exporter
curl http://localhost:9862/metrics | head -20
```

### Step 4 — Point Prometheus at each probe host

Add a scrape job to your Prometheus configuration (`prometheus.yml`):

```yaml
scrape_configs:
  - job_name: piccolo_perf
    static_configs:
      - targets:
          - probe-a:9862
          - probe-b:9862
    scrape_interval: 60s
    scrape_timeout: 10s
```

Then reload Prometheus:

```sh
curl -X POST http://localhost:9090/-/reload
# or
sudo systemctl reload prometheus
```

Open the Prometheus UI at `http://your-prometheus:9090` and query:

```
piccolo_twamp_rtt_avg_ms
piccolo_twamp_loss_pct
piccolo_dns_dns_rtt_ms
```

### Step 5 — Visualize in Grafana

Import the pre-built dashboards from `deploy/grafana/`. See `deploy/grafana/README.md` for import instructions. The dashboards use InfluxDB by default; for Prometheus, point the queries at your Prometheus datasource instead.

### Probe modes

| Mode | When to use |
|---|---|
| `background` (default) | **Recommended.** Probes run continuously in the background on their configured intervals. Scrapes return cached results instantly with no added latency. |
| `scrape` | Each Prometheus scrape triggers a fresh run of all measurers before responding. Use when you need results at exactly the scrape interval. Increase `scrape_timeout` accordingly (see below). |
| `dual` | Background scheduling; results pushed to both Prometheus and InfluxDB simultaneously. Use when you want both reporting backends from a single process. |

### With TLS

If Prometheus requires HTTPS from targets:

```sh
sudo piccolo-perf twamp -mode exporter \
  -config-url http://config-server/piccolo-config.json \
  -metrics-addr :9862 \
  -metrics-tls-cert /etc/piccolo-perf/server.crt \
  -metrics-tls-key  /etc/piccolo-perf/server.key
```

Prometheus scrape config with TLS:

```yaml
scrape_configs:
  - job_name: piccolo_perf
    scheme: https
    tls_config:
      insecure_skip_verify: true   # or ca_file: /path/to/ca.crt
    static_configs:
      - targets: [probe-a:9862, probe-b:9862]
    scrape_interval: 60s
    scrape_timeout: 30s
```

### `scrape` mode timeout guidance

In `scrape` mode, each Prometheus scrape runs all configured measurers sequentially before responding. Set `scrape_timeout` to at least the sum of all measurement timeouts multiplied by the number of targets.

Example: 3 targets, TWAMP (5s timeout), trace (2s), MTU (2s) = 3 × 9s = 27s minimum. Use 30s.

In `background` mode (the default) the timeout can stay short (10s) since cached results are returned immediately.

### Metrics reference

All metrics include labels: `source`, `target`, `site`, `topology`, plus the measurement-specific tags listed below.

| Metric | Extra labels | Description |
|---|---|---|
| `piccolo_twamp_rtt_min_ms` | — | Minimum RTT in burst (ms) |
| `piccolo_twamp_rtt_avg_ms` | — | Average RTT (ms) |
| `piccolo_twamp_rtt_max_ms` | — | Maximum RTT (ms) |
| `piccolo_twamp_rtt_stddev_ms` | — | RTT standard deviation (ms) |
| `piccolo_twamp_jitter_ms` | — | Mean absolute jitter / IPDV (ms) |
| `piccolo_twamp_loss_pct` | — | Packet loss percentage |
| `piccolo_twamp_packets_sent` | — | Packets sent per burst |
| `piccolo_twamp_packets_recv` | — | Packets received per burst |
| `piccolo_bw_bw_tx_mbps` | `method=native\|iperf3` | Transmit throughput (Mbps) |
| `piccolo_bw_bw_rx_mbps` | `method=iperf3` | Receive throughput — iperf3 only (Mbps) |
| `piccolo_bw_bw_duration_s` | `method=native\|iperf3` | Test duration (s) |
| `piccolo_trace_trace_hops` | `skipped=false\|true` | Furthest responding hop number |
| `piccolo_trace_trace_complete` | `skipped=false\|true` | 1.0 if destination reached, 0.0 otherwise |
| `piccolo_trace_hop_N_rtt_ms` | `skipped=false\|true` | RTT to hop N (ms); -1.0 if no response |
| `piccolo_mtu_mtu_effective_bytes` | `skipped=false\|true` | Effective path MTU (bytes) |
| `piccolo_mtu_mtu_ceiling_bytes` | `skipped=false\|true` | Configured ceiling (bytes) |
| `piccolo_dns_dns_rtt_ms` | `resolver=<ip>`, `name=<fqdn>` | Resolver query latency (ms) |
| `piccolo_dns_dns_success` | `resolver=<ip>`, `name=<fqdn>` | 1.0 on success, 0.0 on failure |
| `twamp_reflected_packets_total` | `source=<hostname>` | Cumulative packets reflected by this host |

Metrics tagged `skipped=true` appear when `CAP_NET_RAW` is unavailable (MTU, traceroute). Set `hide_skipped: true` in the config to suppress them entirely.

## IPv6

All measurement types support both IPv4 and IPv6. The test suite runs in IPv6-only environments.

| Measurer | IPv4 | IPv6 |
|---|---|---|
| TWAMP | `udp4` socket | `udp6` socket |
| Bandwidth | `tcp` listener | `tcp6` listener (IPv4 fallback) |
| Traceroute | `ip4:icmp` + TTL | `ip6:ipv6-icmp` + HopLimit |
| MTU | ICMPv4 + DF bit | ICMPv6 Packet Too Big (DF implicit in IPv6) |
| DNS | Works | Works — use `[addr]:53` format handled automatically |

When a hostname resolves to both A and AAAA records, address selection follows the OS policy table per RFC 6724 (IPv6 is typically preferred on dual-stack hosts).

The default DNS resolver is `2620:fe::fe` (Quad9 IPv6), reachable in IPv6-only environments.

### RFC 4941 Temporary Addresses

Hosts with IPv6 privacy extensions (RFC 4941) enabled have multiple addresses per interface — a stable address and one or more temporary addresses. The OS may choose a temporary address as the source for outbound packets, causing TWAMP replies to arrive from a different address than the one the client dialed.

piccolo-perf handles this correctly: the TWAMP client uses an unconnected socket and demultiplexes responses by sequence number rather than source address, so replies from any of the server's addresses are accepted.

To explicitly pin the source address and ensure consistent measurements (recommended for agent deployments):

```sh
# Pin to a stable ULA address
piccolo-perf twamp -mode client \
  -server fd68:1e02:dc1a:4::5 \
  -source fd68:1e02:dc1a:5:dea6:32ff:fe15:f1ef \
  -port 8620
```

The `-source` flag is also supported in the `twamp` subcommand. For agent mode, configure stable addresses in `hosts[].address` in the config file.

## Capabilities

| Measurement | Requires | Without |
|---|---|---|
| TWAMP | UDP port 862 (or `-port` for high port) | N/A — always works |
| Bandwidth (native) | TCP port 5201 | N/A — always works |
| Bandwidth (iperf3) | `iperf3` in `$PATH` | Silent fallback to native |
| MTU | `CAP_NET_RAW` or root | `skipped=true`, no measurement |
| Traceroute | `CAP_NET_RAW` or root | `skipped=true`, no measurement |
| DNS | None | N/A — always works |

Grant raw socket capability without running as root:

```sh
sudo setcap cap_net_raw,cap_net_bind_service+ep /usr/local/bin/piccolo-perf
```

## Scheduling with Cron

```sh
# TWAMP: every 5 minutes, 5 samples
*/5 * * * * /usr/local/bin/piccolo-perf twamp -mode client -server 2001:db8::1 \
    -count 5 -logfile /var/log/piccolo-perf.log
```

## Building and Testing

```sh
go test ./...
go build -o piccolo-perf .
```

72 tests covering NTP conversion, packet marshal/unmarshal, RTT calculation, rate limiter, allowlist parsing, all five measurer types, loopback TWAMP integration, `-source` flag binding, BwServer lifecycle, IPv6 address parsing, LocalStore error handling, Prometheus dynamic gauges, DNS IPv6 resolver formatting, and config parsing. The test suite runs in IPv6-only environments (loopback tests use `::1`).

Run a specific subset:

```sh
go test ./... -run TestTwamp   # TWAMP tests only
go test ./... -run TestBw      # bandwidth tests
go test ./... -short           # skip loopback integration tests
```

## Backward Compatibility

Existing `tinytwamp` flag-style invocations still work via a compatibility shim:

```sh
piccolo-perf -mode server          # deprecated, still works
piccolo-perf -mode client -server 2001:db8::1
```

A deprecation warning is printed. The new subcommand form is preferred:

```sh
piccolo-perf twamp -mode server
piccolo-perf twamp -mode client -server 2001:db8::1
```

## Limitations

- **No TWAMP-Control** — TWAMP-Light only; not interoperable with full TWAMP implementations requiring the TCP control session
- **Unauthenticated mode only** — RFC 5357 §6 authenticated/encrypted modes not implemented
- **TTL not extracted in TWAMP reflector** — reports TTL=64 (default); actual received TTL requires a raw socket
- **No one-way delay** — TWAMP-Light measures RTT; OWAMP (RFC 4656) would require tightly synchronized clocks
- **Native bandwidth: single TCP stream** — no parallel streams; iperf3 mode supports them

## References

- [RFC 5357](https://www.rfc-editor.org/rfc/rfc5357.html) — TWAMP
- [RFC 4656](https://www.rfc-editor.org/rfc/rfc4656.html) — OWAMP
- [RFC 6038](https://www.rfc-editor.org/rfc/rfc6038.html) — TWAMP Reflect Octets
- [perfSONAR](https://www.perfsonar.net/) — the inspiration

## License

[LICENSE](LICENSE.md)

## AI-Assisted Development Notice

This software contains components generated by Large Language Model (LLM) or machine intelligence platforms. AI tools were used to assist in various stages of the development lifecycle. A human reviewed all AI-generated content. Code generated by LLMs may occasionally contain subtle logical errors or inefficiencies that were not detected during review.
