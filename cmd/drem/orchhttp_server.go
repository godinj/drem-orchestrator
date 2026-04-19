package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/orchhttp"
)

// startOrchHTTP launches the orchestrator's public HTTP API on
// cfg.OrchHTTPPort in a background goroutine cancellable via ctx. It is
// a no-op when the port is empty, so pre-containerization dev setups do
// not have to configure one.
//
// Kyle and the TUI both consume this API (see
// docs/prd-containerization.md "Orchestrator is the single state surface
// per project"). The listener is read-only for external callers; the
// only write surface is POST /internal/logs, which is gated by
// cfg.AgentmonToken.
//
// Returned is a stop function that callers should defer to drain
// in-flight requests on shutdown. When the port is empty the returned
// stop is a no-op.
func startOrchHTTP(ctx context.Context, cfg Config, db *gorm.DB, project string) func(context.Context) error {
	if cfg.OrchHTTPPort == "" {
		return func(context.Context) error { return nil }
	}
	srv := orchhttp.New(db, cfg.AgentmonToken, nil, orchhttp.ProjectInfo{
		Name:     project,
		Language: cfg.ProjectLanguage,
		OrchURL:  "http://localhost:" + cfg.OrchHTTPPort,
	})
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
