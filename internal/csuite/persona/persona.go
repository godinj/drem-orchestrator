// Package persona hosts the inbox-driven headless poller used by the
// C-Suite persona containers (mike, alex, seth, kyle).
//
// # Architectural shape
//
// Wave 2 of the csuite-docker pivot replaced the long-lived interactive
// model process that formerly served as the container entrypoint
// (deploy/docker/context/csuite-run.sh) with a polling loop: every tick,
// the loop scans the persona's inbox for new .md messages and invokes
// a non-interactive coding harness once per message, writing the reply into
// the persona's outbox and archiving the processed inbox file. The prior
// design left an interactive model process running with no mechanism to
// observe inbox files — an effective dead-end. See
// docs/containerization/install.md for the operator-facing view and
// plans/csuite-persona-pivot.md for the rationale.
//
// # Subscription-only authentication
//
// The poller never reads or sets API keys. Persona containers use OpenCode
// with the Codex OAuth auth file bind-mounted read-only at
// `/home/drem/.codex/auth.json`; the OpenCode Codex-auth plugin reads that
// file through OPENCODE_MULTI_AUTH_CODEX_AUTH_FILE. Legacy Claude credential
// mounts remain in compose for rollback compatibility only.
//
// Package layout
//
//   - Config     — all poller knobs (paths, poll interval, invocation
//     timeout, failure threshold). Defaults derived from persona name.
//   - Spawner    — the interface the poller uses to launch OpenCode.
//     Production implementation is spawnCLI (subprocess.go); tests
//     provide a fake via SpawnerFunc.
//   - Poller     — holds resolved config + spawner and runs the main
//     loop (Run) until ctx is cancelled. Each tick processes the inbox
//     in deterministic mtime order so message ordering is preserved
//     across restarts.
package persona

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/artifactregistry"
)

// DefaultPollInterval is the spacing between successive inbox scans.
const DefaultPollInterval = 2 * time.Second

// DefaultClaudeTimeout bounds a single model invocation. The name is retained
// for CLI/config compatibility with the earlier Claude-backed poller.
const DefaultClaudeTimeout = 5 * time.Minute

// DefaultMaxFailures is the per-message retry budget before the poller
// gives up, suffixes the file with .failed, and archives it so the loop
// is not stuck on a permanently-bad message.
const DefaultMaxFailures = 3

// AllowedPersonas lists every valid value for Config.Persona. Kyle (the
// CEO persona) runs the same csuite-persona poller as the other three
// but with a per-project :rw bind-mount on the orch-plans tree (the
// compose template enforces that difference, not this package). See
// plans/container-kyle-transition.md.
var AllowedPersonas = []string{"mike", "alex", "seth", "kyle"}

// Config carries every runtime knob for the poller. Zero values mean
// "derive from Persona" where applicable.
type Config struct {
	// Persona selects the prompt file and influences default paths.
	// Must be one of AllowedPersonas. Required.
	Persona string

	// InboxDir is the directory scanned on each tick. Default:
	// /home/drem/.drem-csuite/<persona>/inbox.
	InboxDir string

	// OutboxDir receives the persona's reply files. Default:
	// /home/drem/.drem-csuite/<persona>/outbox.
	OutboxDir string

	// StateFile is a markdown scratchpad updated atomically after each
	// processed message. Default:
	// /home/drem/.drem-csuite/<persona>/state.md.
	StateFile string

	// ArchiveDir receives successfully-processed inbox files (and any
	// message that exhausted its failure budget, suffixed .failed).
	// Default: <InboxDir>/.archive.
	ArchiveDir string

	// PromptFile is the persona system-prompt markdown file mounted into
	// the image. Default: /opt/csuite/prompts/<persona>.md.
	PromptFile string

	// PollInterval governs the tick cadence. Default DefaultPollInterval.
	PollInterval time.Duration

	// ClaudeTimeout bounds a single model invocation. Default
	// DefaultClaudeTimeout.
	ClaudeTimeout time.Duration

	// MaxFailures is the per-message retry budget. Default
	// DefaultMaxFailures.
	MaxFailures int

	// StartupQuietPeriod suppresses inbox processing immediately after boot.
	StartupQuietPeriod time.Duration

	// StartupDrain moves pending boot-time inbox messages to <InboxDir>/.ignored
	// without invoking the model.
	StartupDrain bool

	// MaxMessagesPerScan caps normal scan work when greater than zero.
	MaxMessagesPerScan int

	// MaxMessagesAtBoot caps the one-time post-quiet boot scan when greater
	// than zero. When zero, the boot scan is skipped and normal tick scans
	// handle pending work.
	MaxMessagesAtBoot int

	// RuntimeStateFile receives machine-readable runtime state. Default:
	// /home/drem/.drem-csuite/<persona>/runtime.json.
	RuntimeStateFile string

	// Now returns the current time; override in tests. Default time.Now.
	Now func() time.Time

	// Logger is the structured logger used by the loop. Required — a nil
	// logger is a programmer error. cmd/csuite-persona wires a slog JSON
	// handler to stdout.
	Logger *slog.Logger

	// Signaler posts the post-write HTTP signal to the csuite-watcher
	// after a successful outbox write. Zero value (nil) disables
	// signaling altogether — the outbox file is written and the
	// watcher's rescan path picks it up later. cmd/csuite-persona
	// constructs NewHTTPSignaler from CSUITE_SIGNAL_ENDPOINT +
	// CSUITE_WATCHER_TOKEN env vars. See
	// plans/csuite-watcher-outbox-routing.md §1-3.
	Signaler Signaler

	// Fsyncer is the primitive called on the outbox file after a
	// successful write but before the watcher signal fires. The fsync
	// must happen before the signal so a crash between write and
	// fsync cannot produce a delivery against stale bytes. Default:
	// os.File.Sync via osFsyncer.
	Fsyncer Fsyncer

	// ArtifactAdmissionReporter records report-only context-firewall
	// admission decisions for the assembled persona prompt and active inbox
	// message. Nil preserves the legacy behavior exactly.
	ArtifactAdmissionReporter ArtifactAdmissionReporter
}

