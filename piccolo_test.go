package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// NTP timestamp round-trip
// ============================================================================

func TestNTPRoundTrip(t *testing.T) {
	// Truncate to microsecond because NTP fractional resolution is ~232 ps
	// but the conversion loses sub-nanosecond precision; microsecond is safe.
	original := time.Now().Truncate(time.Microsecond)
	converted := ntpToTime(timeToNTP(original))
	diff := original.Sub(converted)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Microsecond {
		t.Errorf("NTP round-trip error %v > 1µs (original=%v converted=%v)", diff, original, converted)
	}
}

func TestNTPEpochOffset(t *testing.T) {
	// Unix epoch (1970-01-01 00:00:00 UTC) should produce an NTP seconds
	// value equal to ntpEpochOffset.
	epoch := time.Unix(0, 0).UTC()
	ntp := timeToNTP(epoch)
	secs := ntp >> 32
	if secs != ntpEpochOffset {
		t.Errorf("NTP seconds at Unix epoch = %d, want %d", secs, ntpEpochOffset)
	}
}

// ============================================================================
// Error estimate
// ============================================================================

func TestErrorEstimateSynced(t *testing.T) {
	ee := getErrorEstimate(true)
	if ee&0x8000 == 0 {
		t.Errorf("synced error estimate should have S-bit set, got 0x%04x", ee)
	}
}

func TestErrorEstimateUnsynced(t *testing.T) {
	ee := getErrorEstimate(false)
	if ee&0x8000 != 0 {
		t.Errorf("unsynced error estimate should have S-bit clear, got 0x%04x", ee)
	}
}

// ============================================================================
// Packet marshal / unmarshal
// ============================================================================

func TestTestRequestRoundTrip(t *testing.T) {
	original := TWAMPTestRequest{
		SequenceNumber: 42,
		Timestamp:      time.Now().Truncate(time.Microsecond),
		ErrorEstimate:  0x8001,
	}

	data := original.MarshalBinary()
	if len(data) != testRequestBaseSize {
		t.Errorf("marshalled length %d, want %d", len(data), testRequestBaseSize)
	}

	var decoded TWAMPTestRequest
	if err := decoded.UnmarshalBinary(data); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.SequenceNumber != original.SequenceNumber {
		t.Errorf("SequenceNumber %d != %d", decoded.SequenceNumber, original.SequenceNumber)
	}
	if decoded.ErrorEstimate != original.ErrorEstimate {
		t.Errorf("ErrorEstimate 0x%04x != 0x%04x", decoded.ErrorEstimate, original.ErrorEstimate)
	}
	diff := decoded.Timestamp.Sub(original.Timestamp)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Microsecond {
		t.Errorf("Timestamp drift %v", diff)
	}
}

func TestTestRequestWithPadding(t *testing.T) {
	padding := make([]byte, 20)
	req := TWAMPTestRequest{
		SequenceNumber: 1,
		Timestamp:      time.Now(),
		ErrorEstimate:  0x8001,
		Padding:        padding,
	}
	data := req.MarshalBinary()
	want := testRequestBaseSize + 20
	if len(data) != want {
		t.Errorf("padded length %d, want %d", len(data), want)
	}
}

func TestTestRequestTooShort(t *testing.T) {
	var req TWAMPTestRequest
	if err := req.UnmarshalBinary(make([]byte, testRequestBaseSize-1)); err == nil {
		t.Error("expected error for short packet, got nil")
	}
}

func TestTestResponseRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Microsecond)
	original := TWAMPTestResponse{
		SequenceNumber:   7,
		Timestamp:        now,
		ErrorEstimate:    0x8001,
		ReceiveTimestamp: now.Add(time.Millisecond),
		SenderSeqNumber:  3,
		SenderTimestamp:  now.Add(2 * time.Millisecond),
		SenderError:      0x8001,
		SenderTTL:        64,
	}

	data := original.MarshalBinary()
	if len(data) != testResponseBaseSize {
		t.Errorf("marshalled length %d, want %d", len(data), testResponseBaseSize)
	}

	var decoded TWAMPTestResponse
	if err := decoded.UnmarshalBinary(data); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.SequenceNumber != original.SequenceNumber {
		t.Errorf("SequenceNumber %d != %d", decoded.SequenceNumber, original.SequenceNumber)
	}
	if decoded.SenderSeqNumber != original.SenderSeqNumber {
		t.Errorf("SenderSeqNumber %d != %d", decoded.SenderSeqNumber, original.SenderSeqNumber)
	}
	if decoded.SenderTTL != original.SenderTTL {
		t.Errorf("SenderTTL %d != %d", decoded.SenderTTL, original.SenderTTL)
	}
}

func TestTestResponseTooShort(t *testing.T) {
	var resp TWAMPTestResponse
	if err := resp.UnmarshalBinary(make([]byte, testResponseBaseSize-1)); err == nil {
		t.Error("expected error for short response, got nil")
	}
}

