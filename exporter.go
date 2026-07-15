package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// validateTLSFlags returns an error if exactly one of cert/key is non-empty.
func validateTLSFlags(cert, key string) error {
	if (cert == "") != (key == "") {
		return fmt.Errorf("-metrics-tls-cert and -metrics-tls-key must both be set or both be empty")
	}
	return nil
}

// runExporter starts the piccolo-perf Prometheus exporter mode.
// probeMode is one of "background", "scrape", or "dual".
// All configured measurement types (TWAMP, bw, trace, mtu, dns) are exported.
func runExporter(
	port int,
	configURL, hostname string,
	configRefresh time.Duration,
	probeMode, metricsAddr, metricsTLSCert, metricsTLSKey string,
	synced bool,
	logFile *os.File,
) {
	out := io.Writer(os.Stdout)
	if logFile != nil {
		out = logFile
	}
	logger := log.New(out, "[piccolo-perf/exporter] ", log.LstdFlags|log.Lmicroseconds)

	if err := validateTLSFlags(metricsTLSCert, metricsTLSKey); err != nil {
		logger.Fatalf("%v", err)
	}
	if metricsTLSCert != "" {
		if _, err := tls.LoadX509KeyPair(metricsTLSCert, metricsTLSKey); err != nil {
			logger.Fatalf("Cannot load TLS cert/key: %v", err)
		}
	}

	logger.Printf("Fetching config from %s", configURL)
	initialCfg, err := fetchConfig(configURL)
	if err != nil {
		logger.Fatalf("Cannot fetch initial config: %v", err)
	}
	logger.Printf("Config loaded: topology=%s hosts=%d measurements=%d probe_mode=%s",
		initialCfg.Topology, len(initialCfg.Hosts), len(initialCfg.Measurements), probeMode)

	if probeMode == "dual" && initialCfg.InfluxDB.URL == "" {
		logger.Fatalf("probe-mode=dual requires influxdb.url in config")
	}

	if hostname == "" {
		h, err := os.Hostname()
		if err != nil {
			logger.Fatalf("Cannot determine hostname: %v", err)
		}
		hostname = h
	}

	if configRefresh == 0 {
		configRefresh = initialCfg.ConfigRefresh
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newPrometheusStore(hostname)
	measurers := buildMeasurers(hostname, port, synced, logFile)

	var wg sync.WaitGroup

	// TWAMP-Light reflector — increments the reflected-packets counter
	wg.Add(1)
	go func() {
		defer wg.Done()
		al, _ := parseAllowlist("")
		rl := newRateLimiter(0)
		srv := NewServer(logFile, rl, al, synced)
		srv.onReflect = store.IncrementReflected
		if err := srv.Start(port); err != nil {
			logger.Printf("Server error: %v", err)
		}
	}()

	// BwServer — TCP sink for native bandwidth measurements from peers
	wg.Add(1)
	go func() {
		defer wg.Done()
		bwSrv := &BwServer{}
		p, err := bwSrv.Start(5201)
		if err != nil {
			logger.Printf("BwServer failed to start: %v", err)
			return
		}
		logger.Printf("BwServer listening on :%d", p)
		<-ctx.Done()
		bwSrv.Stop()
	}()

	switch probeMode {
	case "background":
		resultsCh := make(chan MeasureResult, 200)
		configCh := make(chan AgentConfig, 1)
		configCh <- initialCfg

		wg.Add(1)
		go func() {
			defer wg.Done()
			runConfigPoller(ctx, configURL, configRefresh, configCh, logger)
		}()

		// Fan-out config updates to one channel per measurer spec
		schedulerChans := make([]chan AgentConfig, len(initialCfg.Measurements))
		for i, spec := range initialCfg.Measurements {
			spec := spec
			ch := make(chan AgentConfig, 1)
			ch <- initialCfg
			schedulerChans[i] = ch
			m, ok := measurers[spec.Type]
			if !ok {
				logger.Printf("unknown measurement type %q — skipping", spec.Type)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				runMeasurerScheduler(ctx, m, spec, ch, resultsCh, hostname, logger, initialCfg.HideSkipped, nil)
			}()
		}

		// Fan-out config refreshes to all scheduler channels
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

		// Drain results into Prometheus store
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case r, ok := <-resultsCh:
					if !ok {
						return
					}
					store.UpdateResult(r)
				case <-ctx.Done():
					return
				}
			}
		}()

	case "scrape":
		var cfgMu sync.RWMutex
		currentCfg := initialCfg

		configCh := make(chan AgentConfig, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runConfigPoller(ctx, configURL, configRefresh, configCh, logger)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case cfg := <-configCh:
					cfgMu.Lock()
					currentCfg = cfg
					cfgMu.Unlock()
				case <-ctx.Done():
					return
				}
			}
		}()

		// On each scrape, run all configured measurers inline before responding
		origHandler := store.Handler()
		var scrapeMu sync.Mutex
		scrapeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scrapeMu.Lock()
			defer scrapeMu.Unlock()
			cfgMu.RLock()
			cfg := currentCfg
			cfgMu.RUnlock()

			for _, spec := range cfg.Measurements {
				m, ok := measurers[spec.Type]
				if !ok {
					continue
				}
				targets := resolveTargets(cfg, hostname, spec.Targets, m.Name())
				mcfg := spec.MeasurerConfig
				mcfg.Synced = synced
				for _, target := range targets {
					results, err := m.Run(r.Context(), target, mcfg)
					if err != nil {
						logger.Printf("scrape %s→%s error: %v", m.Name(), target.Name, err)
						continue
					}
					for _, res := range results {
						if cfg.HideSkipped && res.Tags["skipped"] == "true" {
							continue
						}
						store.UpdateResult(res)
					}
				}
			}
			origHandler.ServeHTTP(w, r)
		})
		store.scrapeHandler = scrapeHandler

	case "dual":
		resultsCh := make(chan MeasureResult, 200)
		influxCh := make(chan MeasureResult, 200)
		configCh := make(chan AgentConfig, 1)
		configCh <- initialCfg

		wg.Add(1)
		go func() {
			defer wg.Done()
			runConfigPoller(ctx, configURL, configRefresh, configCh, logger)
		}()

		schedulerChans := make([]chan AgentConfig, len(initialCfg.Measurements))
		schedulerOut := make(chan MeasureResult, 200)
		for i, spec := range initialCfg.Measurements {
			spec := spec
			ch := make(chan AgentConfig, 1)
			ch <- initialCfg
			schedulerChans[i] = ch
			m, ok := measurers[spec.Type]
			if !ok {
				logger.Printf("unknown measurement type %q — skipping", spec.Type)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				runMeasurerScheduler(ctx, m, spec, ch, schedulerOut, hostname, logger, initialCfg.HideSkipped, nil)
			}()
		}

		// Config fan-out to all scheduler channels
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

		// Fan-out scheduler output to Prometheus and InfluxDB channels
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(resultsCh)
			defer close(influxCh)
			for {
				select {
				case r, ok := <-schedulerOut:
					if !ok {
						return
					}
					select {
					case resultsCh <- r:
					default:
					}
					select {
					case influxCh <- r:
					default:
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		// Prometheus consumer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case r, ok := <-resultsCh:
					if !ok {
						return
					}
					store.UpdateResult(r)
				case <-ctx.Done():
					return
				}
			}
		}()

		// InfluxDB consumer
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := newInfluxWriter(initialCfg.InfluxDB, logger)
			w.runResults(ctx, influxCh)
		}()

	default:
		logger.Fatalf("unknown -probe-mode %q; must be background, scrape, or dual", probeMode)
	}

	mux := http.NewServeMux()
	if store.scrapeHandler != nil {
		mux.Handle("/metrics", store.scrapeHandler)
	} else {
		mux.Handle("/metrics", store.Handler())
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><a href="/metrics">Metrics</a></body></html>`)
	})

	srv := &http.Server{
		Addr:              metricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		if metricsTLSCert != "" {
			logger.Printf("Metrics HTTPS server listening on %s", metricsAddr)
			err = srv.ListenAndServeTLS(metricsTLSCert, metricsTLSKey)
		} else {
			logger.Printf("Metrics HTTP server listening on %s", metricsAddr)
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			logger.Printf("Metrics server error: %v", err)
		}
	}()

	platformWaitForShutdown(cancel, logger)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx) //nolint:errcheck
	wg.Wait()
}
