// Command csuite-watcher publishes events to the C-Suite event bus and runs
// the watcher main loop that orchestrates agent turn triggers.
//
// Usage:
//
//	csuite-watcher <command> [flags]
//
// Commands:
//
//	event    Publish an event to the event bus from a JSON argument
//	run      Start the watcher main loop
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is the testable entry point. It parses args, dispatches subcommands,
// and returns an exit code. All error output goes to stderr.
func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: csuite-watcher <subcommand> [flags] [args]")
		fmt.Fprintln(stderr, "subcommands: event, run")
		return 1
	}

	switch args[0] {
	case "event":
		return runEvent(args[1:], stderr)
	case "run":
		return runWatcher(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		fmt.Fprintln(stderr, "usage: csuite-watcher <subcommand> [flags] [args]")
		fmt.Fprintln(stderr, "subcommands: event, run")
		return 1
	}
}

// runEvent handles the event subcommand: parses flags and JSON argument,
// opens the event bus, and publishes the event.
func runEvent(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("event", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "~/.drem-csuite/csuite.db", "path to the event bus SQLite database")
	_ = fs.String("routing", "", "path to the event routing config file")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(stderr, "error: JSON argument required")
		fmt.Fprintln(stderr, "usage: csuite-watcher event [--db <path>] [--routing <path>] '<json>'")
		return 1
	}

	ev, err := parseEventJSON(remaining[0])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	ev.Source = "csuite-watcher"

	actualPath := expandTilde(*dbPath)
	if err := os.MkdirAll(filepath.Dir(actualPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "error: create DB directory: %v\n", err)
		return 1
	}

	bus, err := eventbus.New(actualPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: open event bus: %v\n", err)
		return 1
	}
	defer bus.Close()

	if err := bus.Publish(&ev); err != nil {
		fmt.Fprintf(stderr, "error: publish event: %v\n", err)
		return 1
	}

	return 0
}

// expandTilde replaces a leading "~" with the user's home directory.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// eventPayload is the wire format for the JSON argument to csuite-watcher event.
type eventPayload struct {
	Type       string `json:"type"`
	TaskID     string `json:"task_id"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	Details    string `json:"details"`
	Timestamp  string `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// run subcommand
// ---------------------------------------------------------------------------

// watcherTomlConfig is the TOML structure for the [watcher] section of drem.toml.
type watcherTomlConfig struct {
	DBPath            string   `toml:"db_path"`
	InboxBaseDir      string   `toml:"inbox_base_dir"`
	AllowedAgents     []string `toml:"allowed_agents"`
	InboxPollInterval string   `toml:"inbox_poll_interval"`
	SafetyInterval    string   `toml:"safety_interval"`
	HeartbeatInterval string   `toml:"heartbeat_interval"`
}

// dremToml is the top-level TOML structure; only the [watcher] section is used.
type dremToml struct {
	Watcher watcherTomlConfig `toml:"watcher"`
}

// runWatcher handles the run subcommand: loads config, opens the database,
// creates a Runner, and blocks until SIGTERM or SIGINT.
func runWatcher(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "drem.toml", "path to the drem.toml config file")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg, err := loadWatcherConfig(expandTilde(*configPath))
	if err != nil {
		fmt.Fprintf(stderr, "error: load config: %v\n", err)
		return 1
	}

	dbPath := expandTilde(cfg.DBPath)
	if dbPath == "" {
		dbPath = expandTilde("~/.drem-csuite/watcher.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "error: create DB directory: %v\n", err)
		return 1
	}

	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	gormLogger := logger.New(log.New(stderr, "", 0), logger.Config{LogLevel: logger.Silent})
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		fmt.Fprintf(stderr, "error: open database: %v\n", err)
		return 1
	}

	// Auto-migrate watcher tables.
	if err := db.AutoMigrate(&watcher.TurnMetric{}); err != nil {
		fmt.Fprintf(stderr, "error: migrate turn_metrics: %v\n", err)
		return 1
	}

	runnerCfg := toRunnerConfig(cfg)

	// Create a real CommandRunner that spawns claude subprocesses.
	runner := &claudeCommandRunner{workDir: expandTilde(cfg.InboxBaseDir)}

	r := watcher.NewRunner(runnerCfg, db, runner)

	// Set up signal-driven context cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := r.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// loadWatcherConfig reads the [watcher] section from a drem.toml file.
// If the file does not exist, returns a zero-value config (no error) so
// defaults apply.
func loadWatcherConfig(path string) (watcherTomlConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return watcherTomlConfig{}, nil
		}
		return watcherTomlConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg dremToml
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return watcherTomlConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.Watcher, nil
}

// toRunnerConfig converts a watcherTomlConfig to a watcher.RunnerConfig,
// parsing duration strings and applying defaults.
func toRunnerConfig(cfg watcherTomlConfig) watcher.RunnerConfig {
	rc := watcher.RunnerConfig{
		AllowedAgents: cfg.AllowedAgents,
		InboxBaseDir:  expandTilde(cfg.InboxBaseDir),
	}
	if rc.AllowedAgents == nil {
		rc.AllowedAgents = []string{"mike", "alex", "ross", "seth"}
	}
	if rc.InboxBaseDir == "" {
		rc.InboxBaseDir = expandTilde("~/.drem-csuite")
	}
	if d, err := time.ParseDuration(cfg.InboxPollInterval); err == nil {
		rc.InboxPollInterval = d
	}
	if d, err := time.ParseDuration(cfg.SafetyInterval); err == nil {
		rc.SafetyInterval = d
	}
	if d, err := time.ParseDuration(cfg.HeartbeatInterval); err == nil {
		rc.HeartbeatInterval = d
	}
	return rc
}

// claudeCommandRunner is the production CommandRunner that spawns claude
// subprocesses. It satisfies watcher.CommandRunner.
type claudeCommandRunner struct {
	workDir string
}

func (r *claudeCommandRunner) Run(ctx context.Context, agent string) ([]byte, int, error) {
	return watcher.RunClaudeSubprocess(ctx, agent, r.workDir)
}

// ---------------------------------------------------------------------------
// event subcommand helpers
// ---------------------------------------------------------------------------

// parseEventJSON deserialises a JSON string into an Event ready for publishing.
// ID is intentionally left empty; the bus assigns it on Publish.
// Source is not set here; runEvent sets it to "csuite-watcher".
func parseEventJSON(jsonStr string) (eventbus.Event, error) {
	var p eventPayload
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		return eventbus.Event{}, fmt.Errorf("invalid JSON: %w", err)
	}

	var createdAt time.Time
	if p.Timestamp != "" {
		t, err := time.Parse(time.RFC3339, p.Timestamp)
		if err != nil {
			return eventbus.Event{}, fmt.Errorf("invalid timestamp %q: %w", p.Timestamp, err)
		}
		createdAt = t
	}

	return eventbus.Event{
		// ID intentionally not set — bus auto-generates on Publish.
		Type:       p.Type,
		TaskID:     p.TaskID,
		FromStatus: p.FromStatus,
		ToStatus:   p.ToStatus,
		Details:    p.Details,
		CreatedAt:  createdAt,
	}, nil
}