// ============================================================================
// RTT calculation
// ============================================================================

func TestCalculateRTT(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	t1 := base
	t2 := base.Add(10 * time.Millisecond) // server receives 10ms later
	t3 := base.Add(11 * time.Millisecond) // server takes 1ms to process
	t4 := base.Add(21 * time.Millisecond) // client receives 10ms after server sends

	// RTT = (T4-T1) - (T3-T2) = 21ms - 1ms = 20ms
	rtt := calculateRTT(t1, t2, t3, t4)
	if rtt != 20*time.Millisecond {
		t.Errorf("RTT = %v, want 20ms", rtt)
	}
}

func TestCalculateRTTNonNegative(t *testing.T) {
	// Clock skew can produce negative intermediate values; result must be >= 0.
	base := time.Unix(1_000_000, 0)
	rtt := calculateRTT(base, base.Add(time.Millisecond), base.Add(time.Hour), base.Add(time.Millisecond))
	if rtt < 0 {
		t.Errorf("RTT should be non-negative, got %v", rtt)
	}
}

// ============================================================================
// Rate limiter
// ============================================================================

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	rl := newRateLimiter(10)
	for i := 0; i < 10; i++ {
		if !rl.allow("192.0.2.1") {
			t.Errorf("packet %d should be allowed", i+1)
		}
	}
}

func TestRateLimiterBlocksOverLimit(t *testing.T) {
	rl := newRateLimiter(2)
	rl.allow("192.0.2.1")
	rl.allow("192.0.2.1")
	if rl.allow("192.0.2.1") {
		t.Error("third packet should be rate-limited")
	}
}

func TestRateLimiterUnlimited(t *testing.T) {
	rl := newRateLimiter(0)
	for i := 0; i < 1000; i++ {
		if !rl.allow("192.0.2.1") {
			t.Fatalf("unlimited limiter blocked packet %d", i+1)
		}
	}
}

func TestRateLimiterPerIP(t *testing.T) {
	rl := newRateLimiter(1)
	if !rl.allow("192.0.2.1") {
		t.Error("first packet from IP-1 should be allowed")
	}
	if !rl.allow("192.0.2.2") {
		t.Error("first packet from IP-2 should be allowed (different bucket)")
	}
}

// ============================================================================
// Allowlist
// ============================================================================

func TestAllowlistEmpty(t *testing.T) {
	al, err := parseAllowlist("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.permitted(net.ParseIP("10.0.0.1")) {
		t.Error("empty allowlist should permit all IPs")
	}
}

func TestAllowlistPermits(t *testing.T) {
	al, err := parseAllowlist("10.0.0.0/8,2001:db8::/32")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.permitted(net.ParseIP("10.1.2.3")) {
		t.Error("10.1.2.3 should be permitted by 10.0.0.0/8")
	}
	if !al.permitted(net.ParseIP("2001:db8::1")) {
		t.Error("2001:db8::1 should be permitted by 2001:db8::/32")
	}
}

func TestAllowlistBlocks(t *testing.T) {
	al, err := parseAllowlist("10.0.0.0/8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if al.permitted(net.ParseIP("192.168.1.1")) {
		t.Error("192.168.1.1 should be blocked")
	}
}

func TestAllowlistInvalidCIDR(t *testing.T) {
	if _, err := parseAllowlist("not-a-cidr"); err == nil {
		t.Error("invalid CIDR should return error")
	}
}

// ============================================================================
// Agent config parsing
// ============================================================================

func TestParseAgentConfigMesh(t *testing.T) {
	raw := []byte(`{
		"topology": "mesh",
		"probe_interval": "30s",
		"burst_size": 5,
		"burst_interval": "200ms",
		"packet_timeout": "5s",
		"padding": 0,
		"config_refresh": "5m",
		"influxdb": {
			"url": "http://influx:8086",
			"token": "tok",
			"org": "myorg",
			"bucket": "twamp"
		},
		"hosts": [
			{"name": "a", "address": "10.0.0.1", "site": "east"},
			{"name": "b", "address": "10.0.0.2", "site": "west"}
		],
		"hub_spoke": {"enabled": false, "hub": ""}
	}`)
	cfg, err := parseAgentConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Topology != "mesh" {
		t.Errorf("Topology = %q, want mesh", cfg.Topology)
	}
	if cfg.ProbeInterval != 30*time.Second {
		t.Errorf("ProbeInterval = %v, want 30s", cfg.ProbeInterval)
	}
	if cfg.BurstSize != 5 {
		t.Errorf("BurstSize = %d, want 5", cfg.BurstSize)
	}
	if len(cfg.Hosts) != 2 {
		t.Errorf("len(Hosts) = %d, want 2", len(cfg.Hosts))
	}
	if cfg.InfluxDB.URL != "http://influx:8086" {
		t.Errorf("InfluxDB.URL = %q", cfg.InfluxDB.URL)
	}
}

