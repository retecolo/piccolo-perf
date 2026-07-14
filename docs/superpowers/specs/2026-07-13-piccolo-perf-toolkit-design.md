# piccolo-perf — Lightweight Network Performance Toolkit Design Spec

**Date:** 2026-07-13
**Status:** Approved
**Approach:** Option B — Shared `Measurer` Interface

---

## 1. Overview

Expand `tinytwamp` into `piccolo-perf`: a self-contained, resource-constrained network performance toolkit inspired by perfSONAR. The binary runs on anything from OpenWrt MIPS routers and ARM SBCs to full Linux servers. It delivers TWAMP-Light latency/jitter/loss (existing), TCP/UDP bandwidth testing, path MTU discovery, traceroute hop-latency, and DNS resolution timing — all from a single static binary with no required runtime dependencies.

The central architectural change is a `Measurer` interface that makes the agent scheduler and reporting pipeline measurement-agnostic. New measurement types plug in without changing the scheduler, InfluxDB writer, or Prometheus store.

---

## 2. Binary Identity & Command Structure

| Item | Old | New |
|---|---|---|
| Binary | `tinytwamp` | `piccolo-perf` |
| Go module | `github.com/buraglio/tiny-twamp` | `github.com/buraglio/piccolo-perf` |
| Systemd units | `tinytwamp-*.service` | `piccolo-perf-*.service` |
| InfluxDB measurements | `twamp_rtt` | `piccolo_twamp`, `piccolo_bw`, `piccolo_trace`, `piccolo_mtu`, `piccolo_dns` |
| Prometheus job | `twamp` | `piccolo_perf` |

### Subcommand Routing

The flat `-mode` flag is replaced with a subcommand structure:

```
piccolo-perf twamp    [client|server|agent|exporter]  # existing TWAMP-Light
piccolo-perf bw       [client|server]                 # bandwidth testing
piccolo-perf trace    <target>                        # traceroute / hop latency
piccolo-perf mtu      <target>                        # path MTU discovery
piccolo-perf dns      <target>                        # DNS resolution timing
piccolo-perf agent                                    # multi-measurement daemon
```

**Backward compatibility:** `piccolo-perf twamp` accepts the old flat flag style (`-mode client`, `-server`, etc.) and emits a one-time deprecation warning. Old configs and systemd units work for one release cycle.

---

## 3. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                      Each Probe Host                             │
│                                                                  │
│  piccolo-perf agent                                              │
│  ├── TWAMP reflector        (UDP, existing)                      │
│  ├── bw server              (TCP listener, when scheduled)       │
│  ├── Probe scheduler        (one ticker per Measurer type)       │
│  │   ├── TwampMeasurer.Run()                                     │
│  │   ├── BwMeasurer.Run()                                        │
│  │   ├── TraceMeasurer.Run()                                     │
│  │   ├── MtuMeasurer.Run()                                       │
│  │   └── DnsMeasurer.Run()                                       │
│  ├── Config poller          (HTTP JSON, live-reload)             │
│  ├── InfluxDB writer        (generic MeasureResult batching)     │
│  ├── Prometheus store       (dynamic gauge registration)         │
│  └── Local resilience store (flat JSONL ring buffer, opt-in)     │
└───────────────┬─────────────────────┬────────────────────────────┘
                │ pull config         │ push metrics
                ▼                     ▼
       ┌──────────────┐      ┌──────────────┐
       │ Config Server│      │   InfluxDB   │◀─── Grafana
       │ (static JSON │      │              │
       │  via HTTP)   │      └──────────────┘
       └──────────────┘
                              ┌──────────────┐
                              │  Prometheus  │ (scrape /metrics)
                              └──────────────┘
```

---

## 4. The `Measurer` Interface

Defined in `measurer.go`. All measurement types implement it.

```go
type Measurer interface {
    // Name returns the measurement type tag ("twamp", "bw", "trace", "mtu", "dns").
    Name() string
    // Run executes one probe burst against target and returns results.
    Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error)
}

// MeasurerConfig carries per-measurement tuning parameters parsed from the config block.
type MeasurerConfig struct {
    // Common
    Timeout  time.Duration
    // TWAMP
    BurstSize     int
    BurstInterval time.Duration
    Padding       int
    // Bandwidth
    Duration     time.Duration
    PreferIperf3 bool
    // Trace
    MaxHops      int
    ProbesPerHop int
    // MTU
    Ceiling int
    // DNS
    Resolvers []string
    Names     []string
}

