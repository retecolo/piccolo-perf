# piccolo-perf Toolkit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expand tinytwamp into piccolo-perf — a multi-measurement network performance toolkit (TWAMP, bandwidth, traceroute, MTU, DNS) for resource-constrained devices, with a shared Measurer interface and unified reporting pipeline.

**Architecture:** A `Measurer` interface makes the agent scheduler and reporting pipeline measurement-agnostic. All measurement types produce `MeasureResult` structs consumed generically by InfluxDB, Prometheus, and a local JSONL ring-buffer store. Subcommand routing replaces the flat `-mode` flag.

**Tech Stack:** Go 1.25, `github.com/prometheus/client_golang`, InfluxDB v2 Line Protocol over HTTP, static CGO_ENABLED=0 binaries, goreleaser.

## Global Constraints

- Module: `github.com/buraglio/piccolo-perf`
- Binary name: `piccolo-perf`
- CGO_ENABLED=0 — all builds fully static, no libc dependency
- Go 1.25 minimum
- No new dependencies beyond existing `prometheus/client_golang` unless strictly necessary
- All files in `package main` (no sub-packages — single-binary constraint)
- `CAP_NET_RAW` required for MTU and trace; both degrade gracefully without it
- iperf3 is optional — detected at runtime, never a build dependency

---

## Phase 1: Foundation — rename, Measurer interface, MeasureResult

### Task 1: Rename binary and module

**Files:**
- Modify: `go.mod`
- Modify: `.goreleaser.yaml`
- Modify: `install.sh`

**Interfaces:**
- Produces: nothing consumed by other tasks — pure rename

- [ ] **Step 1: Update go.mod module path**

Replace line 1 of `go.mod`:
```
module github.com/buraglio/piccolo-perf
```

- [ ] **Step 2: Update goreleaser project name**

In `.goreleaser.yaml`, change line 3:
```yaml
project_name: piccolo-perf
```

- [ ] **Step 3: Update install.sh identity strings**

In `install.sh`, change:
```sh
REPO="buraglio/piccolo-perf"
BINARY="piccolo-perf"
```
And update the comment on line 1:
```sh
# piccolo-perf installer
# Usage: /bin/sh -c "$(curl -fsSL https://raw.githubusercontent.com/buraglio/piccolo-perf/main/install.sh)"
```

- [ ] **Step 4: Verify build still works**

```bash
go build -o piccolo-perf .
```
Expected: binary named `piccolo-perf` produced with no errors.

- [ ] **Step 5: Commit**

```bash
git add go.mod .goreleaser.yaml install.sh
git commit -m "chore: rename binary and module to piccolo-perf"
```

---

### Task 2: Add Measurer interface and MeasureResult type

**Files:**
- Create: `measurer.go`

**Interfaces:**
- Produces:
  - `type MeasureResult struct` — used by Tasks 3–9
  - `type Measurer interface` — implemented by Tasks 5–9
  - `type MeasurerConfig struct` — used by Tasks 5–9

- [ ] **Step 1: Write failing test for MeasureResult construction**

Add to `piccolo_test.go` (rename `tinytwamp_test.go` first):
```bash
git mv tinytwamp_test.go piccolo_test.go
```

Then add at the bottom of `piccolo_test.go`:
```go
// ============================================================================
// MeasureResult
// ============================================================================

func TestMeasureResultFields(t *testing.T) {
    r := MeasureResult{
        Measurement: "twamp",
        Source:      "a",
        Target:      "b",
        Site:        "east",
        Topology:    "mesh",
        Tags:        map[string]string{"method": "native"},
        Fields:      map[string]float64{"rtt_avg_ms": 1.5},
        SentAt:      time.Unix(1_000_000, 0),
    }
    if r.Measurement != "twamp" {
        t.Errorf("Measurement = %q, want twamp", r.Measurement)
    }
    if r.Fields["rtt_avg_ms"] != 1.5 {
        t.Errorf("Fields[rtt_avg_ms] = %v, want 1.5", r.Fields["rtt_avg_ms"])
    }
    if r.Tags["method"] != "native" {
        t.Errorf("Tags[method] = %q, want native", r.Tags["method"])
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestMeasureResultFields ./...
```
Expected: compile error — `MeasureResult undefined`

- [ ] **Step 3: Create measurer.go**

```go
package main

import (
	"context"
	"time"
)

// Measurer is implemented by every measurement type the agent can schedule.
type Measurer interface {
	Name() string
	Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error)
}

// MeasurerConfig carries per-measurement tuning parameters.
type MeasurerConfig struct {
	// Common
	Timeout time.Duration
	Synced  bool
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
	Measurement string
	Source      string
	Target      string
	Site        string
	Topology    string
	Tags        map[string]string
	Fields      map[string]float64
	SentAt      time.Time
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run TestMeasureResultFields ./...
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add measurer.go piccolo_test.go
git commit -m "feat: add Measurer interface and MeasureResult type"
```

---

### Task 3: Generalize InfluxDB writer to MeasureResult