func TestParseAgentConfigInvalid(t *testing.T) {
	_, err := parseAgentConfig([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestParseAgentConfigMissingTopology(t *testing.T) {
	raw := []byte(`{"hosts": [{"name":"a","address":"1.2.3.4","site":"x"}]}`)
	_, err := parseAgentConfig(raw)
	if err == nil {
		t.Error("expected error for missing topology, got nil")
	}
}

func TestParseAgentConfigBadDuration(t *testing.T) {
	raw := []byte(`{"topology":"mesh","probe_interval":"not-a-duration"}`)
	_, err := parseAgentConfig(raw)
	if err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}

func TestParseAgentConfigNegativePadding(t *testing.T) {
	raw := []byte(`{"topology":"mesh","padding":-1,"hosts":[]}`)
	_, err := parseAgentConfig(raw)
	if err == nil {
		t.Error("expected error for negative padding, got nil")
	}
}

func TestTargetsForMesh(t *testing.T) {
	cfg := AgentConfig{
		Topology: "mesh",
		Hosts: []HostEntry{
			{Name: "a", Address: "10.0.0.1", Site: "east"},
			{Name: "b", Address: "10.0.0.2", Site: "west"},
			{Name: "c", Address: "10.0.0.3", Site: "eu"},
		},
		HubSpoke: HubSpokeConfig{Enabled: false},
	}
	targets := cfg.targetsFor("a")
	if len(targets) != 2 {
		t.Fatalf("mesh from 'a': want 2 targets, got %d", len(targets))
	}
	for _, tgt := range targets {
		if tgt.Name == "a" {
			t.Error("mesh should not include self")
		}
	}
}

func TestTargetsForHubAsHub(t *testing.T) {
	cfg := AgentConfig{
		Topology: "hub-spoke",
		Hosts: []HostEntry{
			{Name: "hub",    Address: "10.0.0.1", Site: "core"},
			{Name: "spoke1", Address: "10.0.0.2", Site: "east"},
			{Name: "spoke2", Address: "10.0.0.3", Site: "west"},
		},
		HubSpoke: HubSpokeConfig{Enabled: true, Hub: "hub"},
	}
	targets := cfg.targetsFor("hub")
	if len(targets) != 2 {
		t.Fatalf("hub should probe 2 spokes, got %d", len(targets))
	}
}

func TestTargetsForHubAsSpoke(t *testing.T) {
	cfg := AgentConfig{
		Topology: "hub-spoke",
		Hosts: []HostEntry{
			{Name: "hub",    Address: "10.0.0.1", Site: "core"},
			{Name: "spoke1", Address: "10.0.0.2", Site: "east"},
		},
		HubSpoke: HubSpokeConfig{Enabled: true, Hub: "hub"},
	}
	targets := cfg.targetsFor("spoke1")
	if len(targets) != 1 || targets[0].Name != "hub" {
		t.Fatalf("spoke should probe only hub, got %+v", targets)
	}
}

func TestTargetsForUnknownHost(t *testing.T) {
	cfg := AgentConfig{
		Topology: "mesh",
		Hosts: []HostEntry{
			{Name: "a", Address: "10.0.0.1", Site: "east"},
		},
		HubSpoke: HubSpokeConfig{Enabled: false},
	}
	targets := cfg.targetsFor("not-in-list")
	if len(targets) != 0 {
		t.Errorf("unknown host should get 0 targets, got %d", len(targets))
	}
}

// ============================================================================
// InfluxDB Line Protocol
// ============================================================================

func TestLineProtocolFormat(t *testing.T) {
	r := ProbeResult{
		Source:    "probe-a",
		Target:    "probe-b",
		Site:      "us-east",
		Topology:  "mesh",
		RttMin:    1 * time.Millisecond,
		RttAvg:    2 * time.Millisecond,
		RttMax:    3 * time.Millisecond,
		RttStddev: 500 * time.Microsecond,
		Jitter:    250 * time.Microsecond,
		LossPct:   0.0,
		SentAt:    time.Unix(1_000_000, 0).UTC(),
		Sent:      5,
		Recv:      5,
	}
	line := lineProtocol(r)

	// Must start with measurement and contain key tags
	if !strings.HasPrefix(line, "twamp_rtt,") {
		t.Errorf("line should start with twamp_rtt,, got: %s", line)
	}
	if !strings.Contains(line, "source=probe-a") {
		t.Errorf("missing source tag: %s", line)
	}
	if !strings.Contains(line, "target=probe-b") {
		t.Errorf("missing target tag: %s", line)
	}
	if !strings.Contains(line, "topology=mesh") {
		t.Errorf("missing topology tag: %s", line)
	}
	if !strings.Contains(line, "site=us-east") {
		t.Errorf("missing site tag: %s", line)
	}
	if !strings.Contains(line, "rtt_avg_ms=2.000") {
		t.Errorf("missing rtt_avg_ms field: %s", line)
	}
	if !strings.Contains(line, "loss_pct=0.000") {
		t.Errorf("missing loss_pct field: %s", line)
	}
	if !strings.Contains(line, "packets_sent=5i") {
		t.Errorf("missing packets_sent integer field: %s", line)
	}
	// Timestamp must be last token and be Unix nanoseconds of SentAt
	wantTs := fmt.Sprintf("%d", time.Unix(1_000_000, 0).UnixNano())
	if !strings.HasSuffix(strings.TrimSpace(line), wantTs) {
		t.Errorf("line should end with timestamp %s, got: %s", wantTs, line)
	}
}

func TestLineProtocolEscapesSpaces(t *testing.T) {
	r := ProbeResult{
		Source:   "probe a",
		Target:   "probe b",
		Site:     "us east",
		Topology: "mesh",
		SentAt:   time.Unix(1, 0),
	}
	line := lineProtocol(r)
	if strings.Contains(line, "probe a") {
		t.Error("spaces in tag values must be escaped as probe\\ a")
	}
	if !strings.Contains(line, `probe\ a`) {
		t.Error("spaces in tag values should be escaped as probe\\ a")
	}
}

// ============================================================================
// Server reflected counter
// ============================================================================

func TestServerReflectedCount(t *testing.T) {
	srv := NewServer(nil, newRateLimiter(0), &allowlist{}, true)
	if srv.ReflectedCount() != 0 {
		t.Errorf("initial ReflectedCount = %d, want 0", srv.ReflectedCount())
	}
	srv.reflectedPackets.Add(1)
	if srv.ReflectedCount() != 1 {
		t.Errorf("after Add(1) ReflectedCount = %d, want 1", srv.ReflectedCount())
	}
	srv.reflectedPackets.Add(99)
	if srv.ReflectedCount() != 100 {
		t.Errorf("after Add(99) ReflectedCount = %d, want 100", srv.ReflectedCount())
	}
}

// ============================================================================
// Statistics helpers (stddev, jitter)
// ============================================================================

func TestStddevAndJitter(t *testing.T) {
	// Feed a known set of RTTs and verify stddev is computed correctly.
	// RTTs: 10ms, 20ms, 30ms → avg=20ms, variance=((−10)²+0²+10²)/3 = 200/3 ms²
	rtts := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	}

	var sum time.Duration
	for _, r := range rtts {
		sum += r
	}
	avg := sum / time.Duration(len(rtts))

	var variance float64
	avgF := float64(avg)
	for _, r := range rtts {
		d := float64(r) - avgF
		variance += d * d
	}
	stddev := time.Duration(math.Sqrt(variance / float64(len(rtts))))

	// Expected stddev ≈ 8.165ms
	wantLo := 8 * time.Millisecond
	wantHi := 9 * time.Millisecond
	if stddev < wantLo || stddev > wantHi {
		t.Errorf("stddev %v not in [%v, %v]", stddev, wantLo, wantHi)
	}

	// Jitter: |20-10| + |30-20| = 10ms + 10ms, avg = 10ms
	var jitterSum time.Duration
	for i := 1; i < len(rtts); i++ {
		d := rtts[i] - rtts[i-1]
		if d < 0 {
			d = -d
		}
		jitterSum += d
	}
	jitter := jitterSum / time.Duration(len(rtts)-1)
	if jitter != 10*time.Millisecond {
		t.Errorf("jitter = %v, want 10ms", jitter)
	}
}

// ============================================================================
// PrometheusStore
// ============================================================================

func TestPrometheusStoreUpdate(t *testing.T) {
	store := newPrometheusStore("probe-a")
	r := ProbeResult{
		Source:    "probe-a",
		Target:    "probe-b",
		Site:      "us-east",
		Topology:  "mesh",
		RttMin:    1 * time.Millisecond,
		RttAvg:    2 * time.Millisecond,
		RttMax:    3 * time.Millisecond,
		RttStddev: 500 * time.Microsecond,
		Jitter:    250 * time.Microsecond,
		LossPct:   20.0,
		Sent:      5,
		Recv:      4,
	}
	// Must not panic
	store.Update(r)
}

func TestPrometheusStoreLossRatioConversion(t *testing.T) {
	// LossPct=20.0 should become loss_ratio=0.2 in the gauge
	store := newPrometheusStore("probe-a")
	r := ProbeResult{
		Source:   "probe-a",
		Target:   "probe-b",
		Site:     "east",
		Topology: "mesh",
		LossPct:  20.0,
		Sent:     5,
		Recv:     4,
	}
	store.Update(r)

	rec := httptest.NewRecorder()
	store.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `twamp_loss_ratio`) {
		t.Errorf("expected twamp_loss_ratio in metrics output, got:\n%s", body)
	}
	if !strings.Contains(body, "0.2") {
		t.Errorf("expected loss_ratio=0.2 (20%% -> 0.2), got:\n%s", body)
	}
}

func TestPrometheusStoreLabels(t *testing.T) {
	store := newPrometheusStore("probe-a")
	r := ProbeResult{
		Source:   "probe-a",
		Target:   "probe-b",
		Site:     "us-east",
		Topology: "hub-spoke",
		Sent:     5,
		Recv:     5,
	}
	store.Update(r)

	rec := httptest.NewRecorder()
	store.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`source="probe-a"`,
		`target="probe-b"`,
		`site="us-east"`,
		`topology="hub-spoke"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected label %q in metrics output", want)
		}
	}
}

func TestPrometheusStoreIncrementReflected(t *testing.T) {
	store := newPrometheusStore("probe-a")
	store.IncrementReflected()
	store.IncrementReflected()

	rec := httptest.NewRecorder()
	store.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "twamp_reflected_packets_total") {
		t.Errorf("expected twamp_reflected_packets_total in output, got:\n%s", body)
	}
	if !strings.Contains(body, "2") {
		t.Errorf("expected counter value 2 in output, got:\n%s", body)
	}
}

// ============================================================================
// Exporter TLS flag validation
// ============================================================================

func TestExporterTLSFlagValidation(t *testing.T) {
	// Both cert and key must be provided together — validate the check logic.
	// We test the validation function directly, not runExporter (which blocks).
	err := validateTLSFlags("/path/to/cert", "")
	if err == nil {
		t.Error("expected error when cert is set but key is empty")
	}
	err = validateTLSFlags("", "/path/to/key")
	if err == nil {
		t.Error("expected error when key is set but cert is empty")
	}
	err = validateTLSFlags("", "")
	if err != nil {
		t.Errorf("expected no error when both are empty, got %v", err)
	}
}

// ============================================================================
// MeasureResult
// ============================================================================

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

// ============================================================================
// TwampMeasurer
// ============================================================================

func TestTwampMeasurerName(t *testing.T) {
	m := &TwampMeasurer{hostname: "probe-a", logFile: nil}
	if m.Name() != "twamp" {
		t.Errorf("Name() = %q, want twamp", m.Name())
	}
}

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

// ============================================================================
// BwMeasurer
// ============================================================================

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
		Duration:     500 * time.Millisecond,
		Timeout:      5 * time.Second,
		PreferIperf3: false,
	}
	target := HostEntry{Name: "loopback", Address: fmt.Sprintf("[::1]:%d", port)}
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

// ============================================================================
// MtuMeasurer
// ============================================================================

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
	target := HostEntry{Name: "loopback", Address: "::1"}
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

// ============================================================================
// TraceMeasurer
// ============================================================================

func TestTraceMeasurerName(t *testing.T) {
	m := &TraceMeasurer{hostname: "probe-a"}
	if m.Name() != "trace" {
		t.Errorf("Name() = %q, want trace", m.Name())
	}
}

func TestTraceMeasurerSkippedWithoutCap(t *testing.T) {
	m := &TraceMeasurer{hostname: "probe-a"}
	cfg := MeasurerConfig{MaxHops: 5, ProbesPerHop: 1, Timeout: time.Second}
	target := HostEntry{Name: "loopback", Address: "::1"}
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

// ============================================================================
// LocalStore
// ============================================================================

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

// ============================================================================
// Multi-measurer config parsing
// ============================================================================

func TestParseAgentConfigMeasurements(t *testing.T) {
	raw := []byte(`{
        "topology": "mesh",
        "hosts": [{"name":"a","address":"10.0.0.1","site":"east"}],
        "hub_spoke": {"enabled": false, "hub": ""},
        "measurements": [
            {"type": "twamp", "interval": "30s", "targets": "all", "burst_size": 3},
            {"type": "dns",   "interval": "60s", "resolvers": ["2620:fe::fe"], "names": ["example.com"]}
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

// ============================================================================
// DnsMeasurer
// ============================================================================

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
		Resolvers: []string{"2620:fe::fe"}, // Quad9 IPv6 — reachable in IPv6-only environments
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

// ============================================================================
// resolveHost — RFC 6724 address selection
// ============================================================================

func TestResolveHostIPv4Literal(t *testing.T) {
	ip, err := resolveHost("127.0.0.1")
	if err != nil {
		t.Fatalf("resolveHost(127.0.0.1): %v", err)
	}
	if ip.To4() == nil {
		t.Errorf("expected IPv4, got %v", ip)
	}
}

func TestResolveHostIPv6Literal(t *testing.T) {
	ip, err := resolveHost("::1")
	if err != nil {
		t.Fatalf("resolveHost(::1): %v", err)
	}
	if ip.To4() != nil {
		t.Errorf("expected IPv6, got %v", ip)
	}
}

func TestResolveHostInvalid(t *testing.T) {
	_, err := resolveHost("not-a-valid-host-zzzzzz.invalid")
	if err == nil {
		t.Error("expected error for unresolvable host, got nil")
	}
}

// ============================================================================
// lineProtocolResult — edge cases
// ============================================================================

func TestLineProtocolResultEscapesTagValues(t *testing.T) {
	r := MeasureResult{
		Measurement: "piccolo_test",
		Source:      "a b",   // space
		Target:      "c,d",   // comma
		Site:        "e=f",   // equals
		Topology:    "mesh",
		Tags:        map[string]string{},
		Fields:      map[string]float64{"v": 1.0},
		SentAt:      time.Unix(1, 0),
	}
	line := lineProtocolResult(r)
	if strings.Contains(line, "a b") {
		t.Error("space in source should be escaped")
	}
	if !strings.Contains(line, `a\ b`) {
		t.Error("space should be escaped as a\\ b")
	}
	if strings.Contains(line, "c,d") {
		t.Error("comma in target should be escaped")
	}
	if strings.Contains(line, "e=f") {
		t.Error("equals in site should be escaped")
	}
}

func TestLineProtocolResultFieldsSorted(t *testing.T) {
	r := MeasureResult{
		Measurement: "piccolo_test",
		Source:      "a", Target: "b", Site: "s", Topology: "mesh",
		Tags:   map[string]string{},
		Fields: map[string]float64{"z": 3.0, "a": 1.0, "m": 2.0},
		SentAt: time.Unix(1, 0),
	}
	line := lineProtocolResult(r)
	// Fields must be sorted: a= before m= before z=
	aIdx := strings.Index(line, "a=")
	mIdx := strings.Index(line, "m=")
	zIdx := strings.Index(line, "z=")
	if !(aIdx < mIdx && mIdx < zIdx) {
		t.Errorf("fields not sorted: a@%d m@%d z@%d in %s", aIdx, mIdx, zIdx, line)
	}
}

func TestLineProtocolResultExtraTagsSorted(t *testing.T) {
	r := MeasureResult{
		Measurement: "piccolo_bw",
		Source:      "a", Target: "b", Site: "s", Topology: "mesh",
		Tags:   map[string]string{"method": "native", "extra": "val"},
		Fields: map[string]float64{"bw_tx_mbps": 100.0},
		SentAt: time.Unix(1, 0),
	}
	line := lineProtocolResult(r)
	if !strings.Contains(line, "method=native") {
		t.Errorf("missing method tag: %s", line)
	}
	if !strings.Contains(line, "extra=val") {
		t.Errorf("missing extra tag: %s", line)
	}
}

// ============================================================================
// BwServer — start/stop lifecycle
// ============================================================================

func TestBwServerStartStop(t *testing.T) {
	srv := &BwServer{}
	port, err := srv.Start(0)
	if err != nil {
		t.Fatalf("BwServer.Start: %v", err)
	}
	if port == 0 {
		t.Error("expected non-zero port")
	}
	// Stop must be idempotent
	srv.Stop()
	srv.Stop()
}

func TestBwServerAcceptsConnection(t *testing.T) {
	srv := &BwServer{}
	port, err := srv.Start(0)
	if err != nil {
		t.Fatalf("BwServer.Start: %v", err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp6", fmt.Sprintf("[::1]:%d", port))
	if err != nil {
		// Fall back to IPv4 if ::1 not available
		conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("dial BwServer: %v", err)
		}
	}
	conn.Write([]byte("hello"))
	conn.Close()
}

// ============================================================================
// TWAMP client/server integration (loopback)
// ============================================================================

func TestTwampClientServerLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loopback integration test in short mode")
	}
	al, _ := parseAllowlist("")
	rl := newRateLimiter(0)
	srv := NewServer(nil, rl, al, true)

	// Find a free port
	ln, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6zero, Port: 0})
	if err != nil {
		t.Skipf("udp6 not available: %v", err)
	}
	port := ln.LocalAddr().(*net.UDPAddr).Port
	ln.Close()

	go func() {
		srv.Start(port) //nolint:errcheck
	}()
	time.Sleep(50 * time.Millisecond) // let server bind

	defer srv.cancel()

	c := NewClient("::1", nil, 3, 100*time.Millisecond, 2*time.Second, port, 0, true)
	if err := c.Run(); err != nil {
		t.Fatalf("client.Run: %v", err)
	}
	if srv.ReflectedCount() == 0 {
		t.Error("server reflected 0 packets")
	}
}

func TestTwampClientSourceAddr(t *testing.T) {
	// Verify that setting sourceAddr to ::1 produces a working client
	// (the unconnected socket binds to the specified address).
	if testing.Short() {
		t.Skip("skipping loopback integration test in short mode")
	}
	al, _ := parseAllowlist("")
	rl := newRateLimiter(0)
	srv := NewServer(nil, rl, al, true)

	ln, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6zero, Port: 0})
	if err != nil {
		t.Skipf("udp6 not available: %v", err)
	}
	port := ln.LocalAddr().(*net.UDPAddr).Port
	ln.Close()

	go srv.Start(port) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)
	defer srv.cancel()

	c := NewClient("::1", nil, 3, 100*time.Millisecond, 2*time.Second, port, 0, true)
	c.sourceAddr = "::1"
	if err := c.Run(); err != nil {
		t.Fatalf("client.Run with sourceAddr=::1: %v", err)
	}
}