// MeasureResult is the unified output type for all measurers.
type MeasureResult struct {
    Measurement string             // "twamp", "bw", "trace", "mtu", "dns"
    Source      string             // this host
    Target      string             // probe target (hostname, IP, or resolver)
    Site        string             // from HostEntry
    Topology    string             // from AgentConfig
    Tags        map[string]string  // e.g. {"method": "native"}, {"resolver": "8.8.8.8"}
    Fields      map[string]float64 // all numeric results
    SentAt      time.Time
}
```

The InfluxDB writer and Prometheus store consume `MeasureResult` generically — no per-measurement-type code in the reporting layer.

---

## 5. Measurement Modules

### 5.1 TWAMP (existing, wrapped)

File: `twamp.go` — thin wrapper around existing `Client.runBurst()`.

Fields emitted: `rtt_min_ms`, `rtt_avg_ms`, `rtt_max_ms`, `rtt_stddev_ms`, `jitter_ms`, `loss_ratio`, `packets_sent`, `packets_recv`.

### 5.2 Bandwidth (`bw`)

File: `bw.go`

**Native Go tester (always available):**
- Client: TCP connection, fixed 64 KB send buffer, streams for configurable duration (default 5s), measures throughput.
- Server: TCP listener, reads and discards, records receive rate.
- UDP mode: fixed-rate datagram send, loss and achieved throughput measured at receiver.
- Resource constraint: single goroutine per connection, no parallel streams — safe on 16 MB RAM devices.
- Fields: `bw_tx_mbps`, `bw_rx_mbps`, `bw_duration_s`. Tag: `method=native`.

**iperf3 shim (opportunistic):**
- Detected via `exec.LookPath("iperf3")` at agent startup; result logged once.
- Client: `iperf3 -c <target> -J -t <duration>`, JSON output parsed.
- Server: `iperf3 -s -J --one-off` managed as subprocess.
- Same fields normalized: `bw_tx_mbps`, `bw_rx_mbps`. Tag: `method=iperf3`.
- Absent or failed iperf3 degrades silently to native with no measurement gap.

### 5.3 Path MTU Discovery (`mtu`)

File: `mtu.go`

- Sends ICMP Echo Request with DF bit set, binary-searches from ceiling (default 1500) downward until no "Fragmentation Needed" is returned.
- Requires `CAP_NET_RAW` or root. Without it: logs one warning, skips MTU measurements, agent continues.
- Fallback: TCP MSS probe for platforms where raw sockets are unavailable.
- Fields: `mtu_effective_bytes`, `mtu_ceiling_bytes`.

### 5.4 Traceroute (`trace`)

File: `trace.go`

- UDP/ICMP TTL-increment probing. Max 30 hops, configurable probes per hop (default 1 on constrained devices, 3 otherwise).
- Reports per-hop RTT as labeled fields: `hop_1_rtt_ms`, `hop_2_rtt_ms`, … `hop_N_rtt_ms` (up to max_hops).
- Also reports: `trace_hops` (total responding hops), `trace_complete` (1.0 if target reached).
- Requires `CAP_NET_RAW`. Same graceful degradation as MTU.
- Runs sequentially (no parallel hop probing) to minimize memory on constrained devices.

### 5.5 DNS Timing (`dns`)

File: `dns.go`

- Resolves configurable names against specified resolvers using Go's `net` package directly (bypasses system resolver for accurate per-resolver timing).
- Measures: query RTT, success/failure, NXDOMAIN vs SERVFAIL vs timeout.
- Fields: `dns_rtt_ms`, `dns_success` (1.0/0.0), `dns_ttl_s`.
- Tags: `resolver=<ip>`, `name=<queried_name>`.
- No raw socket required — works everywhere with zero privilege requirements.

---

## 6. Reporting Pipeline

### 6.1 InfluxDB Writer (`influx.go` — refactored)

`lineProtocol()` generalized to operate on `MeasureResult`:

```
piccolo_<measurement>,source=X,target=Y,site=Z,topology=T,<tags...> <fields...> <timestamp_ns>
```

Batching, retry, and flush logic unchanged (batch 100, flush every 10s, 3-attempt exponential backoff).

### 6.2 Prometheus Store (`prom.go` — refactored)

Dynamic gauge registration on first result seen, keyed by `measurement + "_" + field_name`. Label set: `source`, `target`, `site`, `topology`, plus all keys from `MeasureResult.Tags`.

No hardcoded metric names — new measurement types appear in `/metrics` automatically.

### 6.3 Local Resilience Store (`store.go` — new)

Purpose: survive upstream outages on intermittently-connected edge devices.

**Format:** newline-delimited JSON (`.jsonl`), one `MeasureResult` per line.

**Write path:**
- Every `MeasureResult` appended to store file before upstream send.
- Cap enforced at `max_lines` (default 10,000 ≈ ~5 MB). Cap rewrite: read last N lines (~500 KB), overwrite file. Safe on constrained RAM.

**Flush path:**
- Background goroutine monitors upstream health (InfluxDB write success).
- On connectivity restoration: replay in chronological order, batch 100 at a time, delete flushed lines.
- On read-only filesystem: single warning log, store silently skipped.

**Disabled by default.** Opt-in via config.

---

## 7. Agent Scheduler (extended)

The probe scheduler in `agent.go` is extended to:

1. Parse `measurements[]` from config into a list of `(Measurer, MeasurerConfig, interval, targets)` tuples.
2. Start one `time.Ticker` per measurement type.
3. On each tick, dispatch `Measurer.Run()` for each target, send results to `resultsCh`.
4. Unknown `type` values logged and skipped (forward compat).

Targets field per measurement:
- `"all"` — every host in `hosts[]` except self (mesh behavior)
- `"hub-only"` — hub host only (spoke behavior)
- DNS measurements use `resolvers[]` + `names[]` instead of `hosts[]`

---

## 8. Full Config Schema

```json
{
  "topology": "mesh",
  "config_refresh": "5m",
  "hosts": [
    { "name": "probe-a", "address": "10.0.0.1", "site": "us-east" },
    { "name": "probe-b", "address": "10.0.0.2", "site": "us-west" }
  ],
  "hub_spoke": { "enabled": false, "hub": "probe-a" },

  "measurements": [
    {
      "type": "twamp", "interval": "60s", "targets": "all",
      "burst_size": 5, "burst_interval": "200ms", "packet_timeout": "5s", "padding": 0
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
      "type": "mtu", "interval": "600s", "targets": "all",
      "ceiling": 1500
    },
    {
      "type": "dns", "interval": "120s",
      "resolvers": ["8.8.8.8", "1.1.1.1"],
      "names": ["example.com", "google.com"]
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

---

## 9. Capabilities & Graceful Degradation

| Measurer | Requires | Without capability |
|---|---|---|
| TWAMP | UDP port 862 (or high port) | N/A — always works |
| Bandwidth (native) | TCP port (configurable) | N/A — always works |
| Bandwidth (iperf3) | `iperf3` in `$PATH` | Silent fallback to native |
| MTU | `CAP_NET_RAW` or root | Skipped; one log warning |
| Traceroute | `CAP_NET_RAW` or root | Skipped; one log warning |
| DNS | None | N/A — always works |

The agent never exits due to missing capabilities. Degraded measurements are tagged `skipped=true` in the result and reported upstream by default so dashboards surface capability gaps. Set `"hide_skipped": true` in the config root to suppress skipped results from InfluxDB/Prometheus entirely (they are always logged regardless).

---

## 10. Deployment

### systemd (agent mode)

```ini
[Unit]
Description=piccolo-perf Network Performance Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/piccolo-perf agent \
  -config-url http://config-server/piccolo-config.json
Restart=on-failure
RestartSec=10s
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW
NoNewPrivileges=true
DynamicUser=yes

[Install]
WantedBy=multi-user.target
```

### OpenWrt (procd)

Static MIPS/ARM binary from goreleaser releases. `deploy/procd-init` script added for `/etc/init.d/piccolo-perf`.

### Docker

```dockerfile
FROM scratch
COPY piccolo-perf /piccolo-perf
ENTRYPOINT ["/piccolo-perf"]
```

Runtime flags: `--cap-add NET_RAW` for MTU/trace, `--network host` for accurate RTT.

### goreleaser

Existing `.goreleaser.yaml` targets unchanged — linux/mips, linux/arm, linux/arm64, linux/amd64, darwin, windows, FreeBSD, etc. already cover constrained hardware.

---

## 11. Files to Create / Modify

| Path | Action |
|---|---|
| `tinytwamp.go` → `main.go` | Rename; replace flat `-mode` with subcommand routing; backward-compat shim |
| `measurer.go` | New — `Measurer` interface, `MeasureResult`, `MeasurerConfig` types |
| `twamp.go` | New — `TwampMeasurer` wrapper around existing `Client.runBurst()` |
| `bw.go` | New — `BwMeasurer`: native TCP tester + iperf3 shim |
| `trace.go` | New — `TraceMeasurer`: TTL-increment hop latency |
| `mtu.go` | New — `MtuMeasurer`: DF-bit binary search |
| `dns.go` | New — `DnsMeasurer`: per-resolver query timing |
| `store.go` | New — flat JSONL ring buffer with background flush |
| `agent.go` | Extend scheduler for multi-measurer dispatch; `ProbeResult` → `MeasureResult` |
| `influx.go` | Generalize `lineProtocol()` to `MeasureResult` |
| `prom.go` | Dynamic gauge registration from `MeasureResult` |
| `tinytwamp_test.go` → `piccolo_test.go` | Rename; add tests for new measurers and store |
| `go.mod` | Module rename to `github.com/buraglio/piccolo-perf` |
| `deploy/piccolo-perf-agent.service` | New systemd unit |
| `deploy/piccolo-perf-exporter.service` | New systemd unit |
| `deploy/procd-init` | New OpenWrt init script |
| `deploy/config-example.json` | Update to full multi-measurement schema |
| `deploy/grafana-dashboard.json` | Update panels for all measurement types |
| `.goreleaser.yaml` | Update binary name |
| `install.sh` | Update binary name and GitHub repo reference |
| `README.md` | Full rewrite for piccolo-perf |

---

## 12. Out of Scope

- TWAMP-Control protocol (TCP negotiation) — TWAMP-Light only
- Authenticated/encrypted TWAMP modes (RFC 5357 §6)
- One-way delay (OWAMP / RFC 4656) — requires tightly synchronized clocks
- Config server implementation — use any static HTTP server
- TLS for InfluxDB writes (can be added via flag later)
- Web UI or local dashboard — Grafana remains the visualization layer
- Parallel bandwidth streams — single-connection native tester only (resource constraint)