type ArtifactAdmissionReporter interface {
	AdmitArtifacts(context.Context, artifactregistry.AdmissionRequest, []artifactregistry.Artifact) (*artifactregistry.AdmissionResult, error)
}

// ApplyDefaults fills zero-value fields using Persona-derived defaults
// and the DefaultPoll*/DefaultClaude*/DefaultMaxFailures constants. It
// does not validate the result — call Validate for that.
func (c *Config) ApplyDefaults() {
	if c.Persona == "" {
		return
	}
	base := filepath.Join("/home/drem/.drem-csuite", c.Persona)
	if c.InboxDir == "" {
		c.InboxDir = filepath.Join(base, "inbox")
	}
	if c.OutboxDir == "" {
		c.OutboxDir = filepath.Join(base, "outbox")
	}
	if c.StateFile == "" {
		c.StateFile = filepath.Join(base, "state.md")
	}
	if c.ArchiveDir == "" {
		c.ArchiveDir = filepath.Join(c.InboxDir, ".archive")
	}
	if c.PromptFile == "" {
		c.PromptFile = filepath.Join("/opt/csuite/prompts", c.Persona+".md")
	}
	if c.RuntimeStateFile == "" {
		c.RuntimeStateFile = filepath.Join(base, "runtime.json")
	}
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.ClaudeTimeout <= 0 {
		c.ClaudeTimeout = DefaultClaudeTimeout
	}
	if c.MaxFailures <= 0 {
		c.MaxFailures = DefaultMaxFailures
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Fsyncer == nil {
		c.Fsyncer = osFsyncer{}
	}
}

// Validate checks that every required path exists and the persona is
// recognised. It is called once at poller startup so a missing bind-mount
// is surfaced as a fail-fast error rather than a silent poll-forever loop.
func (c *Config) Validate() error {
	if c.Persona == "" {
		return errors.New("persona: Persona is required")
	}
	ok := false
	for _, p := range AllowedPersonas {
		if c.Persona == p {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("persona: unknown persona %q (want one of %s)", c.Persona, strings.Join(AllowedPersonas, ","))
	}
	if c.Logger == nil {
		return errors.New("persona: Logger is required")
	}

	for _, dir := range []struct {
		name, path string
	}{
		{"inbox dir", c.InboxDir},
		{"outbox dir", c.OutboxDir},
		{"archive dir", c.ArchiveDir},
	} {
		if err := ensureDir(dir.path); err != nil {
			return fmt.Errorf("persona: %s %q: %w", dir.name, dir.path, err)
		}
	}
	if _, err := os.Stat(c.PromptFile); err != nil {
		return fmt.Errorf("persona: prompt file %q: %w", c.PromptFile, err)
	}
	// StateFile's parent must exist. The file itself is created lazily.
	if err := ensureDir(filepath.Dir(c.StateFile)); err != nil {
		return fmt.Errorf("persona: state dir %q: %w", filepath.Dir(c.StateFile), err)
	}
	if err := ensureDir(filepath.Dir(c.RuntimeStateFile)); err != nil {
		return fmt.Errorf("persona: runtime state dir %q: %w", filepath.Dir(c.RuntimeStateFile), err)
	}
	return nil
}

// ensureDir succeeds if dir exists and is a directory. It deliberately
// does not MkdirAll — a missing inbox usually means the bind-mount is
// wrong, and silently creating one would mask that error.
func ensureDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory")
	}
	return nil
}

// Spawner launches the configured model CLI (or a test double). The contract:
//
//   - args is the full argv, leading element usually "opencode".
//   - stdin supplies the user message (nil if args carries it as a
//     positional argument; see poller.go's OpenCode invocation).
//   - stdout bytes are the reply; exitCode is the process exit status
//     (0 on success). err is non-nil only if the process could not be
//     launched or the context was cancelled.
type Spawner interface {
	Spawn(ctx context.Context, args []string, stdin io.Reader) (stdout []byte, exitCode int, err error)
}

// SpawnerFunc adapts a plain function to the Spawner interface, which
// lets tests inject a table-driven fake without declaring a struct.
type SpawnerFunc func(ctx context.Context, args []string, stdin io.Reader) ([]byte, int, error)

// Spawn implements Spawner.
func (f SpawnerFunc) Spawn(ctx context.Context, args []string, stdin io.Reader) ([]byte, int, error) {
	return f(ctx, args, stdin)
}
