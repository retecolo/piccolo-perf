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

		bestRTT := -1.0
		reachedDst := false

		for probe := 0; probe < probes; probe++ {
			start := time.Now()
			c.SetDeadline(time.Now().Add(timeout))
			if _, err := c.WriteTo(wb, dst); err != nil {
				// write failure: this probe contributes nothing; bestRTT stays -1.0
				continue
			}

			rb := make([]byte, 1500)
			c.SetDeadline(time.Now().Add(timeout))
			_, peer, err := c.ReadFrom(rb)
			if err != nil {
				continue
			}

			rttMs := float64(time.Since(start).Microseconds()) / 1000.0
			if bestRTT < 0 || rttMs < bestRTT {
				bestRTT = rttMs
			}
			if peer.String() == dst.String() {
				reachedDst = true
			}
		}

		fields[fmt.Sprintf("hop_%d_rtt_ms", ttl)] = bestRTT
		if bestRTT >= 0 {
			reached = ttl
		}
		if reachedDst {
			fields["trace_complete"] = 1.0
			break
		}
	}

	if _, ok := fields["trace_complete"]; !ok {
		fields["trace_complete"] = 0.0
	}

	return fields, reached, nil
}
