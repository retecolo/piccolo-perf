package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"
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
	if _, _, err := net.SplitHostPort(addr); err != nil {
		// No port present — addr is a bare host (IPv4 or IPv6 literal or hostname).
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
			"bw_tx_mbps":    txMbps,
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
			"bw_tx_mbps":    parsed.End.SumSent.BitsPerSecond / 1e6,
			"bw_rx_mbps":    parsed.End.SumReceived.BitsPerSecond / 1e6,
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
// Prefers tcp6 (dual-stack on most kernels) and falls back to tcp on platforms
// where IPv6 is unavailable.
func (s *BwServer) Start(port int) (int, error) {
	addr := fmt.Sprintf("[::]:%d", port)
	ln, err := net.Listen("tcp6", addr)
	if err != nil {
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return 0, err
		}
	}
	s.listener = ln
	go s.accept()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (s *BwServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			io.Copy(io.Discard, c)
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
