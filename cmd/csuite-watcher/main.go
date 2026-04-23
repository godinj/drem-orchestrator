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
//	serve    Start the bridge HTTP server
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
		fmt.Fprintln(stderr, "subcommands: event, run, serve")
		return 1
	}

	switch args[0] {
	case "event":
		return runEvent(args[1:], stderr)
	case "run":
		return runWatcher(args[1:], stderr)
	case "serve":
		return runServe(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		fmt.Fprintln(stderr, "usage: csuite-watcher <subcommand> [flags] [args]")
		fmt.Fprintln(stderr, "subcommands: event, run, serve")
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
	EventBusDBPath    string   `toml:"event_bus_db_path"`
	InboxBaseDir      string   `toml:"inbox_base_dir"`
	AllowedAgents     []string `toml:"allowed_agents"`
	PromptDir         string   `toml:"prompt_dir"`
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

	// Open the event bus database (csuite.db) for delivery polling and
	// pre-trigger inbox checks.
	eventBusPath := expandTilde(cfg.EventBusDBPath)
	if eventBusPath == "" {
		eventBusPath = expandTilde("~/.drem-csuite/csuite.db")
	}
	bus, err := eventbus.New(eventBusPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: open event bus: %v\n", err)
		return 1
	}
	defer bus.Close()

	// Pre-trigger inbox check: skip turns when agent has no relevant inbox
	// messages or unacked event bus deliveries. Filters events by type per
	// agent and auto-acks non-relevant events.
	runnerCfg.Precheck = watcher.NewFilteredPrecheck(
		runnerCfg.InboxBaseDir,
		&busAdapter{bus: bus},
	)

	// Create a real CommandRunner that spawns claude subprocesses.
	runner := &claudeCommandRunner{
		workDir:   expandTilde(cfg.InboxBaseDir),
		promptDir: runnerCfg.PromptDir,
	}

	r := watcher.NewRunner(runnerCfg, db, runner)

	// Set up signal-driven context cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Start delivery poller: periodically check the event bus for unacked
	// deliveries and push agent names onto the Runner's notifications channel.
	go pollDeliveries(ctx, bus, runnerCfg.AllowedAgents, r.Notifications())

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
		PromptDir:     expandTilde(cfg.PromptDir),
	}
	if rc.AllowedAgents == nil {
		rc.AllowedAgents = []string{"mike", "alex", "seth"}
	}
	if rc.InboxBaseDir == "" {
		rc.InboxBaseDir = expandTilde("~/.drem-csuite")
	}
	if rc.PromptDir == "" {
		rc.PromptDir = "docs/csuite-agents/prompts"
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

// busAdapter wraps *eventbus.Bus to satisfy watcher.FilteredEventBus,
// converting eventbus.Event to watcher.EventInfo.
type busAdapter struct {
	bus *eventbus.Bus
}

func (a *busAdapter) UnackedDeliveries(agent string) ([]watcher.EventInfo, error) {
	events, err := a.bus.UnackedDeliveries(agent)
	if err != nil {
		return nil, err
	}
	infos := make([]watcher.EventInfo, len(events))
	for i, e := range events {
		infos[i] = watcher.EventInfo{ID: e.ID, Type: e.Type}
	}
	return infos, nil
}

func (a *busAdapter) UnackedDeliveriesByTypes(agent string, eventTypes []string) ([]watcher.EventInfo, error) {
	events, err := a.bus.UnackedDeliveriesByTypes(agent, eventTypes)
	if err != nil {
		return nil, err
	}
	infos := make([]watcher.EventInfo, len(events))
	for i, e := range events {
		infos[i] = watcher.EventInfo{ID: e.ID, Type: e.Type}
	}
	return infos, nil
}

func (a *busAdapter) Ack(agent string, eventIDs []string) error {
	return a.bus.Ack(agent, eventIDs)
}

// claudeCommandRunner is the production CommandRunner that spawns claude
// subprocesses. It satisfies watcher.CommandRunner.
type claudeCommandRunner struct {
	workDir   string
	promptDir string
}

func (r *claudeCommandRunner) Run(ctx context.Context, agent string) ([]byte, int, error) {
	promptPath := filepath.Join(r.promptDir, agent+".md")
	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		log.Printf("watcher: skip turn for %s: prompt file not found: %s", agent, promptPath)
		return nil, -1, fmt.Errorf("prompt file not found: %s: %w", promptPath, err)
	}
	return watcher.RunClaudeSubprocess(ctx, agent, r.workDir, string(promptBytes))
}

// ---------------------------------------------------------------------------
// delivery poller
// ---------------------------------------------------------------------------

// pollDeliveries polls the event bus for unacked deliveries and pushes
// distinct agent names onto the notifications channel. It runs until ctx is
// cancelled.
//
// For each allowed agent, it calls bus.UnackedDeliveries. If there are unacked
// events, it collects the agent name and acks the events so they are not
// re-delivered on the next poll cycle. After checking all agents, if any had
// pending deliveries, the collected agent names are sent as a single
// notification batch.
func pollDeliveries(ctx context.Context, bus *eventbus.Bus, allowedAgents []string, notifications chan<- []string) {
	const pollInterval = 2 * time.Second

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var agents []string
			for _, agent := range allowedAgents {
				unacked, err := bus.UnackedDeliveries(agent)
				if err != nil {
					log.Printf("watcher: poll deliveries for %s: %v", agent, err)
					continue
				}
				if len(unacked) == 0 {
					continue
				}

				// Collect event IDs for acking.
				ids := make([]string, len(unacked))
				for i, ev := range unacked {
					ids[i] = ev.ID
				}
				if err := bus.Ack(agent, ids); err != nil {
					log.Printf("watcher: ack deliveries for %s: %v", agent, err)
					continue
				}

				agents = append(agents, agent)
			}

			if len(agents) > 0 {
				select {
				case notifications <- agents:
				case <-ctx.Done():
					return
				}
			}
		}
	}
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
