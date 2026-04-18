// Command gq runs the gemma queue proxy — an admission-control and scheduling
// proxy between LLM callers and a single SGLang endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/godinj/drem-orchestrator/internal/gq"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, logOut *os.File) int {
	fs := flag.NewFlagSet("gq", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to gq.toml (default: ~/.drem-csuite/gq.toml)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(logOut, "error: %v\n", err)
		return 1
	}

	// Default config path.
	cfgPath := *configPath
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = home + "/.drem-csuite/gq.toml"
	}

	cfg, err := gq.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(logOut, "config error: %v\n", err)
		return 1
	}

	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Wire components.
	scheduler := gq.NewScheduler(cfg)
	breaker := gq.NewBreaker(cfg.UpstreamBreaker)
	stats := gq.NewStats(cfg.Stats.Window.Duration)
	handler := gq.NewHandler(scheduler, breaker, stats, cfg, log)
	dispatcher := gq.NewDispatcher(scheduler, breaker, stats, cfg, log)
	statsHandler := gq.NewStatsHandler(stats, scheduler, breaker)

	// Proxy server.
	proxySrv := &http.Server{
		Addr:    cfg.BindAddr,
		Handler: handler,
	}

	// Metrics server.
	metricsSrv := &http.Server{
		Addr:    cfg.MetricsAddr,
		Handler: statsHandler.BuildMetricsMux(),
	}

	// Shutdown context.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Start dispatcher.
	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	go dispatcher.Run(dispatchCtx)

	// Start servers.
	go func() {
		log.Info("proxy listening", "addr", cfg.BindAddr)
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("proxy server error", "err", err)
		}
	}()
	go func() {
		log.Info("metrics listening", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server error", "err", err)
		}
	}()

	log.Info("gq started",
		"upstream", cfg.Upstream,
		"max_slots", cfg.MaxSlots,
		"queue_max_depth", cfg.QueueMaxDepth,
	)

	// Wait for shutdown signal.
	<-ctx.Done()
	log.Info("shutting down")

	// Stop accepting new requests.
	dispatchCancel()

	// Drain with timeout.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.DrainTimeout.Duration)
	defer drainCancel()

	var shutdownErr error
	if err := proxySrv.Shutdown(drainCtx); err != nil {
		shutdownErr = err
	}
	if err := metricsSrv.Shutdown(drainCtx); err != nil {
		shutdownErr = err
	}

	if shutdownErr != nil {
		log.Error("shutdown error", "err", shutdownErr)
		return 1
	}

	log.Info("shutdown complete")
	return 0
}
