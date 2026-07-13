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
