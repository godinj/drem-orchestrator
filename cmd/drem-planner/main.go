// Command drem-planner is the warm HTTP server that orch POSTs into when
// a task reaches PLANNING. See
// plans/warm-planner-pivot.md for the full design; the HTTP surface is
// documented in that plan's §5.
//
// Flow:
//
//  1. Orch issues POST /plan with {task_id, task, project, worktree_path, ...}.
//  2. The handler invokes Codex in headless mode via the operator's
//     bind-mounted Codex auth file.
//  3. The resulting plan.json is returned inline in the response body;
//     orch owns the DB write.
//
// The container validates ${HOME}/.codex/auth.json at boot and
// exits 1 loud if missing, so a misconfigured host crash-loops in
// `docker ps` instead of hanging on an auth failure.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const defaultListenAddr = ":8090"

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "drem-planner: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	listenAddr      string
	token           string
	credentialsPath string
	claudeTimeout   time.Duration
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("drem-planner", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cfg config
	fs.StringVar(&cfg.listenAddr, "listen", envOr("DREM_PLANNER_LISTEN", defaultListenAddr), "HTTP listen address")
	fs.StringVar(&cfg.credentialsPath, "credentials", envOr("DREM_PLANNER_CREDENTIALS", defaultCredentialsPath()), "Path to Codex auth file")
	fs.DurationVar(&cfg.claudeTimeout, "claude-timeout", parseDurationOr("DREM_PLANNER_CLAUDE_TIMEOUT", 5*time.Minute), "Max wall-clock time for a single Codex invocation")
	// Token comes from env only — avoids leaking into `ps` output.
	cfg.token = os.Getenv("DREM_AGENTMON_TOKEN")

	if err := fs.Parse(args); err != nil {
		return config{}, err
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

// run does the actual wiring. Split out of main so an integration test can
// drive the binary without calling os.Exit.
func run(args []string, stderr io.Writer) error {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Boot-time credentials validation.
	// Fail loud on startup when the bind-mount is missing — compose's
	// restart: unless-stopped then crash-loops the container visibly in
	// `docker ps` until the operator runs `codex login` on the host.
	if err := credentialsProbe(context.Background(), cfg.credentialsPath); err != nil {
		return fmt.Errorf("credentials validation: %w (run `codex login` on host, then retry)", err)
	}
	logger.Info("drem-planner: credentials validated", "path", cfg.credentialsPath)

	deps := Deps{
		// Real Codex subprocess generator. The invoker execs Codex per
		// request and reads the bind-mounted host auth file.
		GeneratePlan: newClaudePlanGen(runRealClaude, cfg.claudeTimeout, DefaultPlannerModel),
		ProbeHealth: func(ctx context.Context) error {
			// /healthz validates the credentials file AND that the Codex
			// binary is in PATH with a working --version. The second check
			// bounds to 2s so a hung install doesn't block orch dispatch.
			if err := credentialsProbe(ctx, cfg.credentialsPath); err != nil {
				return err
			}
			return codexVersionProbe(ctx, 2*time.Second)
		},
	}

	srv := NewServer(cfg.token, deps, logger)
	httpServer := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("drem-planner listening",
			"addr", cfg.listenAddr,
			"credentials", cfg.credentialsPath,
			"auth_enabled", cfg.token != "",
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("drem-planner: shutdown requested")
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
