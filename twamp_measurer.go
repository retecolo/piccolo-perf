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
			if r < minR {
				minR = r
			}
			if r > maxR {
				maxR = r
			}
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
			if d < 0 {
				d = -d
			}
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
