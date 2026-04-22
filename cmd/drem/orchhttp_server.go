package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/orchhttp"
)

// effectiveAgentmonToken resolves the per-project shared secret used
// by orchhttp.requireAgentmonToken. Precedence is (1) the drem.toml
// [agentmon_token] field, (2) the DREM_AGENTMON_TOKEN env var. Empty
// return means ingestion fails closed — orchhttp rejects every POST
// to /internal/logs with 401.
//
// The env-var fallback exists because the per-project compose
// template populates DREM_AGENTMON_TOKEN on the orch container env
// block but does not currently write the same key into the mounted
// drem.toml (see internal/projects/templates/project-compose.yml.tmpl
// and plans/agentmon-auth-401-fix.md). Without this fallback the
// orch container has SharedToken="" on startup, rejects every
// agentmon POST with 401, and heartbeats never reach the database —
// which in turn causes the stuck-agent reconciler to declare live
// workers dead and fail tasks with spurious "agent session died
// without producing commits" errors. 41-hour production outage
// April 2026, task a23ebaa2-157b-492b-83c1-2a199490268c.
func effectiveAgentmonToken(cfg Config) string {
	if cfg.AgentmonToken != "" {
		return cfg.AgentmonToken
	}
	return os.Getenv("DREM_AGENTMON_TOKEN")
}

// gateOrch is the minimum surface startOrchHTTP needs to wire the gate
// mutation endpoints. *orchestrator.Orchestrator satisfies it; accepting an
// interface here keeps this file decoupled from the concrete type.
type gateOrch = orchhttp.GateOrchestrator

// startOrchHTTP launches the orchestrator's public HTTP API on
// cfg.OrchHTTPPort in a background goroutine cancellable via ctx. It is
// a no-op when the port is empty, so pre-containerization dev setups do
// not have to configure one.
//
// Kyle and the TUI both consume this API (see
// docs/prd-containerization.md "Orchestrator is the single state surface
// per project"). The listener is read-only for external callers; the
// only write surface is POST /internal/logs, which is gated by the
// token returned from effectiveAgentmonToken(cfg).
//
// Returned is a stop function that callers should defer to drain
// in-flight requests on shutdown. When the port is empty the returned
// stop is a no-op.
func startOrchHTTP(ctx context.Context, cfg Config, db *gorm.DB, project string, orch gateOrch) func(context.Context) error {
	if cfg.OrchHTTPPort == "" {
		return func(context.Context) error { return nil }
	}
	token := effectiveAgentmonToken(cfg)
	if token == "" {
		slog.Warn("orchhttp: agentmon token not configured; /internal/logs will reject all requests with 401",
			"project", project,
			"hint", "set DREM_AGENTMON_TOKEN on the orch container env or agentmon_token in drem.toml",
		)
	}
	srv := orchhttp.New(db, token, nil, orchhttp.ProjectInfo{
		Name:     project,
		Language: cfg.ProjectLanguage,
		OrchURL:  "http://localhost:" + cfg.OrchHTTPPort,
	})
	// orch is the single in-process orchestrator instance. Wiring it here
	// makes the gate mutation endpoints (POST /approve, /reject, /pass,
	// /fail, /answer) delegate to the same writer that services the
	// reconciler — no second orchestrator, no SQLite write contention.
	srv.Orch = orch
	httpSrv := &http.Server{
		Addr:              ":" + cfg.OrchHTTPPort,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("orchestrator HTTP API listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("orchestrator HTTP API failed", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	return httpSrv.Shutdown
}
