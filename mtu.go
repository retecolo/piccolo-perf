package main

import (
	"context"
	"fmt"
	"net"
	"syscall"
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

	// Open a raw ICMP socket via net.ListenPacket so we can access SyscallConn.
	pc, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return 0, fmt.Errorf("raw socket: %w", err) // CAP_NET_RAW missing
	}
	defer pc.Close()

	// Set DF bit via platform-specific helper.
	sc, ok := pc.(syscall.Conn)
	if !ok {
		return 0, fmt.Errorf("PacketConn does not implement syscall.Conn")
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("syscall conn: %w", err)
	}
	if dfErr := setDFBit(rc); dfErr != nil {
		return 0, fmt.Errorf("set DF: %w", dfErr)
	}

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

		pc.SetDeadline(time.Now().Add(timeout))
		if _, err := pc.WriteTo(wb, dst); err != nil {
			hi = mid - 1
			continue
		}

		rb := make([]byte, 1500)
		pc.SetDeadline(time.Now().Add(timeout))
		n, _, err := pc.ReadFrom(rb)
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
