// Command csuite-persona is the inbox-driven headless poller that runs
// inside the four C-Suite persona containers (mike, alex, ross, seth).
//
// It replaces the prior long-lived interactive claude invocation
// (deploy/docker/context/csuite-run.sh) with a polling loop that scans
// the persona's inbox, invokes `claude -p` once per message, and writes
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
// This binary never reads or sets any Claude auth token. The claude
// CLI transparently picks up credentials from the bind-mounted
// /home/drem/.claude/.credentials.json. See CLAUDE.md "Authentication:
// subscription-only" — CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, and
// ANTHROPIC_AUTH_TOKEN are policy violations and are intentionally not
// surfaced as flags or env vars.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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

	personaName := fs.String("persona", "", "Persona name: mike, alex, ross, or seth (required)")
	inboxDir := fs.String("inbox-dir", "", "Override inbox directory (default /home/drem/.drem-csuite/<persona>/inbox)")
	outboxDir := fs.String("outbox-dir", "", "Override outbox directory (default /home/drem/.drem-csuite/<persona>/outbox)")
	stateFile := fs.String("state-file", "", "Override state file (default /home/drem/.drem-csuite/<persona>/state.md)")
	archiveDir := fs.String("archive-dir", "", "Override archive directory (default <inbox-dir>/.archive)")
	promptFile := fs.String("prompt-file", "", "Override system-prompt file (default /opt/csuite/prompts/<persona>.md)")
	pollInterval := fs.Duration("poll-interval", persona.DefaultPollInterval, "Interval between inbox scans")
	claudeTimeout := fs.Duration("claude-timeout", persona.DefaultClaudeTimeout, "Timeout for a single claude -p invocation")
	maxFailures := fs.Int("max-failures", persona.DefaultMaxFailures, "Failure threshold before a message is archived as .failed")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Wire the post-write watcher signaler from environment. Empty
	// CSUITE_SIGNAL_ENDPOINT disables signaling entirely (the
	// returned Signaler is a no-op). See
	// plans/csuite-watcher-outbox-routing.md §1-3.
	signalCfg := persona.SignalConfig{
		Endpoint:      os.Getenv("CSUITE_SIGNAL_ENDPOINT"),
		Token:         os.Getenv("CSUITE_WATCHER_TOKEN"),
		PersonaPrefix: os.Getenv("CSUITE_OUTBOX_PATH_PREFIX"),
		WatcherPrefix: os.Getenv("CSUITE_WATCHER_PATH_PREFIX"),
	}
	signaler := persona.NewHTTPSignaler(signalCfg, logger)

	cfg := persona.Config{
		Persona:       *personaName,
		InboxDir:      *inboxDir,
		OutboxDir:     *outboxDir,
		StateFile:     *stateFile,
		ArchiveDir:    *archiveDir,
		PromptFile:    *promptFile,
		PollInterval:  *pollInterval,
		ClaudeTimeout: *claudeTimeout,
		MaxFailures:   *maxFailures,
		Logger:        logger,
		Now:           time.Now,
		Signaler:      signaler,
	}
	cfg.ApplyDefaults()

	p, err := persona.New(cfg, persona.NewClaudeSpawner())
	if err != nil {
		fmt.Fprintf(stderr, "csuite-persona: %v\n", err)
		return 1
	}

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
