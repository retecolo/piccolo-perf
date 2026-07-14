package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"
)

// AgentConfig is the parsed topology config fetched from the config server.
type AgentConfig struct {
	Topology      string
	ProbeInterval time.Duration
	BurstSize     int
	BurstInterval time.Duration
	PacketTimeout time.Duration
	Padding       int
	ConfigRefresh time.Duration
	InfluxDB      InfluxConfig
	Hosts         []HostEntry
	HubSpoke      HubSpokeConfig
	Measurements  []MeasurementSpec
	HideSkipped   bool
	LocalStore    LocalStoreConfig
}

// MeasurementSpec describes one scheduled measurement type.
type MeasurementSpec struct {
	Type           string        // "twamp", "bw", "trace", "mtu", "dns"
	Interval       time.Duration
	Targets        string        // "all" or "hub-only"
	MeasurerConfig MeasurerConfig
}

// LocalStoreConfig controls the optional local JSONL result store.
type LocalStoreConfig struct {
	Enabled  bool
	Path     string
	MaxLines int
}

type HostEntry struct {
	Name    string
	Address string
	Site    string
}

type HubSpokeConfig struct {
	Enabled bool
	Hub     string
}

type InfluxConfig struct {
	URL    string
	Token  string
	Org    string
	Bucket string
}

// ProbeResult carries aggregated statistics for one burst to one target.
type ProbeResult struct {
	Source    string
	Target    string
	Site      string
	Topology  string
	RttMin    time.Duration
	RttAvg    time.Duration
	RttMax    time.Duration
	RttStddev time.Duration
	Jitter    time.Duration
	LossPct   float64
	SentAt    time.Time
	Sent      int
	Recv      int
}

// rawAgentConfig mirrors AgentConfig with string durations for JSON decoding.
type rawAgentConfig struct {
	Topology      string               `json:"topology"`
	ProbeInterval string               `json:"probe_interval"`
	BurstSize     int                  `json:"burst_size"`
	BurstInterval string               `json:"burst_interval"`
	PacketTimeout string               `json:"packet_timeout"`
	Padding       int                  `json:"padding"`
	ConfigRefresh string               `json:"config_refresh"`
	InfluxDB      rawInfluxConfig      `json:"influxdb"`
	Hosts         []rawHostEntry       `json:"hosts"`
	HubSpoke      rawHubSpoke          `json:"hub_spoke"`
	Measurements  []rawMeasurementSpec `json:"measurements"`
	HideSkipped   bool                 `json:"hide_skipped"`
	LocalStore    rawLocalStoreConfig  `json:"local_store"`
}

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

type rawInfluxConfig struct {
	URL    string `json:"url"`
	Token  string `json:"token"`
	Org    string `json:"org"`
	Bucket string `json:"bucket"`
}

type rawHostEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Site    string `json:"site"`
}

type rawHubSpoke struct {
	Enabled bool   `json:"enabled"`
	Hub     string `json:"hub"`
}

