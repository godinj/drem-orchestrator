package watcher_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// writeMockScript writes a shell script with the given body to a temp directory
// and makes it executable. Returns the path to the script.
func writeMockScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mock-claude")
	content := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	return path
}

// claudeJSON builds a JSON payload matching the claude --output-format json
// structure with the given token counts.
func claudeJSON(inputTokens, outputTokens int) string {
	type usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	type response struct {
		Usage usage `json:"usage"`
	}
	data, _ := json.Marshal(response{Usage: usage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}})
	return string(data)
}

// TestRunTurn_Success verifies that a successful turn returns correct token
// counts, zero exit status, positive duration, and StartedAt before EndedAt.
func TestRunTurn_Success(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewMetricsStore(db)
	lm := watcher.NewLifecycleManagerFromStore(store)

	script := writeMockScript(t, fmt.Sprintf("echo '%s'\n", claudeJSON(100, 50)))
	lm.ClaudeBin = script

	result, err := lm.RunTurn("alice", "do something useful")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.InputTokens != 100 {
		t.Errorf("InputTokens: got %d, want 100", result.InputTokens)
	}
	if result.OutputTokens != 50 {
		t.Errorf("OutputTokens: got %d, want 50", result.OutputTokens)
	}
	if result.ExitStatus != 0 {
		t.Errorf("ExitStatus: got %d, want 0", result.ExitStatus)
	}
	if result.Duration <= 0 {
		t.Errorf("Duration should be > 0, got %v", result.Duration)
	}
	if result.StartedAt.IsZero() {
		t.Error("StartedAt should not be zero")
	}
	if result.EndedAt.IsZero() {
		t.Error("EndedAt should not be zero")
	}
	if !result.StartedAt.Before(result.EndedAt) {
		t.Errorf("StartedAt %v must be before EndedAt %v", result.StartedAt, result.EndedAt)
	}
}

// TestRunTurn_FailingCommand verifies that a subprocess that exits non-zero
// causes LifecycleResult to carry the non-zero status and captured stderr content.
func TestRunTurn_FailingCommand(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewMetricsStore(db)
	lm := watcher.NewLifecycleManagerFromStore(store)

	script := writeMockScript(t, "echo 'something went wrong' >&2\nexit 2\n")
	lm.ClaudeBin = script

	result, err := lm.RunTurn("alice", "do something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitStatus == 0 {
		t.Error("ExitStatus: got 0, want non-zero")
	}
	if result.ErrorDetails == "" {
		t.Error("ErrorDetails should contain stderr content, got empty string")
	}
}

// TestRunTurn_KyleException verifies that RunTurn("kyle", ...) returns
// ErrKyleException immediately without launching any subprocess.
func TestRunTurn_KyleException(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewMetricsStore(db)
	lm := watcher.NewLifecycleManagerFromStore(store)

	// The script would create a marker file if the subprocess is launched.
	dir := t.TempDir()
	markerFile := filepath.Join(dir, "kyle-was-here")
	script := writeMockScript(t, fmt.Sprintf("touch %s\n", markerFile))
	lm.ClaudeBin = script

	_, err := lm.RunTurn("kyle", "any prompt")
	if err != watcher.ErrKyleException {
		t.Errorf("expected ErrKyleException, got %v", err)
	}

	// The marker file must NOT exist — subprocess must not have been launched.
	if _, statErr := os.Stat(markerFile); statErr == nil {
		t.Error("subprocess was launched despite kyle exception — marker file exists")
	}
}

// TestRunTurn_Timeout verifies that a subprocess running longer than the
// configured timeout is killed and LifecycleResult reflects a non-zero exit status
// with error details indicating termination.
func TestRunTurn_Timeout(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewMetricsStore(db)
	lm := watcher.NewLifecycleManagerFromStore(store)
	lm.Timeout = 500 * time.Millisecond

	script := writeMockScript(t, "sleep 30\n")
	lm.ClaudeBin = script

	result, err := lm.RunTurn("alice", "do something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitStatus == 0 {
		t.Error("ExitStatus: got 0, want non-zero after timeout kill")
	}
	if result.ErrorDetails == "" {
		t.Error("ErrorDetails should describe timeout/killed, got empty string")
	}
}

// TestRunTurn_RecordsMetrics verifies that after a successful turn the
// MetricsStore contains exactly one TurnMetric with values matching the result.
func TestRunTurn_RecordsMetrics(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewMetricsStore(db)
	lm := watcher.NewLifecycleManagerFromStore(store)

	script := writeMockScript(t, fmt.Sprintf("echo '%s'\n", claudeJSON(200, 80)))
	lm.ClaudeBin = script

	result, err := lm.RunTurn("bob", "some prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	turns, err := store.GetTurns()
	if err != nil {
		t.Fatalf("GetTurns error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn metric, got %d", len(turns))
	}

	m := turns[0]
	if m.Agent != "bob" {
		t.Errorf("Agent: got %q, want %q", m.Agent, "bob")
	}
	if m.InputTokens != result.InputTokens {
		t.Errorf("InputTokens: got %d, want %d", m.InputTokens, result.InputTokens)
	}
	if m.OutputTokens != result.OutputTokens {
		t.Errorf("OutputTokens: got %d, want %d", m.OutputTokens, result.OutputTokens)
	}
	if m.ExitStatus != result.ExitStatus {
		t.Errorf("ExitStatus: got %d, want %d", m.ExitStatus, result.ExitStatus)
	}
	if m.StartedAt.IsZero() {
		t.Error("persisted StartedAt should not be zero")
	}
	if m.EndedAt.IsZero() {
		t.Error("persisted EndedAt should not be zero")
	}
}

// TestRunTurn_InvalidJSON verifies that when the subprocess outputs invalid
// JSON the call still succeeds (graceful degradation) with zero token counts.
func TestRunTurn_InvalidJSON(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	store := watcher.NewMetricsStore(db)
	lm := watcher.NewLifecycleManagerFromStore(store)

	script := writeMockScript(t, "echo 'this is not valid json'\n")
	lm.ClaudeBin = script

	result, err := lm.RunTurn("alice", "do something")
	if err != nil {
		t.Errorf("expected no error on invalid JSON output, got: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Errorf("ExitStatus: got %d, want 0", result.ExitStatus)
	}
	if result.InputTokens != 0 {
		t.Errorf("InputTokens: got %d, want 0 (graceful degradation)", result.InputTokens)
	}
	if result.OutputTokens != 0 {
		t.Errorf("OutputTokens: got %d, want 0 (graceful degradation)", result.OutputTokens)
	}
}
