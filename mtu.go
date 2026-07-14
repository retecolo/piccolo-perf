package main

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// MtuMeasurer discovers effective path MTU using ICMP with DF bit set.
// Requires CAP_NET_RAW; degrades gracefully without it.
// Supports both IPv4 (ICMPv4 Fragmentation Needed) and IPv6 (ICMPv6 Packet Too Big).
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

// discover resolves addr and dispatches to the appropriate IP-family prober.
// When a hostname resolves to both A and AAAA records, IPv6 is preferred.
func (m *MtuMeasurer) discover(ctx context.Context, addr string, ceiling int, timeout time.Duration) (int, error) {
	ip := net.ParseIP(addr)
	if ip == nil {
		ips, err := net.LookupIP(addr)
		if err != nil || len(ips) == 0 {
			return 0, fmt.Errorf("resolve %s: %w", addr, err)
		}
		ip = preferIPv6(ips)
	}

	if ip.To4() != nil {
		return m.discoverV4(ctx, ip, ceiling, timeout)
	}
	return m.discoverV6(ctx, ip, ceiling, timeout)
}

func (m *MtuMeasurer) discoverV4(ctx context.Context, ip net.IP, ceiling int, timeout time.Duration) (int, error) {
	dst := &net.IPAddr{IP: ip}

	pc, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return 0, fmt.Errorf("raw socket: %w", err)
	}
	defer pc.Close()

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
		if payloadSize < 0 {
			break
		}

		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{ID: 1, Seq: mid, Data: make([]byte, payloadSize)},
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

		rb := make([]byte, ceiling+28)
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

// discoverV6 probes path MTU over IPv6 using ICMPv6 Echo Request.
// In IPv6, all packets are implicitly DF — no setsockopt needed.
// The binary search uses ICMPv6 Packet Too Big (type 2) responses to narrow the range.
func (m *MtuMeasurer) discoverV6(ctx context.Context, ip net.IP, ceiling int, timeout time.Duration) (int, error) {
	dst := &net.IPAddr{IP: ip}

	pc, err := icmp.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		return 0, fmt.Errorf("raw socket (IPv6): %w", err)
	}
	defer pc.Close()

	p := pc.IPv6PacketConn()

	lo, hi := 1280, ceiling // IPv6 minimum MTU is 1280
	effective := 0

	for lo <= hi {
		mid := (lo + hi) / 2
		payloadSize := mid - 48 // 40 IPv6 + 8 ICMPv6 header
		if payloadSize < 0 {
			break
		}

		msg := icmp.Message{
			Type: ipv6.ICMPTypeEchoRequest,
			Code: 0,
			Body: &icmp.Echo{ID: 1, Seq: mid, Data: make([]byte, payloadSize)},
		}
		wb, err := msg.Marshal(nil)
		if err != nil {
			break
		}

		cm := &ipv6.ControlMessage{HopLimit: 64}
		pc.SetDeadline(time.Now().Add(timeout))
		if _, err := p.WriteTo(wb, cm, dst); err != nil {
			hi = mid - 1
			continue
		}

		rb := make([]byte, ceiling+48)
		pc.SetDeadline(time.Now().Add(timeout))
		n, _, _, err := p.ReadFrom(rb)
		if err != nil || n == 0 {
			hi = mid - 1
			continue
		}

		rm, err := icmp.ParseMessage(58, rb[:n]) // 58 = ICMPv6 protocol number
		if err != nil {
			hi = mid - 1
			continue
		}
		if rm.Type == ipv6.ICMPTypeEchoReply {
			effective = mid
			lo = mid + 1
		} else {
			// Packet Too Big or any other error response — size too large
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