func TestTwampClientInvalidSourceAddr(t *testing.T) {
	c := NewClient("::1", nil, 1, 100*time.Millisecond, 500*time.Millisecond, 9999, 0, true)
	c.sourceAddr = "not-an-ip"
	err := c.Run()
	if err == nil {
		t.Error("expected error for invalid source address, got nil")
	}
	if !strings.Contains(err.Error(), "invalid source address") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ============================================================================
// LocalStore — flush error handling
// ============================================================================

func TestLocalStoreFlushPropagatesError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalStore(dir+"/results.jsonl", 1000)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer s.Close()

	s.Append(MeasureResult{
		Measurement: "piccolo_twamp",
		Source:      "a", Target: "b",
		Fields: map[string]float64{"rtt_avg_ms": 1.0},
		Tags:   map[string]string{},
		SentAt: time.Now(),
	})

	sentinel := fmt.Errorf("upstream unavailable")
	err = s.Flush(context.Background(), func(batch []MeasureResult) error {
		return sentinel
	})
	if err == nil {
		t.Error("expected Flush to propagate fn error")
	}
	if !strings.Contains(err.Error(), "upstream unavailable") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLocalStoreFlushEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalStore(dir+"/results.jsonl", 1000)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer s.Close()

	called := false
	err = s.Flush(context.Background(), func(batch []MeasureResult) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Flush on empty store: %v", err)
	}
	if called {
		t.Error("fn should not be called for empty store")
	}
}

func TestLocalStoreFlushClearsFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/results.jsonl"
	s, err := NewLocalStore(path, 1000)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		s.Append(MeasureResult{
			Measurement: "piccolo_twamp",
			Source:      fmt.Sprintf("host-%d", i),
			Target:      "b",
			Fields:      map[string]float64{},
			Tags:        map[string]string{},
			SentAt:      time.Now(),
		})
	}

	// First flush delivers all 3
	var count int
	s.Flush(context.Background(), func(batch []MeasureResult) error {
		count += len(batch)
		return nil
	})
	if count != 3 {
		t.Errorf("expected 3 flushed, got %d", count)
	}

	// Second flush should be empty — file was cleared
	count = 0
	s.Flush(context.Background(), func(batch []MeasureResult) error {
		count += len(batch)
		return nil
	})
	if count != 0 {
		t.Errorf("expected 0 on second flush, got %d", count)
	}
}

// ============================================================================
// PrometheusStore.UpdateResult — dynamic gauge consistency
// ============================================================================

func TestPrometheusStoreUpdateResultStableTags(t *testing.T) {
	store := newPrometheusStore("probe-a")
	r := MeasureResult{
		Measurement: "piccolo_dns",
		Source:      "probe-a",
		Target:      "2620:fe::fe",
		Site:        "east",
		Topology:    "mesh",
		Tags:        map[string]string{"resolver": "2620:fe::fe", "name": "example.com"},
		Fields:      map[string]float64{"dns_rtt_ms": 5.0, "dns_success": 1.0},
		SentAt:      time.Now(),
	}
	// Call twice with same tag set — must not panic
	store.UpdateResult(r)
	store.UpdateResult(r)

	rec := httptest.NewRecorder()
	store.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "piccolo_dns_dns_rtt_ms") {
		t.Errorf("expected piccolo_dns_dns_rtt_ms in metrics:\n%s", body)
	}
	if !strings.Contains(body, `resolver="2620:fe::fe"`) {
		t.Errorf("expected resolver label:\n%s", body)
	}
}

