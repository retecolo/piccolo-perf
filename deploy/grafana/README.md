# piccolo-perf Grafana Dashboards

Grafana dashboards for long-term visualization of all piccolo-perf measurement types. All dashboards use InfluxDB v2 (Flux query language) as the data source.

## Dashboards

| File | UID | Coverage |
|---|---|---|
| `overview.json` | `piccolo-overview` | Fleet-wide summary of all measurement types on one screen |
| `twamp.json` | `piccolo-twamp` | RTT min/avg/max, jitter, std deviation, packet loss, RTT heatmap |
| `bandwidth.json` | `piccolo-bandwidth` | TX/RX throughput over time, method breakdown (native vs iperf3), summary table |
| `dns.json` | `piccolo-dns` | Resolver RTT over time, p50/p95/p99 percentiles, success rate, per-resolver summary |
| `mtu-trace.json` | `piccolo-mtu-trace` | Path MTU over time, MTU change detection, hop count, per-hop RTT, trace completeness |

## Import

### Via Grafana UI

1. Go to **Dashboards → Import**
2. Click **Upload JSON file** and select a file from this directory
3. Select your **InfluxDB** datasource from the dropdown (mapped to `DS_INFLUXDB`)
4. Click **Import**

### Via Grafana API

```sh
GRAFANA_URL="http://localhost:3000"
GRAFANA_TOKEN="your-service-account-token"
DATASOURCE_UID="your-influxdb-datasource-uid"

for f in *.json; do
  payload=$(jq --arg ds "$DATASOURCE_UID" \
    '.dashboard |= . + {} | {dashboard: .dashboard, overwrite: true, inputs: [{"name": "DS_INFLUXDB", "type": "datasource", "pluginId": "influxdb", "value": $ds}]}' \
    <(jq '{dashboard: .}' "$f"))
  curl -s -X POST "$GRAFANA_URL/api/dashboards/import" \
    -H "Authorization: Bearer $GRAFANA_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$payload" | jq '.status'
done
```

### Via Grafana provisioning (recommended for persistent deployments)

Create `/etc/grafana/provisioning/dashboards/piccolo-perf.yaml`:

```yaml
apiVersion: 1
providers:
  - name: piccolo-perf
    type: file
    disableDeletion: false
    updateIntervalSeconds: 30
    allowUiUpdates: true
    options:
      path: /var/lib/grafana/dashboards/piccolo-perf
      foldersFromFilesStructure: false
```

Copy the JSON files to `/var/lib/grafana/dashboards/piccolo-perf/` and restart Grafana. Dashboards will appear automatically in a `piccolo-perf` folder.

## Data Source Setup

All dashboards expect an InfluxDB v2 datasource configured with:

| Field | Value |
|---|---|
| URL | `http://influxdb:8086` (or your InfluxDB address) |
| Organisation | Your InfluxDB org |
| Token | Your InfluxDB API token |
| Default bucket | `piccolo` (or match your config) |
| Query language | **Flux** |

The bucket name is set via a `bucket` template variable in each dashboard (default: `piccolo`). Update it to match your InfluxDB bucket name if different.

## Variables

Every dashboard exposes these template variables for filtering:

| Variable | Description |
|---|---|
| `bucket` | InfluxDB bucket name (constant — edit to change) |
| `source` | Probe host sending measurements |
| `target` | Probe host or resolver being measured |

Measurement-specific variables (e.g. `resolver`, `name` for DNS; `method` for bandwidth) are present in the relevant dashboards.

## Recommended Retention Policy

| Measurement interval | Suggested retention |
|---|---|
| 60s TWAMP probes | 90 days raw; 1 year downsampled to 5m |
| 300s bandwidth | 1 year raw |
| 600s MTU/trace | 1 year raw |
| 120s DNS | 90 days raw; 1 year downsampled to 10m |

Configure downsampling tasks in InfluxDB to keep long-term history without unbounded storage growth.
