// Command drem-classifier is the warm direct-classifier HTTP server that
// orch POSTs into when [agents.classifier].endpoint is configured. See
// plans/warm-direct-classifier.md for the full design; the HTTP surface is
// documented in that plan's §4.
//
// Flow:
//
//  1. orch issues POST /classify with {task_id, title, description, context}.
//  2. the handler invokes agent.Classify against the upstream gq/sglang URL
//     configured at startup.
//  3. the parsed classification decision is returned to orch in the response
//     body; orch owns the DB write and the status transition.
//
// Configuration is environment-driven so the container compose file (see
// deploy/compose/global.yml) is the single source of truth. Secrets never
// land on the command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

// defaultListenAddr is the port the container template advertises. Kept in a
// const so the compose file and the binary can't silently disagree.
const defaultListenAddr = ":8090"

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "drem-classifier: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	listenAddr string
	endpoint   string
	model      string
	timeout    time.Duration
	token      string
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("drem-classifier", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cfg config
	fs.StringVar(&cfg.listenAddr, "listen", envOr("DREM_CLASSIFIER_LISTEN", defaultListenAddr), "HTTP listen address")
	fs.StringVar(&cfg.endpoint, "endpoint", envOr("DREM_CLASSIFIER_UPSTREAM", "http://gq:8090/v1/chat/completions"), "Upstream OpenAI-compatible chat completions URL (gq proxy)")
	fs.StringVar(&cfg.model, "model", envOr("DREM_CLASSIFIER_MODEL", "gemma4-26b"), "Model name forwarded to the upstream")
	fs.DurationVar(&cfg.timeout, "timeout", parseDurationOr("DREM_CLASSIFIER_TIMEOUT", 60*time.Second), "Upstream HTTP request timeout")
	// Token comes from env only — avoids leaking into `ps` output.
	cfg.token = os.Getenv("DREM_AGENTMON_TOKEN")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.endpoint == "" {
		return config{}, errors.New("--endpoint or DREM_CLASSIFIER_UPSTREAM is required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDurationOr(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

// run does the actual wiring. Split out of main so a future integration test
// can drive the binary without calling os.Exit.
func run(args []string, stderr io.Writer) error {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	classifierCfg := agent.DirectClassifierConfig{
		Endpoint:    cfg.endpoint,
		Model:       cfg.model,
		MaxTokens:   1024,
		Temperature: 0.1,
		Timeout:     cfg.timeout,
	}

	deps := Deps{
		Classify:      agent.Classify,
		ProbeUpstream: probeUpstream(cfg.endpoint, 5*time.Second),
	}

	srv := NewServer(classifierCfg, cfg.token, deps, logger)
	httpServer := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("drem-classifier listening",
			"addr", cfg.listenAddr,
			"upstream", cfg.endpoint,
			"model", cfg.model,
			"auth_enabled", cfg.token != "",
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("drem-classifier: shutdown requested")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// probeUpstream returns a ProbeUpstream function that GETs the /v1/models
// (or parent) endpoint of the configured upstream. The gq proxy serves
// /v1/models, so a 200 there proves both gq and sglang are up.
func probeUpstream(endpoint string, timeout time.Duration) func(context.Context) error {
	probeURL := deriveProbeURL(endpoint)
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context) error {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, probeURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("upstream %s returned %d", probeURL, resp.StatusCode)
		}
		return nil
	}
}

// deriveProbeURL strips /v1/chat/completions off an OpenAI-compatible URL
// and appends /v1/models. Falls back to the configured endpoint verbatim
// when parsing fails — any 2xx/4xx is considered alive.
func deriveProbeURL(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	// Chop a trailing /v1/chat/completions to /v1/models; else leave as-is.
	path := strings.TrimSuffix(u.Path, "/v1/chat/completions")
	if path != u.Path {
		u.Path = path + "/v1/models"
	}
	return u.String()
}
