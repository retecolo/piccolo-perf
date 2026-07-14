package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

// resolveHost resolves a hostname to an IP address using the OS address
// selection order defined by RFC 6724. On systems with both A and AAAA records,
// the OS will prefer IPv6 per policy table precedence.
// Falls back to net.LookupIP if the preferred-family probe fails.
func resolveHost(host string) (net.IP, error) {
	// LookupIPAddr returns addresses in RFC 6724 order as determined by the OS.
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	return addrs[0].IP, nil
}

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
