// Command csuite-persona is the inbox-driven headless poller that runs
// inside the four C-Suite persona containers (mike, alex, seth, kyle).
//
// It replaces the prior long-lived interactive model invocation
// (deploy/docker/context/csuite-run.sh) with a polling loop that scans
// the persona's inbox, invokes `opencode run` once per message, and writes
// the reply to the persona's outbox. See internal/csuite/persona for
// the loop semantics and docs/containerization/install.md for the
// operator-facing runbook.
//
// Usage (the Dockerfile CMD uses -persona and leaves every other flag
// at its default, which resolves to paths under /home/drem/.drem-csuite
// and /opt/csuite/prompts):
//
//	csuite-persona -persona seth
//	csuite-persona -persona alex -poll-interval 5s -claude-timeout 10m
//
// # Subscription-only authentication
//
// This binary never reads or sets API keys. The OpenCode Codex-auth plugin
// picks up the bind-mounted /home/drem/.codex/auth.json through
// OPENCODE_MULTI_AUTH_CODEX_AUTH_FILE.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite/persona"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. Parsing + wiring live here so unit
// tests can exercise flag handling without forking os.Exit.
func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("csuite-persona", flag.ContinueOnError)
	fs.SetOutput(stderr)
	startupQuietPeriod, err := envDuration("DREM_CSUITE_STARTUP_QUIET_PERIOD", 0)
	if err != nil {
		fmt.Fprintf(stderr, "csuite-persona: %v\n", err)
		return 2
	}
	startupDrain, err := envBool("DREM_CSUITE_STARTUP_DRAIN", false)
	if err != nil {
		fmt.Fprintf(stderr, "csuite-persona: %v\n", err)
		return 2
	}
	maxMessagesPerScan, err := envInt("DREM_CSUITE_MAX_MESSAGES_PER_SCAN", 0)
	if err != nil {
		fmt.Fprintf(stderr, "csuite-persona: %v\n", err)
		return 2
	}
	maxMessagesAtBoot, err := envInt("DREM_CSUITE_MAX_MESSAGES_AT_BOOT", 0)
	if err != nil {
		fmt.Fprintf(stderr, "csuite-persona: %v\n", err)
		return 2
	}

	personaName := fs.String("persona", "", "Persona name: mike, alex, seth, or kyle (required)")
	inboxDir := fs.String("inbox-dir", "", "Override inbox directory (default /home/drem/.drem-csuite/<persona>/inbox)")
	outboxDir := fs.String("outbox-dir", "", "Override outbox directory (default /home/drem/.drem-csuite/<persona>/outbox)")
	stateFile := fs.String("state-file", "", "Override state file (default /home/drem/.drem-csuite/<persona>/state.md)")
	archiveDir := fs.String("archive-dir", "", "Override archive directory (default <inbox-dir>/.archive)")
	promptFile := fs.String("prompt-file", "", "Override system-prompt file (default /opt/csuite/prompts/<persona>.md)")
	pollInterval := fs.Duration("poll-interval", persona.DefaultPollInterval, "Interval between inbox scans")
	claudeTimeout := fs.Duration("claude-timeout", persona.DefaultClaudeTimeout, "Timeout for a single Codex invocation")
	maxFailures := fs.Int("max-failures", persona.DefaultMaxFailures, "Failure threshold before a message is archived as .failed")
	startupQuiet := fs.Duration("startup-quiet-period", startupQuietPeriod, "Suppress inbox processing for this duration after boot")
	startupDrainFlag := fs.Bool("startup-drain", startupDrain, "Move boot-time inbox messages to .ignored without invoking the model")
	maxPerScan := fs.Int("max-messages-per-scan", maxMessagesPerScan, "Maximum messages processed per normal scan (0 means unlimited)")
	maxAtBoot := fs.Int("max-messages-at-boot", maxMessagesAtBoot, "Maximum messages processed in the post-quiet boot scan (0 skips boot scan)")
	runtimeStateFile := fs.String("runtime-state-file", os.Getenv("DREM_CSUITE_RUNTIME_STATE_FILE"), "Runtime JSON state file (default /home/drem/.drem-csuite/<persona>/runtime.json)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Wire the post-write watcher signaler from environment. Empty
	// CSUITE_SIGNAL_ENDPOINT disables signaling entirely (the
	// returned Signaler is a no-op). See
	// plans/csuite-watcher-outbox-routing.md §1-3.
	endpoint := os.Getenv("CSUITE_SIGNAL_ENDPOINT")
	token := os.Getenv("CSUITE_WATCHER_TOKEN")

	// Scoreboard item 33 (mitigation path 2): if the env var was not
	// passed through from the host shell but a token file was
	// bind-mounted in (CSUITE_WATCHER_TOKEN_FILE), read from the file.
	// This breaks the dependency on the host shell having the env
	// var exported at `docker compose up` time — the file is always
	// accessible from a bind-mount independent of shell state.
	if token == "" {
		if path := os.Getenv("CSUITE_WATCHER_TOKEN_FILE"); path != "" {
			b, rerr := os.ReadFile(path) //nolint:gosec // path is operator-controlled
			if rerr != nil {
				fmt.Fprintf(stderr, "csuite-persona: read CSUITE_WATCHER_TOKEN_FILE=%q: %v\n", path, rerr)
				return 1
			}
			token = strings.TrimSpace(string(b))
		}
	}

	// Scoreboard item 33: fail fast when the signaling endpoint is
	// wired but the token is empty. Pre-fix symptom was a persistent
	// stream of `status=401 body="{"error":"missing X-Csuite-Token
	// header"}"` poller warnings while deliveries silently backed up
	// in the watcher's rescan queue. The valueless-inherit form
	// `CSUITE_WATCHER_TOKEN:` in the compose template depends on the
	// host shell having the env var set at `docker compose up` time;
	// if the operator forgot to export it the persona container boots
	// healthy but can never complete the auth handshake. A startup
	// error surfaces the misconfiguration in `docker logs` immediately
	// rather than burying it in poll-tick-level WARN output. See
	// plans/containerization-pivot-attack-plan.md §3 Group A item 33.
	if endpoint != "" && token == "" {
		fmt.Fprintln(stderr,
			"csuite-persona: CSUITE_SIGNAL_ENDPOINT is set but CSUITE_WATCHER_TOKEN is empty.")
		fmt.Fprintln(stderr,
			"csuite-persona: This means the watcher will reject every signal with 401.")
		fmt.Fprintln(stderr,
			"csuite-persona: Export CSUITE_WATCHER_TOKEN on the host before `docker compose up`:")
		fmt.Fprintln(stderr,
			"csuite-persona:   export CSUITE_WATCHER_TOKEN=$(cat ~/.drem/csuite-watcher.token)")
		fmt.Fprintln(stderr,
			"csuite-persona: Or unset CSUITE_SIGNAL_ENDPOINT to disable signaling entirely.")
		return 1
	}

	signalCfg := persona.SignalConfig{
		Endpoint:      endpoint,
		Token:         token,
		PersonaPrefix: os.Getenv("CSUITE_OUTBOX_PATH_PREFIX"),
		WatcherPrefix: os.Getenv("CSUITE_WATCHER_PATH_PREFIX"),
	}
	signaler := persona.NewHTTPSignaler(signalCfg, logger)

	cfg := persona.Config{
		Persona:            *personaName,
		InboxDir:           *inboxDir,
		OutboxDir:          *outboxDir,
		StateFile:          *stateFile,
		ArchiveDir:         *archiveDir,
		PromptFile:         *promptFile,
		PollInterval:       *pollInterval,
		ClaudeTimeout:      *claudeTimeout,
		MaxFailures:        *maxFailures,
		StartupQuietPeriod: *startupQuiet,
		StartupDrain:       *startupDrainFlag,
		MaxMessagesPerScan: *maxPerScan,
		MaxMessagesAtBoot:  *maxAtBoot,
		RuntimeStateFile:   *runtimeStateFile,
		Logger:             logger,
		Now:                time.Now,
		Signaler:           signaler,
	}
	cfg.ApplyDefaults()

	p, err := persona.New(cfg, persona.NewOpenCodeSpawner())
	if err != nil {
		fmt.Fprintf(stderr, "csuite-persona: %v\n", err)
		return 1
	}

	// Scoreboard item 3: reap orphan `.failures` sidecars whose anchor
	// .md is already in the archive. Runs once at startup; any new
	// orphans created at runtime are caught on the next container
	// restart. See internal/csuite/persona/reaper.go.
	_ = persona.ReapOnceOnStartup(cfg)

	// SIGTERM/SIGINT cancels the context; Run's loop observes ctx.Done
	// at every tick boundary and between messages, so shutdown happens
	// cleanly once the in-flight claude -p invocation returns.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := p.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "csuite-persona: run: %v\n", err)
		return 1
	}
	return 0
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a valid duration: %w", name, value, err)
	}
	return d, nil
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a valid bool: %w", name, value, err)
	}
	return b, nil
}

func envInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a valid integer: %w", name, value, err)
	}
	if n < 0 {
		return 0, errors.New(name + " must be >= 0")
	}
	return n, nil
}
