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
			return d.DialContext(ctx, "udp", net.JoinHostPort(resolver, "53"))
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
			"dns_rtt_ms":  rttMs,
			"dns_success": success,
		},
		SentAt: time.Now(),
	}
}