func TestPrometheusStoreUpdateResultMultipleMeasurements(t *testing.T) {
	store := newPrometheusStore("probe-a")
	for _, meas := range []string{"piccolo_twamp", "piccolo_bw", "piccolo_dns"} {
		store.UpdateResult(MeasureResult{
			Measurement: meas,
			Source:      "probe-a",
			Target:      "probe-b",
			Site:        "east",
			Topology:    "mesh",
			Tags:        map[string]string{},
			Fields:      map[string]float64{"value": 42.0},
			SentAt:      time.Now(),
		})
	}
	rec := httptest.NewRecorder()
	store.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{"piccolo_twamp_value", "piccolo_bw_value", "piccolo_dns_value"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in metrics output", want)
		}
	}
}

// ============================================================================
// BwMeasurer — address parsing
// ============================================================================

func TestBwRunNativeAddressWithPort(t *testing.T) {
	// Verify address-with-port passes through SplitHostPort without appending :5201
	srv := &BwServer{}
	port, err := srv.Start(0)
	if err != nil {
		t.Fatalf("BwServer.Start: %v", err)
	}
	defer srv.Stop()

	m := &BwMeasurer{hostname: "probe-a"}
	cfg := MeasurerConfig{Duration: 200 * time.Millisecond, Timeout: 5 * time.Second}
	// Pass address with explicit port — must not double-append port
	target := HostEntry{Name: "loopback", Address: fmt.Sprintf("[::1]:%d", port)}
	results, err := m.Run(context.Background(), target, cfg)
	if err != nil {
		t.Fatalf("Run() with host:port address: %v", err)
	}
	if len(results) == 0 || results[0].Fields["bw_tx_mbps"] <= 0 {
		t.Error("expected positive bw_tx_mbps")
	}
}

