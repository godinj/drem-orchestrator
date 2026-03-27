package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
)

// validEventJSON is a representative well-formed payload.
const validEventJSON = `{"type":"task_status_changed","task_id":"42","from_status":"planning","to_status":"failed","details":"context exhaustion","timestamp":"2026-03-26T14:30:00Z"}`

// ---------------------------------------------------------------------------
// parseEventJSON — unit tests (no I/O)
// ---------------------------------------------------------------------------

// TestParseEventJSON_ValidPayload verifies that all wire-format fields are
// mapped to the correct Event struct fields.
func TestParseEventJSON_ValidPayload(t *testing.T) {
	ev, err := parseEventJSON(validEventJSON)
	if err != nil {
		t.Fatalf("parseEventJSON returned unexpected error: %v", err)
	}

	if ev.Type != "task_status_changed" {
		t.Errorf("Type: got %q, want %q", ev.Type, "task_status_changed")
	}
	if ev.TaskID != "42" {
		t.Errorf("TaskID: got %q, want %q", ev.TaskID, "42")
	}
	if ev.FromStatus != "planning" {
		t.Errorf("FromStatus: got %q, want %q", ev.FromStatus, "planning")
	}
	if ev.ToStatus != "failed" {
		t.Errorf("ToStatus: got %q, want %q", ev.ToStatus, "failed")
	}
	if ev.Details != "context exhaustion" {
		t.Errorf("Details: got %q, want %q", ev.Details, "context exhaustion")
	}

	want := time.Date(2026, 3, 26, 14, 30, 0, 0, time.UTC)
	if !ev.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt: got %v, want %v", ev.CreatedAt, want)
	}
}