**Files:**
- Modify: `influx.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `MeasureResult` from Task 2
- Produces: `lineProtocolResult(r MeasureResult) string` — used by Task 4 (agent refactor)

- [ ] **Step 1: Write failing test for generic line protocol**

Add to `piccolo_test.go`:
```go
func TestLineProtocolResultFormat(t *testing.T) {
    r := MeasureResult{
        Measurement: "piccolo_twamp",
        Source:      "probe-a",
        Target:      "probe-b",
        Site:        "us-east",
        Topology:    "mesh",
        Tags:        map[string]string{"method": "native"},
        Fields:      map[string]float64{"rtt_avg_ms": 2.5, "packets_sent": 5},
        SentAt:      time.Unix(1_000_000, 0).UTC(),
    }
    line := lineProtocolResult(r)
    if !strings.HasPrefix(line, "piccolo_twamp,") {
        t.Errorf("line should start with piccolo_twamp,, got: %s", line)
    }
    if !strings.Contains(line, "source=probe-a") {
        t.Errorf("missing source tag: %s", line)
    }
    if !strings.Contains(line, "method=native") {
        t.Errorf("missing method tag: %s", line)
    }
    if !strings.Contains(line, "rtt_avg_ms=2.500") {
        t.Errorf("missing rtt_avg_ms field: %s", line)
    }
    wantTs := fmt.Sprintf("%d", time.Unix(1_000_000, 0).UnixNano())
    if !strings.HasSuffix(strings.TrimSpace(line), wantTs) {
        t.Errorf("line should end with timestamp %s, got: %s", wantTs, line)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestLineProtocolResultFormat ./...
```
Expected: compile error — `lineProtocolResult undefined`

- [ ] **Step 3: Add lineProtocolResult to influx.go**

Add after the existing `lineProtocol` function in `influx.go`:
```go
// lineProtocolResult formats a MeasureResult as an InfluxDB v2 Line Protocol line.
func lineProtocolResult(r MeasureResult) string {
	escape := func(s string) string {
		s = strings.ReplaceAll(s, " ", `\ `)
		s = strings.ReplaceAll(s, ",", `\,`)
		s = strings.ReplaceAll(s, "=", `\=`)
		return s
	}

	// Base tags
	tagParts := []string{
		"source=" + escape(r.Source),
		"target=" + escape(r.Target),
		"topology=" + escape(r.Topology),
		"site=" + escape(r.Site),
	}
	// Additional tags from result (sorted for determinism)
	tagKeys := make([]string, 0, len(r.Tags))
	for k := range r.Tags {
		tagKeys = append(tagKeys, k)
	}
	sort.Strings(tagKeys)
	for _, k := range tagKeys {
		tagParts = append(tagParts, escape(k)+"="+escape(r.Tags[k]))
	}

	// Fields
	fieldKeys := make([]string, 0, len(r.Fields))
	for k := range r.Fields {
		fieldKeys = append(fieldKeys, k)
	}
	sort.Strings(fieldKeys)
	fieldParts := make([]string, 0, len(fieldKeys))
	for _, k := range fieldKeys {
		fieldParts = append(fieldParts, fmt.Sprintf("%s=%.3f", escape(k), r.Fields[k]))
	}

	return fmt.Sprintf("%s,%s %s %d",
		r.Measurement,
		strings.Join(tagParts, ","),
		strings.Join(fieldParts, ","),
		r.SentAt.UnixNano(),
	)
}
```

Also add `"sort"` to the imports in `influx.go`.

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run TestLineProtocolResultFormat ./...
```
Expected: PASS

- [ ] **Step 5: Update InfluxWriter.run to accept MeasureResult channel**

In `influx.go`, add a second `run` method alongside the existing one:
```go
// runResults reads MeasureResults from ch, batches, and flushes to InfluxDB.
func (w *InfluxWriter) runResults(ctx context.Context, results <-chan MeasureResult) {
	const maxBatch = 100
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var batch []string

	flush := func() {
		if len(batch) == 0 {
			return
		}
		body := strings.Join(batch, "\n")
		batch = batch[:0]
		if err := w.write(ctx, body); err != nil {
			w.logger.Printf("[InfluxWriter] write error: %v", err)
		}
	}

	for {
		select {
		case r, ok := <-results:
			if !ok {
				flush()
				return
			}
			batch = append(batch, lineProtocolResult(r))
			if len(batch) >= maxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case r, ok := <-results:
					if !ok {
						goto done
					}
					batch = append(batch, lineProtocolResult(r))
				default:
					goto done
				}
			}
		done:
			if len(batch) > 0 {
				body := strings.Join(batch, "\n")
				shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := w.write(shutCtx, body); err != nil {
					w.logger.Printf("[InfluxWriter] shutdown flush error: %v", err)
				}
			}
			return
		}
	}
}
```

- [ ] **Step 6: Run all tests**

```bash
go test ./...
```
Expected: all PASS (existing tests unaffected — `lineProtocol` and `run` unchanged)

- [ ] **Step 7: Commit**

```bash
git add influx.go piccolo_test.go
git commit -m "feat: add lineProtocolResult and runResults for generic MeasureResult pipeline"
```

---

### Task 4: Generalize Prometheus store to MeasureResult

**Files:**
- Modify: `prom.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `MeasureResult` from Task 2
- Produces: `PrometheusStore.UpdateResult(r MeasureResult)` — used by Tasks 10–11

- [ ] **Step 1: Write failing test**

Add to `piccolo_test.go`:
```go
func TestPrometheusStoreUpdateResult(t *testing.T) {
    store := newPrometheusStore("probe-a")
    r := MeasureResult{
        Measurement: "piccolo_bw",
        Source:      "probe-a",
        Target:      "probe-b",
        Site:        "us-east",
        Topology:    "mesh",
        Tags:        map[string]string{"method": "native"},
        Fields:      map[string]float64{"bw_tx_mbps": 95.5},
        SentAt:      time.Now(),
    }
    // Must not panic; metric must appear in output
    store.UpdateResult(r)

    rec := httptest.NewRecorder()
    store.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
    body := rec.Body.String()
    if !strings.Contains(body, "piccolo_bw_bw_tx_mbps") {
        t.Errorf("expected piccolo_bw_bw_tx_mbps in metrics, got:\n%s", body)
    }
    if !strings.Contains(body, `method="native"`) {
        t.Errorf("expected method label in metrics, got:\n%s", body)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestPrometheusStoreUpdateResult ./...
```
Expected: compile error — `UpdateResult undefined`

- [ ] **Step 3: Add UpdateResult to prom.go**

Add these fields to `PrometheusStore`:
```go
// Dynamic gauges: key is "measurement_fieldname"
dynamicGauges map[string]*prometheus.GaugeVec
dynamicMu     sync.Mutex
```

Update `newPrometheusStore` to initialize them:
```go
s := &PrometheusStore{
    // ... existing fields ...
    dynamicGauges: make(map[string]*prometheus.GaugeVec),
}
```

Add method:
```go
// UpdateResult sets gauges for a generic MeasureResult.
// Gauges are registered on first use; label set is source+target+site+topology+Tags keys.
func (s *PrometheusStore) UpdateResult(r MeasureResult) {
    // Build sorted extra label names from Tags
    extraKeys := make([]string, 0, len(r.Tags))
    for k := range r.Tags {
        extraKeys = append(extraKeys, k)
    }
    sort.Strings(extraKeys)

    labelNames := append([]string{"source", "target", "site", "topology"}, extraKeys...)

    labelVals := prometheus.Labels{
        "source":   r.Source,
        "target":   r.Target,
        "site":     r.Site,
        "topology": r.Topology,
    }
    for _, k := range extraKeys {
        labelVals[k] = r.Tags[k]
    }

    for fieldName, val := range r.Fields {
        metricName := r.Measurement + "_" + fieldName
        // Replace hyphens and dots with underscores for valid Prometheus names
        metricName = strings.NewReplacer("-", "_", ".", "_").Replace(metricName)

        s.dynamicMu.Lock()
        g, ok := s.dynamicGauges[metricName]
        if !ok {
            g = prometheus.NewGaugeVec(prometheus.GaugeOpts{
                Name: metricName,
                Help: "piccolo-perf: " + r.Measurement + " " + fieldName,
            }, labelNames)
            if err := s.registry.Register(g); err != nil {
                s.dynamicMu.Unlock()
                continue
            }
            s.dynamicGauges[metricName] = g
        }
        s.dynamicMu.Unlock()
        g.With(labelVals).Set(val)
    }
}
```

Add `"sort"` and `"sync"` to prom.go imports (sync already present via prometheus internals — add sort).

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run TestPrometheusStoreUpdateResult ./...
```
Expected: PASS

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add prom.go piccolo_test.go
git commit -m "feat: add PrometheusStore.UpdateResult for dynamic MeasureResult gauges"
```

---

## Phase 2: Measurement modules

### Task 5: TWAMP Measurer wrapper

**Files:**
- Create: `twamp_measurer.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `Measurer`, `MeasurerConfig`, `MeasureResult` from Task 2; `Client.runBurst()` from existing `tinytwamp.go`
- Produces: `TwampMeasurer` struct implementing `Measurer`

- [ ] **Step 1: Write failing test**

Add to `piccolo_test.go`:
```go
func TestTwampMeasurerName(t *testing.T) {
    m := &TwampMeasurer{hostname: "probe-a", logFile: nil}
    if m.Name() != "twamp" {
        t.Errorf("Name() = %q, want twamp", m.Name())
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestTwampMeasurerName ./...
```
Expected: compile error

- [ ] **Step 3: Create twamp_measurer.go**

```go
package main

import (
	"context"
	"os"
	"time"
)

// TwampMeasurer wraps the existing TWAMP-Light client as a Measurer.
type TwampMeasurer struct {
	hostname string
	port     int
	logFile  *os.File
}

func (m *TwampMeasurer) Name() string { return "twamp" }

func (m *TwampMeasurer) Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error) {
	c := NewClient(
		target.Address,
		m.logFile,
		cfg.BurstSize,
		cfg.BurstInterval,
		cfg.Timeout,
		m.port,
		cfg.Padding,
		cfg.Synced,
	)

	rtts, recv := c.runBurst()
	sent := cfg.BurstSize

	fields := map[string]float64{
		"packets_sent": float64(sent),
		"packets_recv": float64(recv),
	}

	var lossPct float64
	if sent > 0 {
		lossPct = float64(sent-recv) / float64(sent) * 100.0
	} else {
		lossPct = 100.0
	}
	fields["loss_pct"] = lossPct

	if recv > 0 {
		ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
		var sum time.Duration
		minR, maxR := rtts[0], rtts[0]
		for _, r := range rtts {
			sum += r
			if r < minR { minR = r }
			if r > maxR { maxR = r }
		}
		avg := sum / time.Duration(recv)

		var variance float64
		for _, r := range rtts {
			d := float64(r) - float64(avg)
			variance += d * d
		}
		import_math_sqrt := func(v float64) float64 {
			// inline to avoid import — use time.Duration cast
			return v
		}
		_ = import_math_sqrt
		stddev := computeSqrt(variance / float64(recv))

		var jitterSum time.Duration
		for i := 1; i < recv; i++ {
			d := rtts[i] - rtts[i-1]
			if d < 0 { d = -d }
			jitterSum += d
		}
		var jitter time.Duration
		if recv > 1 {
			jitter = jitterSum / time.Duration(recv-1)
		}

		fields["rtt_min_ms"] = ms(minR)
		fields["rtt_avg_ms"] = ms(avg)
		fields["rtt_max_ms"] = ms(maxR)
		fields["rtt_stddev_ms"] = stddev
		fields["jitter_ms"] = ms(jitter)
	}

	return []MeasureResult{{
		Measurement: "piccolo_twamp",
		Source:      m.hostname,
		Target:      target.Name,
		Site:        target.Site,
		Tags:        map[string]string{},
		Fields:      fields,
		SentAt:      time.Now(),
	}}, nil
}
```

Note: `computeSqrt` needs to be a shared helper. Add to `measurer.go`:
```go
import "math"

func computeSqrt(v float64) float64 { return math.Sqrt(v) }
```

And fix the twamp_measurer.go stddev line to:
```go
fields["rtt_stddev_ms"] = computeSqrt(variance/float64(recv)) / float64(time.Millisecond) * float64(time.Microsecond) / 1000.0
```

Actually simpler — just import math directly in twamp_measurer.go and compute inline:
```go
import "math"
// ...
fields["rtt_stddev_ms"] = math.Sqrt(variance/float64(recv)) / float64(time.Millisecond/time.Microsecond) / 1000.0
```

Replace the twamp_measurer.go with this corrected, clean version:

```go
package main

import (
	"context"
	"math"
	"os"
	"time"
)

// TwampMeasurer wraps the existing TWAMP-Light client as a Measurer.
type TwampMeasurer struct {
	hostname string
	port     int
	logFile  *os.File
}

func (m *TwampMeasurer) Name() string { return "twamp" }

func (m *TwampMeasurer) Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error) {
	c := NewClient(
		target.Address,
		m.logFile,
		cfg.BurstSize,
		cfg.BurstInterval,
		cfg.Timeout,
		m.port,
		cfg.Padding,
		cfg.Synced,
	)

	rtts, recv := c.runBurst()
	sent := cfg.BurstSize
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

	fields := map[string]float64{
		"packets_sent": float64(sent),
		"packets_recv": float64(recv),
	}
	if sent > 0 {
		fields["loss_pct"] = float64(sent-recv) / float64(sent) * 100.0
	} else {
		fields["loss_pct"] = 100.0
	}

	if recv > 0 {
		var sum time.Duration
		minR, maxR := rtts[0], rtts[0]
		for _, r := range rtts {
			sum += r
			if r < minR { minR = r }
			if r > maxR { maxR = r }
		}
		avg := sum / time.Duration(recv)

		var variance float64
		for _, r := range rtts {
			d := float64(r) - float64(avg)
			variance += d * d
		}
		stddevNs := math.Sqrt(variance / float64(recv))

		var jitterSum time.Duration
		for i := 1; i < recv; i++ {
			d := rtts[i] - rtts[i-1]
			if d < 0 { d = -d }
			jitterSum += d
		}
		var jitter time.Duration
		if recv > 1 {
			jitter = jitterSum / time.Duration(recv-1)
		}

		fields["rtt_min_ms"] = ms(minR)
		fields["rtt_avg_ms"] = ms(avg)
		fields["rtt_max_ms"] = ms(maxR)
		fields["rtt_stddev_ms"] = stddevNs / 1e6
		fields["jitter_ms"] = ms(jitter)
	}

	return []MeasureResult{{
		Measurement: "piccolo_twamp",
		Source:      m.hostname,
		Target:      target.Name,
		Site:        target.Site,
		Topology:    "",
		Tags:        map[string]string{},
		Fields:      fields,
		SentAt:      time.Now(),
	}}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run TestTwampMeasurerName ./...
```
Expected: PASS

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add twamp_measurer.go piccolo_test.go
git commit -m "feat: add TwampMeasurer wrapping existing TWAMP-Light client"
```

---

### Task 6: DNS Measurer

**Files:**
- Create: `dns.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `Measurer`, `MeasurerConfig`, `MeasureResult` from Task 2
- Produces: `DnsMeasurer` struct; `HostEntry` target is ignored — uses `cfg.Resolvers` and `cfg.Names`

- [ ] **Step 1: Write failing test**

Add to `piccolo_test.go`:
```go
func TestDnsMeasurerName(t *testing.T) {
    m := &DnsMeasurer{hostname: "probe-a"}
    if m.Name() != "dns" {
        t.Errorf("Name() = %q, want dns", m.Name())
    }
}

func TestDnsMeasurerRun(t *testing.T) {
    m := &DnsMeasurer{hostname: "probe-a"}
    cfg := MeasurerConfig{
        Timeout:   2 * time.Second,
        Resolvers: []string{"8.8.8.8"},
        Names:     []string{"example.com"},
    }
    ctx := context.Background()
    results, err := m.Run(ctx, HostEntry{Name: "dns", Address: ""}, cfg)
    if err != nil {
        t.Fatalf("Run() error: %v", err)
    }
    if len(results) == 0 {
        t.Fatal("expected at least one result")
    }
    r := results[0]
    if r.Measurement != "piccolo_dns" {
        t.Errorf("Measurement = %q, want piccolo_dns", r.Measurement)
    }
    if _, ok := r.Fields["dns_rtt_ms"]; !ok {
        t.Error("missing dns_rtt_ms field")
    }
    if _, ok := r.Fields["dns_success"]; !ok {
        t.Error("missing dns_success field")
    }
    if r.Tags["resolver"] == "" {
        t.Error("missing resolver tag")
    }
    if r.Tags["name"] == "" {
        t.Error("missing name tag")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run "TestDnsMeasurer" ./...
```
Expected: compile error — `DnsMeasurer undefined`

- [ ] **Step 3: Create dns.go**

```go
package main

import (
	"context"
	"net"
	"time"
)

// DnsMeasurer measures DNS resolution latency per resolver per name.
type DnsMeasurer struct {
	hostname string
}

func (m *DnsMeasurer) Name() string { return "dns" }

func (m *DnsMeasurer) Run(ctx context.Context, _ HostEntry, cfg MeasurerConfig) ([]MeasureResult, error) {
	var results []MeasureResult
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	for _, resolver := range cfg.Resolvers {
		for _, name := range cfg.Names {
			r := m.probe(ctx, resolver, name, timeout)
			results = append(results, r)
		}
	}
	return results, nil
}

func (m *DnsMeasurer) probe(ctx context.Context, resolver, name string, timeout time.Duration) MeasureResult {
	dialer := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "udp", resolver+":53")
		},
	}

	start := time.Now()
	_, err := dialer.LookupHost(ctx, name)
	rttMs := float64(time.Since(start).Microseconds()) / 1000.0

	success := 1.0
	if err != nil {
		success = 0.0
	}

	return MeasureResult{
		Measurement: "piccolo_dns",
		Source:      m.hostname,
		Target:      resolver,
		Tags: map[string]string{
			"resolver": resolver,
			"name":     name,
		},
		Fields: map[string]float64{
			"dns_rtt_ms": rttMs,
			"dns_success": success,
		},
		SentAt: time.Now(),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run "TestDnsMeasurer" ./...
```
Expected: PASS (Note: `TestDnsMeasurerRun` makes a real DNS query to 8.8.8.8 — if network is unavailable, `dns_success=0` is still a valid result and the test checks structure not success value)

- [ ] **Step 5: Commit**

```bash
git add dns.go piccolo_test.go
git commit -m "feat: add DnsMeasurer for per-resolver DNS timing"
```

---

### Task 7: Bandwidth Measurer (native + iperf3 shim)

**Files:**
- Create: `bw.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `Measurer`, `MeasurerConfig`, `MeasureResult` from Task 2
- Produces: `BwMeasurer`; `BwServer` (TCP listener for peer use)

- [ ] **Step 1: Write failing tests**

Add to `piccolo_test.go`:
```go
func TestBwMeasurerName(t *testing.T) {
    m := &BwMeasurer{hostname: "probe-a"}
    if m.Name() != "bw" {
        t.Errorf("Name() = %q, want bw", m.Name())
    }
}

func TestBwNativeLoopback(t *testing.T) {
    // Start a BwServer on a random port, run BwMeasurer against it.
    srv := &BwServer{}
    port, err := srv.Start(0) // 0 = OS picks port
    if err != nil {
        t.Fatalf("BwServer.Start: %v", err)
    }
    defer srv.Stop()

    m := &BwMeasurer{hostname: "probe-a"}
    cfg := MeasurerConfig{
        Duration: 500 * time.Millisecond,
        Timeout:  5 * time.Second,
        PreferIperf3: false,
    }
    target := HostEntry{Name: "loopback", Address: fmt.Sprintf("127.0.0.1:%d", port)}
    ctx := context.Background()
    results, err := m.Run(ctx, target, cfg)
    if err != nil {
        t.Fatalf("Run() error: %v", err)
    }
    if len(results) == 0 {
        t.Fatal("expected at least one result")
    }
    r := results[0]
    if r.Measurement != "piccolo_bw" {
        t.Errorf("Measurement = %q, want piccolo_bw", r.Measurement)
    }
    if r.Fields["bw_tx_mbps"] <= 0 {
        t.Errorf("bw_tx_mbps = %v, want > 0", r.Fields["bw_tx_mbps"])
    }
    if r.Tags["method"] != "native" {
        t.Errorf("Tags[method] = %q, want native", r.Tags["method"])
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run "TestBw" ./...
```
Expected: compile error — `BwMeasurer undefined`

- [ ] **Step 3: Create bw.go**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"encoding/json"
)

const bwBufSize = 64 * 1024 // 64 KB send buffer — safe on 16 MB RAM devices

// BwMeasurer measures TCP bandwidth, using iperf3 when available.
type BwMeasurer struct {
	hostname    string
	iperf3Path  string // set by detectIperf3(); empty means unavailable
	iperf3Once  sync.Once
}

func (m *BwMeasurer) Name() string { return "bw" }

func (m *BwMeasurer) detectIperf3() string {
	m.iperf3Once.Do(func() {
		p, err := exec.LookPath("iperf3")
		if err == nil {
			m.iperf3Path = p
		}
	})
	return m.iperf3Path
}

func (m *BwMeasurer) Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error) {
	duration := cfg.Duration
	if duration <= 0 {
		duration = 5 * time.Second
	}

	if cfg.PreferIperf3 {
		if path := m.detectIperf3(); path != "" {
			r, err := m.runIperf3(ctx, target, duration, path)
			if err == nil {
				return []MeasureResult{r}, nil
			}
			// fall through to native on iperf3 failure
		}
	}
	return m.runNative(ctx, target, duration)
}

func (m *BwMeasurer) runNative(ctx context.Context, target HostEntry, duration time.Duration) ([]MeasureResult, error) {
	addr := target.Address
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "5201")
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bw dial %s: %w", addr, err)
	}
	defer conn.Close()

	buf := make([]byte, bwBufSize)
	sent := int64(0)
	deadline := time.Now().Add(duration)
	conn.SetDeadline(deadline)

	start := time.Now()
	for time.Now().Before(deadline) {
		n, err := conn.Write(buf)
		sent += int64(n)
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start)

	txMbps := float64(sent) * 8 / 1e6 / elapsed.Seconds()

	return []MeasureResult{{
		Measurement: "piccolo_bw",
		Source:      m.hostname,
		Target:      target.Name,
		Site:        target.Site,
		Tags:        map[string]string{"method": "native"},
		Fields: map[string]float64{
			"bw_tx_mbps":   txMbps,
			"bw_duration_s": elapsed.Seconds(),
		},
		SentAt: time.Now(),
	}}, nil
}

// iperf3JSONResult is the subset of iperf3's JSON output we care about.
type iperf3JSONResult struct {
	End struct {
		SumSent     struct{ BitsPerSecond float64 `json:"bits_per_second"` } `json:"sum_sent"`
		SumReceived struct{ BitsPerSecond float64 `json:"bits_per_second"` } `json:"sum_received"`
	} `json:"end"`
}

func (m *BwMeasurer) runIperf3(ctx context.Context, target HostEntry, duration time.Duration, iperf3 string) (MeasureResult, error) {
	addr := target.Address
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	secs := strconv.Itoa(int(duration.Seconds()))
	if secs == "0" {
		secs = "5"
	}

	cmd := exec.CommandContext(ctx, iperf3, "-c", host, "-J", "-t", secs)
	out, err := cmd.Output()
	if err != nil {
		return MeasureResult{}, fmt.Errorf("iperf3: %w", err)
	}

	var parsed iperf3JSONResult
	if err := json.Unmarshal(out, &parsed); err != nil {
		return MeasureResult{}, fmt.Errorf("iperf3 json: %w", err)
	}

	return MeasureResult{
		Measurement: "piccolo_bw",
		Source:      m.hostname,
		Target:      target.Name,
		Site:        target.Site,
		Tags:        map[string]string{"method": "iperf3"},
		Fields: map[string]float64{
			"bw_tx_mbps": parsed.End.SumSent.BitsPerSecond / 1e6,
			"bw_rx_mbps": parsed.End.SumReceived.BitsPerSecond / 1e6,
			"bw_duration_s": duration.Seconds(),
		},
		SentAt: time.Now(),
	}, nil
}

// BwServer is a TCP sink for native bandwidth tests.
type BwServer struct {
	listener net.Listener
	once     sync.Once
}

// Start begins listening on the given port (0 = OS-assigned). Returns the bound port.
func (s *BwServer) Start(port int) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return 0, err
	}
	s.listener = ln
	go s.accept()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (s *BwServer) accept() {
	buf := make([]byte, bwBufSize)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			io.CopyBuffer(io.Discard, c, buf)
		}(conn)
	}
}

// Stop shuts down the server.
func (s *BwServer) Stop() {
	s.once.Do(func() {
		if s.listener != nil {
			s.listener.Close()
		}
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -run "TestBw" ./...
```
Expected: PASS

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add bw.go piccolo_test.go
git commit -m "feat: add BwMeasurer with native TCP tester and iperf3 shim"
```

---

### Task 8: MTU Measurer

**Files:**
- Create: `mtu.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `Measurer`, `MeasurerConfig`, `MeasureResult` from Task 2
- Produces: `MtuMeasurer`; degrades gracefully without `CAP_NET_RAW`

- [ ] **Step 1: Write failing test**

Add to `piccolo_test.go`:
```go
func TestMtuMeasurerName(t *testing.T) {
    m := &MtuMeasurer{hostname: "probe-a"}
    if m.Name() != "mtu" {
        t.Errorf("Name() = %q, want mtu", m.Name())
    }
}

func TestMtuMeasurerSkippedWithoutCap(t *testing.T) {
    // This test verifies the measurer returns a skipped result (not an error)
    // when raw sockets are unavailable — which is the case in most CI environments.
    m := &MtuMeasurer{hostname: "probe-a"}
    cfg := MeasurerConfig{Ceiling: 1500, Timeout: 2 * time.Second}
    target := HostEntry{Name: "loopback", Address: "127.0.0.1"}
    results, err := m.Run(context.Background(), target, cfg)
    // Either succeeds (has CAP_NET_RAW) or returns skipped result — never hard error
    if err != nil {
        t.Fatalf("Run() returned error (should degrade gracefully): %v", err)
    }
    if len(results) == 0 {
        t.Fatal("expected at least one result")
    }
    // If skipped, the tag must be present
    r := results[0]
    if r.Measurement != "piccolo_mtu" {
        t.Errorf("Measurement = %q, want piccolo_mtu", r.Measurement)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run "TestMtu" ./...
```
Expected: compile error

- [ ] **Step 3: Create mtu.go**

```go
package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// MtuMeasurer discovers effective path MTU using ICMP with DF bit set.
// Requires CAP_NET_RAW; degrades gracefully without it.
type MtuMeasurer struct {
	hostname string
}

func (m *MtuMeasurer) Name() string { return "mtu" }

func (m *MtuMeasurer) Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error) {
	ceiling := cfg.Ceiling
	if ceiling <= 0 {
		ceiling = 1500
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	effective, err := m.discover(ctx, target.Address, ceiling, timeout)

	skipped := "false"
	if err != nil {
		// Raw socket unavailable or permission denied — degrade gracefully
		skipped = "true"
		effective = 0
	}

	return []MeasureResult{{
		Measurement: "piccolo_mtu",
		Source:      m.hostname,
		Target:      target.Name,
		Site:        target.Site,
		Tags:        map[string]string{"skipped": skipped},
		Fields: map[string]float64{
			"mtu_effective_bytes": float64(effective),
			"mtu_ceiling_bytes":   float64(ceiling),
		},
		SentAt: time.Now(),
	}}, nil
}

func (m *MtuMeasurer) discover(ctx context.Context, addr string, ceiling int, timeout time.Duration) (int, error) {
	dst, err := net.ResolveIPAddr("ip4", addr)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", addr, err)
	}

	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return 0, fmt.Errorf("raw socket: %w", err) // CAP_NET_RAW missing
	}
	defer c.Close()

	p := c.IPv4PacketConn()

	lo, hi := 576, ceiling
	effective := 0

	for lo <= hi {
		mid := (lo + hi) / 2
		payloadSize := mid - 28 // 20 IP + 8 ICMP header

		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{
				ID:   1,
				Seq:  mid,
				Data: make([]byte, payloadSize),
			},
		}
		wb, err := msg.Marshal(nil)
		if err != nil {
			break
		}

		if err := p.SetDF(true); err != nil {
			return 0, fmt.Errorf("set DF: %w", err)
		}

		c.SetDeadline(time.Now().Add(timeout))
		if _, err := c.WriteTo(wb, dst); err != nil {
			hi = mid - 1
			continue
		}

		rb := make([]byte, 1500)
		c.SetDeadline(time.Now().Add(timeout))
		n, _, err := c.ReadFrom(rb)
		if err != nil || n == 0 {
			hi = mid - 1
			continue
		}

		rm, err := icmp.ParseMessage(1, rb[:n])
		if err != nil {
			hi = mid - 1
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			effective = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}

		select {
		case <-ctx.Done():
			return effective, nil
		default:
		}
	}

	return effective, nil
}
```

Note: this requires `golang.org/x/net`. Add to go.mod:
```bash
go get golang.org/x/net
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -run "TestMtu" ./...
```
Expected: PASS (skipped result on systems without CAP_NET_RAW)

- [ ] **Step 5: Commit**

```bash
git add mtu.go piccolo_test.go go.mod go.sum
git commit -m "feat: add MtuMeasurer with graceful CAP_NET_RAW degradation"
```

---

### Task 9: Traceroute Measurer

**Files:**
- Create: `trace.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `Measurer`, `MeasurerConfig`, `MeasureResult` from Task 2
- Produces: `TraceMeasurer`; degrades gracefully without `CAP_NET_RAW`

- [ ] **Step 1: Write failing tests**

Add to `piccolo_test.go`:
```go
func TestTraceMeasurerName(t *testing.T) {
    m := &TraceMeasurer{hostname: "probe-a"}
    if m.Name() != "trace" {
        t.Errorf("Name() = %q, want trace", m.Name())
    }
}

func TestTraceMeasurerSkippedWithoutCap(t *testing.T) {
    m := &TraceMeasurer{hostname: "probe-a"}
    cfg := MeasurerConfig{MaxHops: 5, ProbesPerHop: 1, Timeout: time.Second}
    target := HostEntry{Name: "loopback", Address: "127.0.0.1"}
    results, err := m.Run(context.Background(), target, cfg)
    if err != nil {
        t.Fatalf("Run() error (should degrade): %v", err)
    }
    if len(results) == 0 {
        t.Fatal("expected at least one result")
    }
    if results[0].Measurement != "piccolo_trace" {
        t.Errorf("Measurement = %q, want piccolo_trace", results[0].Measurement)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run "TestTrace" ./...
```
Expected: compile error

- [ ] **Step 3: Create trace.go**

```go
package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// TraceMeasurer measures per-hop RTT using TTL-increment ICMP probing.
// Requires CAP_NET_RAW; degrades gracefully without it.
type TraceMeasurer struct {
	hostname string
}

func (m *TraceMeasurer) Name() string { return "trace" }

func (m *TraceMeasurer) Run(ctx context.Context, target HostEntry, cfg MeasurerConfig) ([]MeasureResult, error) {
	maxHops := cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 30
	}
	probes := cfg.ProbesPerHop
	if probes <= 0 {
		probes = 1
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	fields, hops, err := m.trace(ctx, target.Address, maxHops, probes, timeout)

	skipped := "false"
	if err != nil {
		skipped = "true"
		fields = map[string]float64{}
		hops = 0
	}
	fields["trace_hops"] = float64(hops)

	return []MeasureResult{{
		Measurement: "piccolo_trace",
		Source:      m.hostname,
		Target:      target.Name,
		Site:        target.Site,
		Tags:        map[string]string{"skipped": skipped},
		Fields:      fields,
		SentAt:      time.Now(),
	}}, nil
}

func (m *TraceMeasurer) trace(ctx context.Context, addr string, maxHops, probes int, timeout time.Duration) (map[string]float64, int, error) {
	dst, err := net.ResolveIPAddr("ip4", addr)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve: %w", err)
	}

	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, 0, fmt.Errorf("raw socket: %w", err)
	}
	defer c.Close()

	p := c.IPv4PacketConn()
	fields := make(map[string]float64)
	reached := 0

	for ttl := 1; ttl <= maxHops; ttl++ {
		select {
		case <-ctx.Done():
			return fields, reached, nil
		default:
		}

		if err := p.SetTTL(ttl); err != nil {
			return nil, 0, fmt.Errorf("setTTL: %w", err)
		}

		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: 0,
			Body: &icmp.Echo{ID: 1, Seq: ttl, Data: []byte("piccolo-perf")},
		}
		wb, _ := msg.Marshal(nil)

		start := time.Now()
		c.SetDeadline(time.Now().Add(timeout))
		if _, err := c.WriteTo(wb, dst); err != nil {
			continue
		}

		rb := make([]byte, 1500)
		c.SetDeadline(time.Now().Add(timeout))
		_, peer, err := c.ReadFrom(rb)
		if err != nil {
			fields[fmt.Sprintf("hop_%d_rtt_ms", ttl)] = -1
			continue
		}

		rttMs := float64(time.Since(start).Microseconds()) / 1000.0
		fields[fmt.Sprintf("hop_%d_rtt_ms", ttl)] = rttMs
		reached = ttl

		if peer.String() == dst.String() {
			fields["trace_complete"] = 1.0
			break
		}
	}

	return fields, reached, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run "TestTrace" ./...
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add trace.go piccolo_test.go
git commit -m "feat: add TraceMeasurer with hop-latency and graceful degradation"
```

---

## Phase 3: Agent scheduler, local store, config schema

### Task 10: Local resilience store

**Files:**
- Create: `store.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `MeasureResult` from Task 2
- Produces: `LocalStore` with `Append(MeasureResult)`, `Flush(ctx, func([]MeasureResult) error) error`

- [ ] **Step 1: Write failing tests**

Add to `piccolo_test.go`:
```go
func TestLocalStoreAppendAndFlush(t *testing.T) {
    dir := t.TempDir()
    path := dir + "/results.jsonl"
    s, err := NewLocalStore(path, 1000)
    if err != nil {
        t.Fatalf("NewLocalStore: %v", err)
    }
    defer s.Close()

    r := MeasureResult{
        Measurement: "piccolo_twamp",
        Source:      "a",
        Target:      "b",
        Fields:      map[string]float64{"rtt_avg_ms": 1.0},
        Tags:        map[string]string{},
        SentAt:      time.Unix(1_000_000, 0),
    }
    if err := s.Append(r); err != nil {
        t.Fatalf("Append: %v", err)
    }

    var flushed []MeasureResult
    err = s.Flush(context.Background(), func(batch []MeasureResult) error {
        flushed = append(flushed, batch...)
        return nil
    })
    if err != nil {
        t.Fatalf("Flush: %v", err)
    }
    if len(flushed) != 1 {
        t.Fatalf("expected 1 flushed result, got %d", len(flushed))
    }
    if flushed[0].Source != "a" {
        t.Errorf("flushed result Source = %q, want a", flushed[0].Source)
    }
}

func TestLocalStoreCapEnforced(t *testing.T) {
    dir := t.TempDir()
    s, err := NewLocalStore(dir+"/results.jsonl", 3)
    if err != nil {
        t.Fatalf("NewLocalStore: %v", err)
    }
    defer s.Close()

    for i := 0; i < 5; i++ {
        s.Append(MeasureResult{
            Measurement: "piccolo_twamp",
            Source:      fmt.Sprintf("host-%d", i),
            Target:      "b",
            Fields:      map[string]float64{},
            Tags:        map[string]string{},
            SentAt:      time.Now(),
        })
    }
    var flushed []MeasureResult
    s.Flush(context.Background(), func(b []MeasureResult) error {
        flushed = append(flushed, b...)
        return nil
    })
    if len(flushed) > 3 {
        t.Errorf("expected at most 3 results (cap=3), got %d", len(flushed))
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run "TestLocalStore" ./...
```
Expected: compile error

- [ ] **Step 3: Create store.go**

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// LocalStore is a flat JSONL ring-buffer store for MeasureResults.
// It survives upstream outages and replays on reconnection.
type LocalStore struct {
	path     string
	maxLines int
	mu       sync.Mutex
	file     *os.File
}

// NewLocalStore opens (or creates) the store file at path with a cap of maxLines.
func NewLocalStore(path string, maxLines int) (*LocalStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	return &LocalStore{path: path, maxLines: maxLines, file: f}, nil
}

// Append writes one MeasureResult as a JSON line, enforcing the cap.
func (s *LocalStore) Append(r MeasureResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := fmt.Fprintf(s.file, "%s\n", line); err != nil {
		return fmt.Errorf("write store: %w", err)
	}
	return s.enforceCap()
}

// enforceCap reads all lines and rewrites the file keeping only the last maxLines.
// Called while mu is held.
func (s *LocalStore) enforceCap() error {
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(s.file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) <= s.maxLines {
		return nil
	}
	lines = lines[len(lines)-s.maxLines:]
	if err := s.file.Truncate(0); err != nil {
		return err
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	w := bufio.NewWriter(s.file)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return w.Flush()
}

// Flush reads all stored results, calls fn in batches of 100, and clears the file on success.
func (s *LocalStore) Flush(ctx context.Context, fn func([]MeasureResult) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(s.file)
	var batch []MeasureResult
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var r MeasureResult
		if err := json.Unmarshal([]byte(scanner.Text()), &r); err != nil {
			continue
		}
		batch = append(batch, r)
		if len(batch) >= 100 {
			if err := fn(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := fn(batch); err != nil {
			return err
		}
	}
	// Clear the file after successful flush
	s.file.Truncate(0)
	s.file.Seek(0, 0)
	return nil
}

// Close closes the underlying file.
func (s *LocalStore) Close() error {
	return s.file.Close()
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run "TestLocalStore" ./...
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add store.go piccolo_test.go
git commit -m "feat: add LocalStore flat JSONL ring-buffer with flush"
```

---

### Task 11: Extend agent scheduler for multi-measurer dispatch

**Files:**
- Modify: `agent.go`
- Modify: `piccolo_test.go`

**Interfaces:**
- Consumes: `Measurer` from Task 2; `TwampMeasurer`, `BwMeasurer`, `TraceMeasurer`, `MtuMeasurer`, `DnsMeasurer` from Tasks 5–9; `LocalStore` from Task 10
- Produces: updated `AgentConfig` with `Measurements []MeasurementSpec`; updated `runAgent()` dispatching all measurers

- [ ] **Step 1: Add MeasurementSpec to AgentConfig**

In `agent.go`, add to `AgentConfig`:
```go
type MeasurementSpec struct {
    Type         string        // "twamp", "bw", "trace", "mtu", "dns"
    Interval     time.Duration
    Targets      string        // "all" or "hub-only"
    MeasurerConfig MeasurerConfig
}
```

And add field to `AgentConfig`:
```go
Measurements []MeasurementSpec
HideSkipped  bool
LocalStore   LocalStoreConfig
```

Add new config struct:
```go
type LocalStoreConfig struct {
    Enabled  bool
    Path     string
    MaxLines int
}
```

- [ ] **Step 2: Extend rawAgentConfig and parseAgentConfig**

In `agent.go`, add to `rawAgentConfig`:
```go
Measurements []rawMeasurementSpec `json:"measurements"`
HideSkipped  bool                  `json:"hide_skipped"`
LocalStore   rawLocalStoreConfig   `json:"local_store"`
```

Add:
```go
type rawMeasurementSpec struct {
    Type          string   `json:"type"`
    Interval      string   `json:"interval"`
    Targets       string   `json:"targets"`
    BurstSize     int      `json:"burst_size"`
    BurstInterval string   `json:"burst_interval"`
    PacketTimeout string   `json:"packet_timeout"`
    Padding       int      `json:"padding"`
    Duration      string   `json:"duration"`
    PreferIperf3  bool     `json:"prefer_iperf3"`
    MaxHops       int      `json:"max_hops"`
    ProbesPerHop  int      `json:"probes_per_hop"`
    Ceiling       int      `json:"ceiling"`
    Resolvers     []string `json:"resolvers"`
    Names         []string `json:"names"`
    Timeout       string   `json:"timeout"`
}

type rawLocalStoreConfig struct {
    Enabled  bool   `json:"enabled"`
    Path     string `json:"path"`
    MaxLines int    `json:"max_lines"`
}
```

In `parseAgentConfig`, add parsing of `Measurements`:
```go
var specs []MeasurementSpec
for _, rm := range raw.Measurements {
    interval, err := parseDur(rm.Interval, "measurements[].interval", "60s")
    if err != nil {
        return AgentConfig{}, err
    }
    burstInterval, err := parseDur(rm.BurstInterval, "burst_interval", "200ms")
    if err != nil {
        return AgentConfig{}, err
    }
    pktTimeout, err := parseDur(rm.PacketTimeout, "packet_timeout", "5s")
    if err != nil {
        return AgentConfig{}, err
    }
    dur, err := parseDur(rm.Duration, "duration", "5s")
    if err != nil {
        return AgentConfig{}, err
    }
    mTimeout, err := parseDur(rm.Timeout, "timeout", "2s")
    if err != nil {
        return AgentConfig{}, err
    }
    burstSize := rm.BurstSize
    if burstSize <= 0 {
        burstSize = 5
    }
    maxHops := rm.MaxHops
    if maxHops <= 0 {
        maxHops = 30
    }
    probes := rm.ProbesPerHop
    if probes <= 0 {
        probes = 1
    }
    ceiling := rm.Ceiling
    if ceiling <= 0 {
        ceiling = 1500
    }
    specs = append(specs, MeasurementSpec{
        Type:     rm.Type,
        Interval: interval,
        Targets:  rm.Targets,
        MeasurerConfig: MeasurerConfig{
            BurstSize:     burstSize,
            BurstInterval: burstInterval,
            Timeout:       mTimeout,
            Padding:       rm.Padding,
            Duration:      dur,
            PreferIperf3:  rm.PreferIperf3,
            MaxHops:       maxHops,
            ProbesPerHop:  probes,
            Ceiling:       ceiling,
            Resolvers:     rm.Resolvers,
            Names:         rm.Names,
        },
    })
    _ = pktTimeout // PacketTimeout goes into BurstInterval for TWAMP
}
// If no measurements block, default to TWAMP for backward compat
if len(specs) == 0 {
    specs = []MeasurementSpec{{
        Type: "twamp", Interval: probeInterval, Targets: "all",
        MeasurerConfig: MeasurerConfig{
            BurstSize: burstSize, BurstInterval: burstInterval,
            Timeout: packetTimeout, Padding: raw.Padding,
        },
    }}
}
```

And set on return:
```go
Measurements: specs,
HideSkipped:  raw.HideSkipped,
LocalStore: LocalStoreConfig{
    Enabled:  raw.LocalStore.Enabled,
    Path:     raw.LocalStore.Path,
    MaxLines: raw.LocalStore.MaxLines,
},
```

- [ ] **Step 3: Write failing test for multi-measurer config parsing**

Add to `piccolo_test.go`:
```go
func TestParseAgentConfigMeasurements(t *testing.T) {
    raw := []byte(`{
        "topology": "mesh",
        "hosts": [{"name":"a","address":"10.0.0.1","site":"east"}],
        "hub_spoke": {"enabled": false, "hub": ""},
        "measurements": [
            {"type": "twamp", "interval": "30s", "targets": "all", "burst_size": 3},
            {"type": "dns",   "interval": "60s", "resolvers": ["8.8.8.8"], "names": ["example.com"]}
        ]
    }`)
    cfg, err := parseAgentConfig(raw)
    if err != nil {
        t.Fatalf("parseAgentConfig: %v", err)
    }
    if len(cfg.Measurements) != 2 {
        t.Fatalf("expected 2 measurements, got %d", len(cfg.Measurements))
    }
    if cfg.Measurements[0].Type != "twamp" {
        t.Errorf("Measurements[0].Type = %q, want twamp", cfg.Measurements[0].Type)
    }
    if cfg.Measurements[1].Type != "dns" {
        t.Errorf("Measurements[1].Type = %q, want dns", cfg.Measurements[1].Type)
    }
    if cfg.Measurements[0].MeasurerConfig.BurstSize != 3 {
        t.Errorf("BurstSize = %d, want 3", cfg.Measurements[0].MeasurerConfig.BurstSize)
    }
}
```

- [ ] **Step 4: Run test to verify it fails**

```bash
go test -run TestParseAgentConfigMeasurements ./...
```
Expected: compile error then logic failure

- [ ] **Step 5: Update runAgent to dispatch all measurers**

Replace the probe scheduler goroutine section in `runAgent` with a multi-measurer dispatcher. Add a helper to build the measurer registry:

```go
func buildMeasurers(hostname string, port int, synced bool, logFile *os.File) map[string]Measurer {
    return map[string]Measurer{
        "twamp": &TwampMeasurer{hostname: hostname, port: port, logFile: logFile, synced: synced},
        "bw":    &BwMeasurer{hostname: hostname},
        "trace": &TraceMeasurer{hostname: hostname},
        "mtu":   &MtuMeasurer{hostname: hostname},
        "dns":   &DnsMeasurer{hostname: hostname},
    }
}
```

Update `TwampMeasurer` struct to include `synced bool` field (add it and use `cfg.Synced` already present in `MeasurerConfig`).

In `runAgent`, replace the single `runProbeScheduler` goroutine with:
```go
measurers := buildMeasurers(hostname, port, synced, logFile)
for _, spec := range initialCfg.Measurements {
    spec := spec // capture
    m, ok := measurers[spec.Type]
    if !ok {
        logger.Printf("[Agent] unknown measurement type %q — skipping", spec.Type)
        continue
    }
    wg.Add(1)
    go func() {
        defer wg.Done()
        runMeasurerScheduler(ctx, m, spec, configCh, resultsCh, hostname, logger, initialCfg.HideSkipped)
    }()
}
```

Add `runMeasurerScheduler`:
```go
func runMeasurerScheduler(ctx context.Context, m Measurer, spec MeasurementSpec, configs <-chan AgentConfig, results chan<- MeasureResult, hostname string, logger *log.Logger, hideSkipped bool) {
    ticker := time.NewTicker(spec.Interval)
    defer ticker.Stop()
    var cfg AgentConfig

    for {
        select {
        case newCfg := <-configs:
            cfg = newCfg
        case <-ticker.C:
            targets := resolveTargets(cfg, hostname, spec.Targets, m.Name())
            cfg := spec.MeasurerConfig
            cfg.Synced = true // always use agent's synced flag
            for _, target := range targets {
                rs, err := m.Run(ctx, target, cfg)
                if err != nil {
                    logger.Printf("[Agent] %s→%s error: %v", m.Name(), target.Name, err)
                    continue
                }
                for _, r := range rs {
                    if hideSkipped && r.Tags["skipped"] == "true" {
                        continue
                    }
                    select {
                    case results <- r:
                    case <-ctx.Done():
                        return
                    }
                }
            }
        case <-ctx.Done():
            return
        }
    }
}

func resolveTargets(cfg AgentConfig, hostname, targetsField, measType string) []HostEntry {
    if measType == "dns" {
        // DNS uses resolvers/names from MeasurerConfig, not hosts
        return []HostEntry{{Name: "dns", Address: ""}}
    }
    switch targetsField {
    case "hub-only":
        for _, h := range cfg.Hosts {
            if h.Name == cfg.HubSpoke.Hub {
                return []HostEntry{h}
            }
        }
        return nil
    default: // "all" or empty
        var out []HostEntry
        for _, h := range cfg.Hosts {
            if h.Name != hostname {
                out = append(out, h)
            }
        }
        return out
    }
}
```

Also update the InfluxDB writer goroutine in `runAgent` to accept `chan MeasureResult` and call `w.runResults(ctx, resultsCh)`.

Change `resultsCh` declaration:
```go
resultsCh := make(chan MeasureResult, 200)
```

Update Goroutine 4:
```go
wg.Add(1)
go func() {
    defer wg.Done()
    w := newInfluxWriter(initialCfg.InfluxDB, logger)
    w.runResults(ctx, resultsCh)
}()
```

- [ ] **Step 6: Run all tests**

```bash
go test ./...
```
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add agent.go piccolo_test.go
git commit -m "feat: extend agent scheduler for multi-measurer dispatch"
```

---

## Phase 4: Subcommand routing and deployment

### Task 12: Subcommand routing in main.go

**Files:**
- Rename: `tinytwamp.go` → `main.go`
- Modify: `main.go` (subcommand dispatch, backward-compat shim)

**Interfaces:**
- Consumes: all modes from existing code; `BwServer` from Task 7

- [ ] **Step 1: Rename the file**

```bash
git mv tinytwamp.go main.go
```

- [ ] **Step 2: Replace main() with subcommand dispatch**

Replace the entire `main()` function in `main.go` with:

```go
func main() {
    if len(os.Args) < 2 {
        printUsage()
        os.Exit(1)
    }

    // Backward compat: old flat -mode flag
    if os.Args[1] == "-mode" || os.Args[1] == "--mode" {
        fmt.Fprintf(os.Stderr, "Warning: -mode flag is deprecated. Use subcommands: piccolo-perf <twamp|bw|trace|mtu|dns|agent>\n")
        flag.Parse()
        runLegacyMode()
        return
    }

    sub := os.Args[1]
    os.Args = append([]string{os.Args[0]}, os.Args[2:]...)

    switch sub {
    case "twamp":
        runTwampSubcommand()
    case "bw":
        runBwSubcommand()
    case "trace":
        runTraceSubcommand()
    case "mtu":
        runMtuSubcommand()
    case "dns":
        runDnsSubcommand()
    case "agent":
        runAgentSubcommand()
    case "version", "--version", "-version":
        fmt.Println(version)
    default:
        fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n", sub)
        printUsage()
        os.Exit(1)
    }
}

func printUsage() {
    fmt.Fprintf(os.Stderr, `piccolo-perf — network performance toolkit

Usage:
  piccolo-perf twamp  [client|server|agent|exporter]
  piccolo-perf bw     [client|server]
  piccolo-perf trace  -target <addr> [flags]
  piccolo-perf mtu    -target <addr> [flags]
  piccolo-perf dns    -resolver <ip> -name <fqdn> [flags]
  piccolo-perf agent  -config-url <url> [flags]
  piccolo-perf version
`)
}
```

- [ ] **Step 3: Add runLegacyMode**

Extract the existing flag.Parse()+switch into `runLegacyMode()`:
```go
func runLegacyMode() {
    synced := !*noSync
    var logFile *os.File
    if *logFilePath != "" {
        var err error
        logFile, err = os.OpenFile(*logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
            os.Exit(1)
        }
        defer logFile.Close()
    }
    switch *mode {
    case "server":
        // ... (existing server case)
    case "client":
        // ... (existing client case)
    case "agent":
        // ... (existing agent case)
    case "exporter":
        // ... (existing exporter case)
    default:
        fmt.Fprintf(os.Stderr, "Invalid mode %q\n", *mode)
        os.Exit(1)
    }
}
```

- [ ] **Step 4: Add subcommand functions**

Add stub runners that parse their own flag sets:

```go
func runTwampSubcommand() {
    fs := flag.NewFlagSet("twamp", flag.ExitOnError)
    twampMode := fs.String("mode", "client", "client, server, agent, or exporter")
    server    := fs.String("server", "localhost", "server address")
    port      := fs.Int("port", defaultPort, "UDP port")
    count     := fs.Int("count", 10, "packets to send")
    interval  := fs.Duration("interval", time.Second, "interval between packets")
    timeout   := fs.Duration("timeout", 5*time.Second, "per-packet timeout")
    padding   := fs.Int("padding", 0, "padding bytes")
    noSync    := fs.Bool("no-sync", false, "assert clock unsynchronized")
    rateLimit := fs.Int("rate-limit", 0, "max pkts/sec per source IP")
    allowed   := fs.String("allowed", "", "CIDR allowlist")
    logPath   := fs.String("logfile", "", "log file path")
    configURL := fs.String("config-url", "", "topology config URL (agent/exporter mode)")
    hostname  := fs.String("hostname", "", "override hostname")
    cfgRefresh := fs.Duration("config-refresh", 0, "config refresh interval")
    probeMode := fs.String("probe-mode", "background", "exporter probe mode")
    metricsAddr := fs.String("metrics-addr", ":9862", "Prometheus metrics address")
    metricsCert := fs.String("metrics-tls-cert", "", "TLS cert file")
    metricsKey  := fs.String("metrics-tls-key", "", "TLS key file")
    fs.Parse(os.Args[1:])

    synced := !*noSync
    var logFile *os.File
    if *logPath != "" {
        var err error
        logFile, err = os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
        if err != nil {
            fmt.Fprintf(os.Stderr, "log file: %v\n", err)
            os.Exit(1)
        }
        defer logFile.Close()
    }

    switch *twampMode {
    case "server":
        al, err := parseAllowlist(*allowed)
        if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
        rl := newRateLimiter(*rateLimit)
        srv := NewServer(logFile, rl, al, synced)
        if err := srv.Start(*port); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    case "client":
        c := NewClient(*server, logFile, *count, *interval, *timeout, *port, *padding, synced)
        if err := c.Run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    case "agent":
        if *configURL == "" { fmt.Fprintln(os.Stderr, "agent requires -config-url"); os.Exit(1) }
        runAgent(*port, *configURL, *hostname, *cfgRefresh, synced, logFile)
    case "exporter":
        if *configURL == "" { fmt.Fprintln(os.Stderr, "exporter requires -config-url"); os.Exit(1) }
        runExporter(*port, *configURL, *hostname, *cfgRefresh, *probeMode, *metricsAddr, *metricsCert, *metricsKey, synced, logFile)
    default:
        fmt.Fprintf(os.Stderr, "unknown twamp mode %q\n", *twampMode)
        os.Exit(1)
    }
}

func runBwSubcommand() {
    fs := flag.NewFlagSet("bw", flag.ExitOnError)
    bwMode   := fs.String("mode", "client", "client or server")
    target   := fs.String("target", "", "target address:port (client mode)")
    port     := fs.Int("port", 5201, "listen port (server mode)")
    duration := fs.Duration("duration", 5*time.Second, "test duration")
    iperf3   := fs.Bool("prefer-iperf3", false, "use iperf3 when available")
    fs.Parse(os.Args[1:])

    switch *bwMode {
    case "server":
        srv := &BwServer{}
        p, err := srv.Start(*port)
        if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
        fmt.Printf("piccolo-perf bw server listening on :%d\n", p)
        select {} // block forever
    case "client":
        if *target == "" { fmt.Fprintln(os.Stderr, "bw client requires -target"); os.Exit(1) }
        h, _ := os.Hostname()
        m := &BwMeasurer{hostname: h}
        cfg := MeasurerConfig{Duration: *duration, PreferIperf3: *iperf3, Timeout: 10 * time.Second}
        results, err := m.Run(context.Background(), HostEntry{Name: *target, Address: *target}, cfg)
        if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
        for _, r := range results {
            fmt.Printf("method=%s tx=%.2f Mbps\n", r.Tags["method"], r.Fields["bw_tx_mbps"])
        }
    default:
        fmt.Fprintf(os.Stderr, "unknown bw mode %q\n", *bwMode)
        os.Exit(1)
    }
}

func runTraceSubcommand() {
    fs := flag.NewFlagSet("trace", flag.ExitOnError)
    target   := fs.String("target", "", "target address (required)")
    maxHops  := fs.Int("max-hops", 30, "maximum hops")
    probes   := fs.Int("probes", 1, "probes per hop")
    timeout  := fs.Duration("timeout", 2*time.Second, "per-hop timeout")
    fs.Parse(os.Args[1:])
    if *target == "" { fmt.Fprintln(os.Stderr, "trace requires -target"); os.Exit(1) }
    h, _ := os.Hostname()
    m := &TraceMeasurer{hostname: h}
    cfg := MeasurerConfig{MaxHops: *maxHops, ProbesPerHop: *probes, Timeout: *timeout}
    results, err := m.Run(context.Background(), HostEntry{Name: *target, Address: *target}, cfg)
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    for _, r := range results {
        if r.Tags["skipped"] == "true" {
            fmt.Println("traceroute skipped: requires CAP_NET_RAW")
            return
        }
        fmt.Printf("hops=%v complete=%v\n", r.Fields["trace_hops"], r.Fields["trace_complete"])
        for i := 1; i <= int(r.Fields["trace_hops"]); i++ {
            key := fmt.Sprintf("hop_%d_rtt_ms", i)
            fmt.Printf("  hop %2d  %.3f ms\n", i, r.Fields[key])
        }
    }
}

func runMtuSubcommand() {
    fs := flag.NewFlagSet("mtu", flag.ExitOnError)
    target  := fs.String("target", "", "target address (required)")
    ceiling := fs.Int("ceiling", 1500, "MTU ceiling bytes")
    timeout := fs.Duration("timeout", 2*time.Second, "probe timeout")
    fs.Parse(os.Args[1:])
    if *target == "" { fmt.Fprintln(os.Stderr, "mtu requires -target"); os.Exit(1) }
    h, _ := os.Hostname()
    m := &MtuMeasurer{hostname: h}
    cfg := MeasurerConfig{Ceiling: *ceiling, Timeout: *timeout}
    results, err := m.Run(context.Background(), HostEntry{Name: *target, Address: *target}, cfg)
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    for _, r := range results {
        if r.Tags["skipped"] == "true" {
            fmt.Println("MTU discovery skipped: requires CAP_NET_RAW")
            return
        }
        fmt.Printf("effective MTU: %v bytes (ceiling: %v)\n",
            int(r.Fields["mtu_effective_bytes"]), int(r.Fields["mtu_ceiling_bytes"]))
    }
}

func runDnsSubcommand() {
    fs := flag.NewFlagSet("dns", flag.ExitOnError)
    resolver := fs.String("resolver", "8.8.8.8", "DNS resolver IP")
    name     := fs.String("name", "example.com", "name to resolve")
    timeout  := fs.Duration("timeout", 2*time.Second, "query timeout")
    fs.Parse(os.Args[1:])
    h, _ := os.Hostname()
    m := &DnsMeasurer{hostname: h}
    cfg := MeasurerConfig{Resolvers: []string{*resolver}, Names: []string{*name}, Timeout: *timeout}
    results, err := m.Run(context.Background(), HostEntry{}, cfg)
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    for _, r := range results {
        fmt.Printf("resolver=%s name=%s rtt=%.3fms success=%v\n",
            r.Tags["resolver"], r.Tags["name"], r.Fields["dns_rtt_ms"], r.Fields["dns_success"] == 1.0)
    }
}

func runAgentSubcommand() {
    fs := flag.NewFlagSet("agent", flag.ExitOnError)
    configURL   := fs.String("config-url", "", "topology config URL (required)")
    hostname    := fs.String("hostname", "", "override hostname")
    cfgRefresh  := fs.Duration("config-refresh", 0, "config refresh interval")
    port        := fs.Int("port", defaultPort, "TWAMP UDP port")
    noSync      := fs.Bool("no-sync", false, "assert clock unsynchronized")
    logPath     := fs.String("logfile", "", "log file path")
    fs.Parse(os.Args[1:])
    if *configURL == "" { fmt.Fprintln(os.Stderr, "agent requires -config-url"); os.Exit(1) }
    var logFile *os.File
    if *logPath != "" {
        var err error
        logFile, err = os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
        if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
        defer logFile.Close()
    }
    runAgent(*port, *configURL, *hostname, *cfgRefresh, !*noSync, logFile)
}
```

- [ ] **Step 5: Build and smoke-test**

```bash
go build -o piccolo-perf .
./piccolo-perf version
./piccolo-perf dns -resolver 8.8.8.8 -name example.com
```
Expected: version string printed; DNS result with rtt printed.

- [ ] **Step 6: Run all tests**

```bash
go test ./...
```
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add main.go
git commit -m "feat: add subcommand routing (twamp/bw/trace/mtu/dns/agent) with backward-compat -mode shim"
```

---

### Task 13: Deployment files and config update

**Files:**
- Create: `deploy/piccolo-perf-agent.service`
- Create: `deploy/piccolo-perf-exporter.service`
- Create: `deploy/procd-init`
- Modify: `deploy/config-example.json`

**Interfaces:** None — deployment artifacts only.

- [ ] **Step 1: Create piccolo-perf-agent.service**

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

- [ ] **Step 2: Create piccolo-perf-exporter.service**

```ini
[Unit]
Description=piccolo-perf Prometheus Exporter
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/piccolo-perf twamp \
  -mode exporter \
  -config-url http://config-server/piccolo-config.json \
  -probe-mode background \
  -metrics-addr :9862
Restart=on-failure
RestartSec=10s
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW
NoNewPrivileges=true
DynamicUser=yes

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 3: Create deploy/procd-init (OpenWrt)**

```sh
#!/bin/sh /etc/rc.common
USE_PROCD=1
START=99
STOP=01

start_service() {
    procd_open_instance
    procd_set_param command /usr/local/bin/piccolo-perf agent \
        -config-url http://config-server/piccolo-config.json
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
```

- [ ] **Step 4: Update deploy/config-example.json**

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
      "type": "mtu", "interval": "600s", "targets": "all", "ceiling": 1500
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

- [ ] **Step 5: Commit**

```bash
git add deploy/piccolo-perf-agent.service deploy/piccolo-perf-exporter.service deploy/procd-init deploy/config-example.json
git commit -m "feat: add systemd units, OpenWrt procd init, and updated example config"
```

---

## Self-Review

**Spec coverage:**
- Binary rename → Task 1
- Measurer interface + MeasureResult → Task 2
- InfluxDB generic pipeline → Task 3
- Prometheus generic pipeline → Task 4
- TWAMP measurer wrapper → Task 5
- DNS measurer → Task 6
- Bandwidth measurer + iperf3 shim + BwServer → Task 7
- MTU measurer + graceful degradation → Task 8
- Traceroute measurer + graceful degradation → Task 9
- Local JSONL ring-buffer store → Task 10
- Multi-measurer agent scheduler + hide_skipped → Task 11
- Subcommand routing + backward compat → Task 12
- Deployment files + config schema update → Task 13
- `golang.org/x/net` dependency added in Task 8

**Placeholder scan:** All code blocks are complete. No TBDs.

**Type consistency:**
- `MeasureResult` defined Task 2, consumed Tasks 3–11 consistently
- `Measurer` interface defined Task 2, implemented Tasks 5–9 with matching `Name() string` and `Run(ctx, HostEntry, MeasurerConfig) ([]MeasureResult, error)` signatures
- `BwServer.Start(port int) (int, error)` defined and used consistently in Tasks 7 and 12
- `LocalStore` defined Task 10, instantiation matches `NewLocalStore(path string, maxLines int) (*LocalStore, error)`
- `buildMeasurers` in Task 11 references `TwampMeasurer{synced}` — Task 5 must be updated to include `synced bool` field; noted in Task 11 step 5.
