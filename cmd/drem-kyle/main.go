// Command drem-kyle is the Drem reporting agent. It is the single globally-
// running container that aggregates state across every registered project
// by polling each project's orchestrator HTTP API and exposing a unified
// read-only view on :8090.
//
// See internal/kyle for the implementation and docs/prd-containerization.md
// for the architectural rationale ("C-Suite's reporting surface; read-only;
// no authority over task state").
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/godinj/drem-orchestrator/internal/kyle"
	"github.com/godinj/drem-orchestrator/internal/projects"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses flags, loads the registry, builds the service, and blocks on
// the HTTP server + poll loop. Separated from main so an integration test
// can exercise the binary without calling os.Exit.
func run(args []string) int {
	fs := flag.NewFlagSet("drem-kyle", flag.ContinueOnError)
	var (
		registryPath   = fs.String("registry", "/etc/drem/projects.toml", "path to the host-wide project registry (projects.toml)")
		listen         = fs.String("listen", ":8090", "HTTP listen address")
		pollInterval   = fs.Duration("poll-interval", kyle.DefaultPollInterval, "how often to poll each orchestrator")
		dockerQueryURL = fs.String("docker-query-url", "http://docker-query-proxy:9000", "base URL of the docker-query-proxy sidecar")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "drem-kyle: %v\n", err)
		return 2
	}

	reg, err := projects.Load(*registryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drem-kyle: load registry: %v\n", err)
		return 1
	}

	svc := kyle.New(reg, *dockerQueryURL, *pollInterval)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           svc.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Poll loop runs in its own goroutine; it exits when ctx is cancelled.
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("kyle poll loop exited", "err", err)
		}
	}()

	// HTTP server in another goroutine. ListenAndServe returns
	// http.ErrServerClosed on graceful shutdown; that is not an error.
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("kyle listening", "addr", *listen, "registry", *registryPath, "projects", len(reg.Projects))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	// Block until either a signal fires or ListenAndServe fails.
	select {
	case <-ctx.Done():
		slog.Info("kyle shutting down")
	case err := <-serveErr:
		if err != nil {
			slog.Error("kyle HTTP server exited", "err", err)
			cancel()
			return 1
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("kyle HTTP shutdown error", "err", err)
	}
	<-pollDone
	return 0
}