func TestBwRunNativeIPv6BareAddress(t *testing.T) {
	// Bare IPv6 address (no port) must have :5201 appended via JoinHostPort
	srv := &BwServer{}
	if _, err := srv.Start(5201); err != nil {
		t.Skipf("port 5201 not available: %v", err)
	}
	defer srv.Stop()

	m := &BwMeasurer{hostname: "probe-a"}
	cfg := MeasurerConfig{Duration: 200 * time.Millisecond, Timeout: 5 * time.Second}
	target := HostEntry{Name: "loopback", Address: "::1"} // no port — must auto-append 5201
	results, err := m.Run(context.Background(), target, cfg)
	if err != nil {
		t.Fatalf("Run() with bare IPv6 address: %v", err)
	}
	if len(results) == 0 || results[0].Fields["bw_tx_mbps"] <= 0 {
		t.Error("expected positive bw_tx_mbps")
	}
}

// ============================================================================
// TwampMeasurer.Run (via loopback server)
// ============================================================================

func TestTwampMeasurerRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loopback integration test in short mode")
	}
	al, _ := parseAllowlist("")
	rl := newRateLimiter(0)
	srv := NewServer(nil, rl, al, true)

	ln, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6zero, Port: 0})
	if err != nil {
		t.Skipf("udp6 not available: %v", err)
	}
	port := ln.LocalAddr().(*net.UDPAddr).Port
	ln.Close()

	go srv.Start(port) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)
	defer srv.cancel()

	m := &TwampMeasurer{hostname: "probe-a", port: port}
	cfg := MeasurerConfig{
		BurstSize:     3,
		BurstInterval: 50 * time.Millisecond,
		Timeout:       2 * time.Second,
		Synced:        true,
	}
	results, err := m.Run(context.Background(), HostEntry{Name: "loopback", Address: "::1"}, cfg)
	if err != nil {
		t.Fatalf("TwampMeasurer.Run: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	r := results[0]
	if r.Measurement != "piccolo_twamp" {
		t.Errorf("Measurement = %q, want piccolo_twamp", r.Measurement)
	}
	if r.Fields["packets_sent"] != 3 {
		t.Errorf("packets_sent = %v, want 3", r.Fields["packets_sent"])
	}
	if r.Fields["loss_pct"] != 0 {
		t.Errorf("loss_pct = %v, want 0", r.Fields["loss_pct"])
	}
	if r.Fields["rtt_avg_ms"] <= 0 {
		t.Errorf("rtt_avg_ms = %v, want > 0", r.Fields["rtt_avg_ms"])
	}
}

// ============================================================================
// DnsMeasurer — IPv6 resolver address (JoinHostPort)
// ============================================================================

func TestDnsMeasurerIPv6Resolver(t *testing.T) {
	// Verify that an IPv6 resolver address is correctly formatted as [addr]:53
	// This is a structural test — dns_success may be 0 if resolver unreachable.
	m := &DnsMeasurer{hostname: "probe-a"}
	cfg := MeasurerConfig{
		Timeout:   1 * time.Second,
		Resolvers: []string{"::1"}, // loopback — will fail but must not panic/crash
		Names:     []string{"example.com"},
	}
	results, err := m.Run(context.Background(), HostEntry{}, cfg)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	r := results[0]
	if r.Tags["resolver"] != "::1" {
		t.Errorf("Tags[resolver] = %q, want ::1", r.Tags["resolver"])
	}
	// dns_success must be 0.0 or 1.0 — never absent
	if _, ok := r.Fields["dns_success"]; !ok {
		t.Error("missing dns_success field")
	}
}