// TestParseEventJSON_IDNotSet verifies that parseEventJSON never populates
// Event.ID — the bus must assign IDs at publish time to avoid duplicates.
func TestParseEventJSON_IDNotSet(t *testing.T) {
	ev, err := parseEventJSON(validEventJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.ID != "" {
		t.Errorf("ID must be empty from parseEventJSON (bus auto-assigns), got %q", ev.ID)
	}
}

// TestParseEventJSON_SourceNotSet verifies that parseEventJSON does not set
// Source — that responsibility belongs to runEvent, which sets it to
// "csuite-watcher" before calling Publish.
func TestParseEventJSON_SourceNotSet(t *testing.T) {
	ev, err := parseEventJSON(validEventJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Source != "" {
		t.Errorf("Source must not be set by parseEventJSON, got %q", ev.Source)
	}
}

// TestParseEventJSON_InvalidJSON verifies that malformed JSON produces an error.
func TestParseEventJSON_InvalidJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"truncated object", `{"type":"foo"`},
		{"plain text", "not json at all"},
		{"array instead of object", `["a","b"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEventJSON(tc.input)
			if err == nil {
				t.Errorf("expected error for input %q, got nil", tc.input)
			}
		})
	}
}

// TestParseEventJSON_InvalidTimestamp verifies that an unparseable timestamp
// field returns an error.
func TestParseEventJSON_InvalidTimestamp(t *testing.T) {
	payload := `{"type":"x","timestamp":"not-a-date"}`
	_, err := parseEventJSON(payload)
	if err == nil {
		t.Error("expected error for invalid timestamp, got nil")
	}
}

// TestParseEventJSON_MissingTimestamp verifies that omitting timestamp is
// accepted and produces a zero CreatedAt (the bus or caller may fill it in).
func TestParseEventJSON_MissingTimestamp(t *testing.T) {
	payload := `{"type":"task_status_changed","task_id":"1"}`
	ev, err := parseEventJSON(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ev.CreatedAt.IsZero() {
		t.Errorf("expected zero CreatedAt when timestamp is omitted, got %v", ev.CreatedAt)
	}
}

// ---------------------------------------------------------------------------
// run() — exit code and stderr tests (no I/O)
// ---------------------------------------------------------------------------

// TestRun_NoArgs verifies that invoking without any arguments exits non-zero
// and prints usage information to stderr.
func TestRun_NoArgs(t *testing.T) {
	var buf bytes.Buffer
	code := run(nil, &buf)
	if code == 0 {
		t.Error("expected non-zero exit code with no args, got 0")
	}
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %q", buf.String())
	}
}

// TestRun_UnknownSubcommand verifies that an unrecognised subcommand exits
// non-zero and prints usage to stderr.
func TestRun_UnknownSubcommand(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"frobnicate"}, &buf)
	if code == 0 {
		t.Error("expected non-zero exit for unknown subcommand, got 0")
	}
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("expected usage message on stderr, got: %q", buf.String())
	}
}

// TestRun_EventInvalidJSON verifies that passing malformed JSON to the event
// subcommand exits non-zero and reports the error on stderr.
func TestRun_EventInvalidJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "csuite.db")
	var buf bytes.Buffer
	code := run([]string{"event", "--db", dbPath, "not valid json"}, &buf)
	if code == 0 {
		t.Error("expected non-zero exit for invalid JSON, got 0")
	}
	if buf.Len() == 0 {
		t.Error("expected error message on stderr, got empty output")
	}
}

// TestRun_EventMissingJSONArg verifies that omitting the JSON argument to the
// event subcommand exits non-zero with an error on stderr.
func TestRun_EventMissingJSONArg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "csuite.db")
	var buf bytes.Buffer
	code := run([]string{"event", "--db", dbPath}, &buf)
	if code == 0 {
		t.Error("expected non-zero exit when JSON arg is missing, got 0")
	}
	if buf.Len() == 0 {
		t.Error("expected error message on stderr, got empty output")
	}
}

// ---------------------------------------------------------------------------
// End-to-end: run() → eventbus DB
// ---------------------------------------------------------------------------

// TestRun_EventPublishesToDB verifies the full happy path: a valid JSON
// argument is parsed, published to the event bus, and the event is retrievable
// from the database with all fields set correctly.
func TestRun_EventPublishesToDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "csuite.db")
	var buf bytes.Buffer
	code := run([]string{"event", "--db", dbPath, validEventJSON}, &buf)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, buf.String())
	}

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("open bus: %v", err)
	}
	defer bus.Close()

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event in DB, got %d", len(events))
	}

	ev := events[0]
	if ev.Type != "task_status_changed" {
		t.Errorf("Type: got %q, want %q", ev.Type, "task_status_changed")
	}
	if ev.TaskID != "42" {
		t.Errorf("TaskID: got %q, want %q", ev.TaskID, "42")
	}
	if ev.FromStatus != "planning" {
		t.Errorf("FromStatus: got %q, want %q", ev.FromStatus, "planning")
	}
	if ev.ToStatus != "failed" {
		t.Errorf("ToStatus: got %q, want %q", ev.ToStatus, "failed")
	}
	if ev.Details != "context exhaustion" {
		t.Errorf("Details: got %q, want %q", ev.Details, "context exhaustion")
	}

	want := time.Date(2026, 3, 26, 14, 30, 0, 0, time.UTC)
	if !ev.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt: got %v, want %v", ev.CreatedAt, want)
	}
}

// TestRun_EventSetsSource verifies that the CLI sets Source to "csuite-watcher"
// on every published event, regardless of what the JSON payload contains.
func TestRun_EventSetsSource(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "csuite.db")
	var buf bytes.Buffer
	code := run([]string{"event", "--db", dbPath, validEventJSON}, &buf)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, buf.String())
	}

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("open bus: %v", err)
	}
	defer bus.Close()

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Source != "csuite-watcher" {
		t.Errorf("Source: got %q, want %q", events[0].Source, "csuite-watcher")
	}
}

// TestRun_EventIDAssignedByBus verifies that Event.ID is non-empty in the DB
// (assigned by the bus) even though the JSON payload contains no id field.
func TestRun_EventIDAssignedByBus(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "csuite.db")
	var buf bytes.Buffer
	code := run([]string{"event", "--db", dbPath, validEventJSON}, &buf)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %q", code, buf.String())
	}

	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("open bus: %v", err)
	}
	defer bus.Close()

	events, err := bus.Events()
	if err != nil {
		t.Fatalf("bus.Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID == "" {
		t.Error("Event.ID must be non-empty (assigned by bus on Publish)")
	}
}

// TestRun_EventCustomDBPath verifies that the --db flag controls which database
// file receives the event. Publishing to pathA must not create an event in pathB.
func TestRun_EventCustomDBPath(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.db")
	pathB := filepath.Join(dir, "b.db")

	var buf bytes.Buffer
	code := run([]string{"event", "--db", pathA, validEventJSON}, &buf)
	if code != 0 {
		t.Fatalf("run returned %d; stderr: %q", code, buf.String())
	}

	// pathA must contain the event.
	busA, err := eventbus.New(pathA)
	if err != nil {
		t.Fatalf("open busA: %v", err)
	}
	defer busA.Close()
	eventsA, err := busA.Events()
	if err != nil {
		t.Fatalf("busA.Events: %v", err)
	}
	if len(eventsA) != 1 {
		t.Errorf("pathA: expected 1 event, got %d", len(eventsA))
	}

	// pathB must be untouched (or absent).
	busB, err := eventbus.New(pathB)
	if err != nil {
		t.Fatalf("open busB: %v", err)
	}
	defer busB.Close()
	eventsB, err := busB.Events()
	if err != nil {
		t.Fatalf("busB.Events: %v", err)
	}
	if len(eventsB) != 0 {
		t.Errorf("pathB: expected 0 events, got %d", len(eventsB))
	}
}

// TestRun_EventRoutingFlagAccepted verifies that passing --routing with a path
// does not cause an error (the flag must be defined and parseable).
func TestRun_EventRoutingFlagAccepted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "csuite.db")
	routingPath := filepath.Join(t.TempDir(), "routing.toml")
	var buf bytes.Buffer
	run([]string{"event", "--db", dbPath, "--routing", routingPath, validEventJSON}, &buf)
	// We accept non-zero if the routing file doesn't exist, but the flag itself
	// must not cause an "unknown flag" failure. Check stderr for flag errors.
	if strings.Contains(buf.String(), "flag provided but not defined") {
		t.Errorf("--routing flag not defined; stderr: %q", buf.String())
	}
}