func parseAgentConfig(data []byte) (AgentConfig, error) {
	var raw rawAgentConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentConfig{}, fmt.Errorf("JSON parse error: %w", err)
	}
	if raw.Topology == "" {
		return AgentConfig{}, fmt.Errorf("config missing required field: topology")
	}

	parseDur := func(s, name, def string) (time.Duration, error) {
		if s == "" {
			s = def
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration for %s %q: %w", name, s, err)
		}
		return d, nil
	}

	probeInterval, err := parseDur(raw.ProbeInterval, "probe_interval", "60s")
	if err != nil {
		return AgentConfig{}, err
	}
	burstInterval, err := parseDur(raw.BurstInterval, "burst_interval", "200ms")
	if err != nil {
		return AgentConfig{}, err
	}
	packetTimeout, err := parseDur(raw.PacketTimeout, "packet_timeout", "5s")
	if err != nil {
		return AgentConfig{}, err
	}
	configRefresh, err := parseDur(raw.ConfigRefresh, "config_refresh", "5m")
	if err != nil {
		return AgentConfig{}, err
	}

	burstSize := raw.BurstSize
	if burstSize <= 0 {
		burstSize = 5
	}

	if raw.Padding < 0 {
		return AgentConfig{}, fmt.Errorf("padding must be >= 0, got %d", raw.Padding)
	}

	hosts := make([]HostEntry, len(raw.Hosts))
	for i, h := range raw.Hosts {
		hosts[i] = HostEntry{Name: h.Name, Address: h.Address, Site: h.Site}
	}

	var specs []MeasurementSpec
	for _, rm := range raw.Measurements {
		interval, err := parseDur(rm.Interval, "measurements[].interval", "60s")
		if err != nil {
			return AgentConfig{}, err
		}
		burstIntervalSpec, err := parseDur(rm.BurstInterval, "burst_interval", "200ms")
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
		rmBurstSize := rm.BurstSize
		if rmBurstSize <= 0 {
			rmBurstSize = 5
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
		// For TWAMP, packet_timeout is the canonical timeout field
		timeout := mTimeout
		if rm.Type == "twamp" && pktTimeout > 0 {
			timeout = pktTimeout
		}
		specs = append(specs, MeasurementSpec{
			Type:     rm.Type,
			Interval: interval,
			Targets:  rm.Targets,
			MeasurerConfig: MeasurerConfig{
				BurstSize:     rmBurstSize,
				BurstInterval: burstIntervalSpec,
				Timeout:       timeout,
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

	return AgentConfig{
		Topology:      raw.Topology,
		ProbeInterval: probeInterval,
		BurstSize:     burstSize,
		BurstInterval: burstInterval,
		PacketTimeout: packetTimeout,
		Padding:       raw.Padding,
		ConfigRefresh: configRefresh,
		InfluxDB: InfluxConfig{
			URL:    raw.InfluxDB.URL,
			Token:  raw.InfluxDB.Token,
			Org:    raw.InfluxDB.Org,
			Bucket: raw.InfluxDB.Bucket,
		},
		Hosts: hosts,
		HubSpoke: HubSpokeConfig{
			Enabled: raw.HubSpoke.Enabled,
			Hub:     raw.HubSpoke.Hub,
		},
		Measurements: specs,
		HideSkipped:  raw.HideSkipped,
		LocalStore: LocalStoreConfig{
			Enabled:  raw.LocalStore.Enabled,
			Path:     raw.LocalStore.Path,
			MaxLines: raw.LocalStore.MaxLines,
		},
	}, nil
}

// targetsFor returns the list of hosts this agent should probe given its hostname.
// Returns empty slice if hostname not found in Hosts (agent acts as reflector only).
func (c AgentConfig) targetsFor(hostname string) []HostEntry {
	found := false
	for _, h := range c.Hosts {
		if h.Name == hostname {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	if c.HubSpoke.Enabled {
		if hostname == c.HubSpoke.Hub {
			// Hub probes all spokes
			var spokes []HostEntry
			for _, h := range c.Hosts {
				if h.Name != c.HubSpoke.Hub {
					spokes = append(spokes, h)
				}
			}
			return spokes
		}
		// Spoke probes hub only
		for _, h := range c.Hosts {
			if h.Name == c.HubSpoke.Hub {
				return []HostEntry{h}
			}
		}
		return nil
	}

	// Mesh: probe everyone except self
	var targets []HostEntry
	for _, h := range c.Hosts {
		if h.Name != hostname {
			targets = append(targets, h)
		}
	}
	return targets
}

var configHTTPClient = &http.Client{Timeout: 15 * time.Second}

// fetchConfig fetches and parses the topology JSON from url.
func fetchConfig(url string) (AgentConfig, error) {
	resp, err := configHTTPClient.Get(url) //nolint:gosec // URL is operator-supplied config
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fetch config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AgentConfig{}, fmt.Errorf("config server returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return AgentConfig{}, fmt.Errorf("read config body: %w", err)
	}
	return parseAgentConfig(data)
}

// runConfigPoller fetches config at startup (sends on out), then re-fetches
// every refresh interval. On refresh failure it logs and continues with the
// last-known config. Exits when ctx is cancelled.
func runConfigPoller(ctx context.Context, url string, refresh time.Duration, out chan<- AgentConfig, logger *log.Logger) {
	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cfg, err := fetchConfig(url)
			if err != nil {
				logger.Printf("[Agent] config refresh failed: %v (using last-known config)", err)
				continue
			}
			select {
			case out <- cfg:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// runBurst fires cfg.BurstSize TWAMP-Light packets at target and returns
// aggregated statistics as a ProbeResult.
func runBurst(target HostEntry, cfg AgentConfig, hostname string, port int, synced bool, logFile *os.File) ProbeResult {
	result := ProbeResult{
		Source:   hostname,
		Target:   target.Name,
		Site:     target.Site,
		Topology: cfg.Topology,
		Sent:     cfg.BurstSize,
		SentAt:   time.Now(),
	}

	c := NewClient(
		target.Address,
		logFile,
		cfg.BurstSize,
		cfg.BurstInterval,
		cfg.PacketTimeout,
		port,
		cfg.Padding,
		synced,
	)

	rtts, recv := c.runBurst()
	result.Recv = recv

	if recv == 0 {
		result.LossPct = 100.0
		return result
	}

	// Compute statistics
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
	avgF := float64(avg)
	for _, r := range rtts {
		d := float64(r) - avgF
		variance += d * d
	}
	stddev := time.Duration(math.Sqrt(variance / float64(recv)))

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

	result.RttMin = minR
	result.RttAvg = avg
	result.RttMax = maxR
	result.RttStddev = stddev
	result.Jitter = jitter
	result.LossPct = float64(cfg.BurstSize-recv) / float64(cfg.BurstSize) * 100.0
	return result
}

// runAgent starts the four concurrent goroutines that make up agent mode.
// It blocks until SIGINT/SIGTERM is received.
func runAgent(port int, configURL, hostname string, configRefresh time.Duration, synced bool, logFile *os.File) {
	out := io.Writer(os.Stdout)
	if logFile != nil {
		out = logFile
	}
	logger := log.New(out, "[TWAMP-Light-Agent] ", log.LstdFlags|log.Lmicroseconds)

	// Fetch initial config — fatal on failure
	logger.Printf("Fetching config from %s", configURL)
	initialCfg, err := fetchConfig(configURL)
	if err != nil {
		logger.Fatalf("Cannot fetch initial config: %v", err)
	}
	logger.Printf("Config loaded: topology=%s hosts=%d probe_interval=%v",
		initialCfg.Topology, len(initialCfg.Hosts), initialCfg.ProbeInterval)

	// Resolve hostname
	if hostname == "" {
		h, err := os.Hostname()
		if err != nil {
			logger.Fatalf("Cannot determine hostname: %v", err)
		}
		hostname = h
	}

	// Use config_refresh from fetched config if caller didn't override
	if configRefresh == 0 {
		configRefresh = initialCfg.ConfigRefresh
	}

	// Open local store if configured
	var localStore *LocalStore
	if initialCfg.LocalStore.Enabled && initialCfg.LocalStore.Path != "" {
		maxLines := initialCfg.LocalStore.MaxLines
		if maxLines <= 0 {
			maxLines = 10000
		}
		ls, err := NewLocalStore(initialCfg.LocalStore.Path, maxLines)
		if err != nil {
			logger.Printf("[Agent] local store disabled: %v", err)
		} else {
			localStore = ls
			defer localStore.Close()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wire channels
	configCh := make(chan AgentConfig, 1)
	resultsCh := make(chan MeasureResult, 200)

	// Seed the config channel with initial config so scheduler starts immediately
	configCh <- initialCfg

	var wg sync.WaitGroup

	// Goroutine 1: Config poller (sends subsequent configs)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runConfigPoller(ctx, configURL, configRefresh, configCh, logger)
	}()

	// Goroutine 2: TWAMP-Light reflector server
	wg.Add(1)
	go func() {
		defer wg.Done()
		al, _ := parseAllowlist("") // agent mode: accept from all peers
		rl := newRateLimiter(0)     // agent mode: no rate limiting (trusted peers)
		srv := NewServer(logFile, rl, al, synced)
		if err := srv.Start(port); err != nil {
			logger.Printf("Server error: %v", err)
		}
	}()

	// Goroutine: BwServer (TCP sink for native bandwidth measurements)
	wg.Add(1)
	go func() {
		defer wg.Done()
		bwSrv := &BwServer{}
		port, err := bwSrv.Start(5201)
		if err != nil {
			logger.Printf("[Agent] BwServer failed to start: %v", err)
			return
		}
		logger.Printf("[Agent] BwServer listening on :%d", port)
		<-ctx.Done()
		bwSrv.Stop()
	}()

	// Goroutine 3: Per-measurer schedulers (one goroutine per MeasurementSpec)
	// Each scheduler gets its own channel so config updates are broadcast to all.
	measurers := buildMeasurers(hostname, port, synced, logFile)
	schedulerChans := make([]chan AgentConfig, 0, len(initialCfg.Measurements))
	for _, spec := range initialCfg.Measurements {
		spec := spec // capture
		m, ok := measurers[spec.Type]
		if !ok {
			logger.Printf("[Agent] unknown measurement type %q — skipping", spec.Type)
			continue
		}
		ch := make(chan AgentConfig, 1)
		ch <- initialCfg // seed so scheduler starts immediately
		schedulerChans = append(schedulerChans, ch)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runMeasurerScheduler(ctx, m, spec, ch, resultsCh, hostname, logger, initialCfg.HideSkipped, localStore)
		}()
	}

	// Fan-out goroutine: broadcast each config update from configCh to all scheduler channels.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case cfg, ok := <-configCh:
				if !ok {
					return
				}
				for _, ch := range schedulerChans {
					select {
					case ch <- cfg:
					case <-ctx.Done():
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Goroutine 4: InfluxDB writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		w := newInfluxWriter(initialCfg.InfluxDB, logger)
		w.runResults(ctx, resultsCh)
	}()

	// Wait for shutdown signal then cancel context
	platformWaitForShutdown(cancel, logger)
	wg.Wait()
}

// buildMeasurers constructs the full registry of supported measurer types.
func buildMeasurers(hostname string, port int, synced bool, logFile *os.File) map[string]Measurer {
	return map[string]Measurer{
		"twamp": &TwampMeasurer{hostname: hostname, port: port, logFile: logFile},
		"bw":    &BwMeasurer{hostname: hostname},
		"trace": &TraceMeasurer{hostname: hostname},
		"mtu":   &MtuMeasurer{hostname: hostname},
		"dns":   &DnsMeasurer{hostname: hostname},
	}
}

// runMeasurerScheduler drives a single Measurer on its own ticker.
// It listens for updated AgentConfigs on configs and forwards MeasureResults to results.
func runMeasurerScheduler(ctx context.Context, m Measurer, spec MeasurementSpec, configs <-chan AgentConfig, results chan<- MeasureResult, hostname string, logger *log.Logger, hideSkipped bool, localStore *LocalStore) {
	ticker := time.NewTicker(spec.Interval)
	defer ticker.Stop()
	var cfg AgentConfig

	for {
		select {
		case newCfg := <-configs:
			cfg = newCfg
		case <-ticker.C:
			targets := resolveTargets(cfg, hostname, spec.Targets, m.Name())
			mcfg := spec.MeasurerConfig
			mcfg.Synced = true // always use agent's synced flag
			for _, target := range targets {
				rs, err := m.Run(ctx, target, mcfg)
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
					if localStore != nil {
						if err := localStore.Append(r); err != nil {
							logger.Printf("[Agent] local store append error: %v", err)
						}
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// resolveTargets returns the list of HostEntry values the measurer should probe.
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

// runProbeScheduler drives periodic probe bursts to all configured targets.
// When a new AgentConfig arrives on configs it drains in-flight work and
// restarts with the new target list. Exits when ctx is cancelled.
func runProbeScheduler(ctx context.Context, configs <-chan AgentConfig, results chan<- ProbeResult, hostname string, port int, synced bool, logFile *os.File, logger *log.Logger) {
	var cfg AgentConfig
	var ticker *time.Ticker

	// Wait for first config before starting ticker
	select {
	case cfg = <-configs:
	case <-ctx.Done():
		return
	}

	ticker = time.NewTicker(cfg.ProbeInterval)
	defer ticker.Stop()

	probe := func() {
		targets := cfg.targetsFor(hostname)
		if len(targets) == 0 {
			logger.Printf("[Agent] %s not in hosts list or no targets — acting as reflector only", hostname)
			return
		}
		for _, target := range targets {
			select {
			case <-ctx.Done():
				return
			default:
			}
			r := runBurst(target, cfg, hostname, port, synced, logFile)
			select {
			case results <- r:
			case <-ctx.Done():
				return
			}
		}
	}

	for {
		select {
		case newCfg := <-configs:
			cfg = newCfg
			ticker.Reset(cfg.ProbeInterval)
			logger.Printf("[Agent] config updated, probing %d hosts every %v", len(cfg.Hosts), cfg.ProbeInterval)
		case <-ticker.C:
			probe()
		case <-ctx.Done():
			return
		}
	}
}
