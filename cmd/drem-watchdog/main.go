// Command drem-watchdog is the crash-recovery daemon baked into every
// Drem Orchestrator worker container image.
//
// It commits any in-progress changes to the container-local working copy
// and pushes the branch to the bare repository every --interval, and
// additionally whenever the configured --test-cmd exits with status 0.
// A heartbeat line is written to stdout each commit tick so that
// agentmon's log-extraction pipeline can attribute liveness to this
// container's agent.
//
// The watchdog exits cleanly on SIGTERM or SIGINT, performing a single
// best-effort final commit-and-push attempt before returning.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/godinj/drem-orchestrator/internal/watchdog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "drem-watchdog: %v\n", err)
		os.Exit(1)
	}
}

// run parses flags, builds the watchdog loop, wires signal handling, and
// blocks until the context is cancelled. It is separated from main so a
// future integration test can drive it without calling os.Exit.
func run(args []string) error {
	fs := flag.NewFlagSet("drem-watchdog", flag.ContinueOnError)
	var (
		repo         = fs.String("repo", "", "path to the container-local working copy (required)")
		branch       = fs.String("branch", "", "branch to push to (required)")
		remote       = fs.String("remote", "origin", "push target; the bare repo is added as this remote by the worker at startup")
		interval     = fs.Duration("interval", 60*time.Second, "periodic commit cadence when the tree is dirty")
		testCmd      = fs.String("test-cmd", "", "optional test command; on exit 0 the watchdog commits and pushes immediately")
		testInterval = fs.Duration("test-interval", 5*time.Minute, "cadence for --test-cmd; ignored when --test-cmd is empty")
		agentID      = fs.String("agent-id", "", "agent identifier emitted on heartbeat markers (required)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *branch == "" || *agentID == "" {
		fs.Usage()
		return fmt.Errorf("--repo, --branch, and --agent-id are required")
	}

	loop := &watchdog.Loop{
		Repo:         *repo,
		Branch:       *branch,
		Remote:       *remote,
		Interval:     *interval,
		TestCmd:      *testCmd,
		TestInterval: *testInterval,
		AgentID:      *agentID,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	return loop.Run(ctx)
}
