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
	"fmt"
	"io"
	"os"
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

// runEvent handles the event subcommand (stub — always returns 0).
func runEvent(args []string, stderr io.Writer) int {
	_ = args
	_ = stderr
	return 0
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
