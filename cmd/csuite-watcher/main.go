// Command csuite-watcher publishes events to the C-Suite event bus.
//
// Usage:
//
//	csuite-watcher <command> [flags]
//
// Commands:
//
//	event    Publish an event to the event bus from a JSON argument
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is the testable entry point. It parses args, dispatches subcommands,
// and returns an exit code. All error output goes to stderr.
func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: csuite-watcher <subcommand> [flags] [args]")
		fmt.Fprintln(stderr, "subcommands: event")
		return 1
	}

	switch args[0] {
	case "event":
		return runEvent(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		fmt.Fprintln(stderr, "usage: csuite-watcher <subcommand> [flags] [args]")
		fmt.Fprintln(stderr, "subcommands: event")
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

	if err := bus.Publish(ev); err != nil {
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
