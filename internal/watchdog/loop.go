package watchdog

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// HeartbeatPrefix is the fixed stdout marker emitted every interval tick
// so that agentmon can attribute the watchdog's liveness signal to the
// correct agent. It must match the constant parsed by internal/extract.
//
// TODO: once internal/extract exports this value (owned by prompt 02),
// import it from there instead of duplicating it here.
const HeartbeatPrefix = "DREM-HEARTBEAT"

// Loop is the watchdog's core control loop. It is decoupled from flag
// parsing and process management so that tests can drive it directly.
//
// Fields are populated by cmd/drem-watchdog/main.go (or a test). All
// fields are required except TestCmd, which disables the test-driven
// commit path when empty, and Clock/Out, which have sensible defaults.
type Loop struct {
	Repo         string
	Branch       string
	Remote       string
	Interval     time.Duration
	TestCmd      string
	TestInterval time.Duration
	AgentID      string

	// Clock is the time source used for heartbeat timestamps and commit
	// messages. Tests override this to produce deterministic output.
	Clock func() time.Time
	// Out receives heartbeat lines. Defaults to os.Stdout.
	Out io.Writer
}

// now returns the current time via the injected clock, falling back to
// time.Now when Clock is nil.
func (l *Loop) now() time.Time {
	if l.Clock == nil {
		return time.Now().UTC()
	}
	return l.Clock()
}

// out returns the configured writer or os.Stdout if none was set.
func (l *Loop) out() io.Writer {
	if l.Out == nil {
		return os.Stdout
	}
	return l.Out
}

// Run drives the commit and test-run tickers until ctx is cancelled.
// On cancellation it performs a single best-effort final commit-and-push
// attempt so that a SIGTERM delivered to the container still captures
// the most recent work.
func (l *Loop) Run(ctx context.Context) error {
	commitTicker := time.NewTicker(l.Interval)
	defer commitTicker.Stop()

	var testCh <-chan time.Time
	if l.TestCmd != "" {
		testTicker := time.NewTicker(l.TestInterval)
		defer testTicker.Stop()
		testCh = testTicker.C
	}

	// Emit an initial heartbeat so consumers see liveness immediately,
	// not only after the first interval elapses.
	l.heartbeat()

	for {
		select {
		case <-ctx.Done():
			l.finalFlush()
			return nil
		case <-commitTicker.C:
			l.heartbeat()
			if err := l.tickCommit(ctx); err != nil {
				fmt.Fprintf(l.out(), "watchdog: commit tick error: %v\n", err)
			}
		case <-testCh:
			if err := l.tickTest(ctx); err != nil {
				fmt.Fprintf(l.out(), "watchdog: test tick error: %v\n", err)
			}
		}
	}
}

// tickCommit checks the working tree for uncommitted changes. If any are
// found it creates a single [watchdog] wip commit and pushes the branch
// to the configured remote. A clean tree is a no-op.
func (l *Loop) tickCommit(ctx context.Context) error {
	dirty, err := isDirty(ctx, l.Repo)
	if err != nil {
		return err
	}
	if !dirty {
		return nil
	}
	msg := fmt.Sprintf("[watchdog] wip %s", l.now().UTC().Format(time.RFC3339))
	committed, err := commitAll(ctx, l.Repo, msg)
	if err != nil {
		return err
	}
	if !committed {
		return nil
	}
	return pushBranch(ctx, l.Repo, l.Remote, l.Branch)
}

// tickTest runs the configured test command. On exit code 0 it performs
// an unconditional commit-and-push so that a passing-tests snapshot is
// always captured, even if the previous commit tick already pushed an
// identical tree. Non-zero exits are ignored (the tree may simply be in
// a transient broken state mid-edit).
func (l *Loop) tickTest(ctx context.Context) error {
	if l.TestCmd == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", l.TestCmd)
	cmd.Dir = l.Repo
	if err := cmd.Run(); err != nil {
		// Non-zero exit is not a watchdog error — tests are allowed to fail.
		return nil
	}
	msg := fmt.Sprintf("[watchdog] tests-passing %s", l.now().UTC().Format(time.RFC3339))
	if _, err := commitAll(ctx, l.Repo, msg); err != nil {
		return err
	}
	return pushBranch(ctx, l.Repo, l.Remote, l.Branch)
}

// heartbeat writes the structured liveness marker consumed by agentmon's
// log extraction. The exact format is documented in internal/extract.
func (l *Loop) heartbeat() {
	fmt.Fprintf(l.out(), "%s agent_id=%s timestamp=%s\n",
		HeartbeatPrefix, l.AgentID, l.now().UTC().Format(time.RFC3339))
}

// finalFlush runs one last commit-and-push attempt on shutdown with a short
// bounded timeout so that a slow remote does not delay container exit. The
// push is unconditional: the harness may already have committed a clean tree
// after the watchdog's last tick, and that commit is still delivery-critical.
// Errors are logged and swallowed — the caller is exiting anyway.
func (l *Loop) finalFlush() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.tickCommit(ctx); err != nil {
		fmt.Fprintf(l.out(), "watchdog: final flush error: %v\n", err)
	}
	if err := pushBranch(ctx, l.Repo, l.Remote, l.Branch); err != nil {
		fmt.Fprintf(l.out(), "watchdog: final push error: %v\n", err)
	}
}
