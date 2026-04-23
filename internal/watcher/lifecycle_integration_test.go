package watcher_test

// lifecycle_integration_test.go exercises the complete Phase 2 vertical slice:
//
//   TriggerAgent("mike") → subprocess launches, metrics recorded, Started
//   TriggerAgent("mike") while running → Queued, no second subprocess
//   Queued trigger auto-starts after first turn → two turn_metrics rows
//   TriggerAgent("kyle") → Refused, no subprocess, no metrics row
//
// Each test creates an isolated in-memory SQLite database via testutil and
// injects a lifecycleMockRunner that controls JSON output, exit codes, and blocking
// behaviour so concurrency scenarios are deterministic.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// lifecycleMockRunner is a controllable CommandRunner injected into LifecycleManager
// during integration tests. It avoids real subprocess execution.
//
//	output   — stdout bytes returned to the caller (JSON)
//	exitCode — process exit code returned to the caller
//	block    — if non-nil, Run blocks until the channel is closed (simulates
//	           a long-running turn for concurrency tests)
//	ready    — if non-nil, closed on the first Run call so tests can
//	           synchronise on "subprocess has started"
type lifecycleMockRunner struct {
	output   string
	exitCode int
	block    chan struct{} // close to unblock all waiting Run calls
	ready    chan struct{} // closed when Run is first called

	mu    sync.Mutex
	count int // number of times Run has been called
}

func (r *lifecycleMockRunner) Run(_ context.Context, _ string) ([]byte, int, error) {
	r.mu.Lock()
	r.count++
	isFirst := r.count == 1
	r.mu.Unlock()

	// Signal the first call so concurrency tests can synchronise.
	if isFirst && r.ready != nil {
		close(r.ready)
	}

	// Optionally block until the test unblocks us (or ctx is cancelled).
	if r.block != nil {
		<-r.block
	}

	return []byte(r.output), r.exitCode, nil
}

func (r *lifecycleMockRunner) runCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// allowedCfg returns a Config that permits mike, alex, and seth.
// kyle is intentionally absent.
func allowedCfg() watcher.Config {
	return watcher.Config{
		AllowedAgents: []string{"mike", "alex", "seth"},
	}
}

// integrationJSON returns a JSON payload that matches the claude
// --output-format json schema with the provided token counts and event metrics.
func integrationJSON(inputTokens, outputTokens, eventsProcessed, messagesSent int) string {
	type usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	type response struct {
		Usage           usage `json:"usage"`
		EventsProcessed int   `json:"events_processed"`
		MessagesSent    int   `json:"messages_sent"`
	}
	data, _ := json.Marshal(response{
		Usage:           usage{InputTokens: inputTokens, OutputTokens: outputTokens},
		EventsProcessed: eventsProcessed,
		MessagesSent:    messagesSent,
	})
	return string(data)
}

// TestLifecycleIntegration_TriggerStarted verifies the happy-path trigger:
// TriggerAgent for a permitted agent with no turn in progress returns
// TriggerStarted, meaning a subprocess was launched immediately.
//
// Lifecycle scenario: idle manager → trigger "mike" → Started.
func TestLifecycleIntegration_TriggerStarted(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	lm := watcher.NewLifecycleManager(db, allowedCfg(), runner)
	defer lm.Close()

	result := lm.TriggerAgent("mike")
	if result != watcher.TriggerStarted {
		t.Errorf("TriggerAgent(\"mike\"): got %v, want TriggerStarted (%v)", result, watcher.TriggerStarted)
	}
}

// TestLifecycleIntegration_MetricsRecorded verifies that after a turn
// completes, a row exists in the turn_metrics table with the correct field
// values sourced from the mock subprocess output.
//
// Lifecycle scenario: trigger "mike" → turn completes → DB row persisted.
func TestLifecycleIntegration_MetricsRecorded(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: integrationJSON(100, 50, 12, 7)}
	lm := watcher.NewLifecycleManager(db, allowedCfg(), runner)

	lm.TriggerAgent("mike")
	lm.Close() // waits for the turn goroutine to finish

	var metrics []watcher.TurnMetric
	if err := db.Where("agent = ?", "mike").Find(&metrics).Error; err != nil {
		t.Fatalf("query turn_metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 turn_metrics row for mike, got %d", len(metrics))
	}

	m := metrics[0]
	if m.Agent != "mike" {
		t.Errorf("Agent: got %q, want \"mike\"", m.Agent)
	}
	if m.Duration <= 0 {
		t.Errorf("Duration: got %v, want > 0", m.Duration)
	}
	if m.InputTokens != 100 {
		t.Errorf("InputTokens: got %d, want 100", m.InputTokens)
	}
	if m.OutputTokens != 50 {
		t.Errorf("OutputTokens: got %d, want 50", m.OutputTokens)
	}
	if m.EventsProcessed != 12 {
		t.Errorf("EventsProcessed: got %d, want 12", m.EventsProcessed)
	}
	if m.MessagesSent != 7 {
		t.Errorf("MessagesSent: got %d, want 7", m.MessagesSent)
	}
	if m.ExitStatus != 0 {
		t.Errorf("ExitStatus: got %d, want 0", m.ExitStatus)
	}
}

// TestLifecycleIntegration_TriggerWhileRunning_Queued verifies that a second
// TriggerAgent call for the same agent while the first turn is still executing
// returns TriggerQueued without launching a second concurrent subprocess.
//
// Lifecycle scenario: trigger "mike" → still running → trigger "mike" again
// → Queued, subprocess count stays at 1.
func TestLifecycleIntegration_TriggerWhileRunning_Queued(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	block := make(chan struct{})
	ready := make(chan struct{})
	runner := &lifecycleMockRunner{
		output: claudeJSON(100, 50),
		block:  block,
		ready:  ready,
	}
	lm := watcher.NewLifecycleManager(db, allowedCfg(), runner)
	defer func() {
		close(block) // unblock any waiting Run call before Close
		lm.Close()
	}()

	// First trigger: launches a subprocess that blocks until we close(block).
	// Use Fatalf so the test stops before the blocking <-ready if the stub
	// returns the wrong value (avoids a hang in TDD stub mode).
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Wait until the subprocess goroutine has actually entered Run, so we
	// know the turn is in-flight before issuing the second trigger.
	<-ready

	// Second trigger while first turn is still running: must be Queued.
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerQueued {
		t.Errorf("second trigger while running: got %v, want TriggerQueued", r2)
	}

	// Only one subprocess should have been launched during the concurrent window.
	if runner.runCount() != 1 {
		t.Errorf("subprocess launch count: got %d, want 1 (no concurrent launch)", runner.runCount())
	}
}

// TestLifecycleIntegration_QueuedAutoStarts verifies that after a queued
// trigger is registered, it automatically starts a second turn once the
// first turn completes. After both turns finish there should be two
// turn_metrics rows for mike.
//
// Lifecycle scenario: trigger "mike" (runs) → trigger "mike" (queued) →
// first turn finishes → queued turn auto-starts → both rows in DB.
func TestLifecycleIntegration_QueuedAutoStarts(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	// block is closed once to unblock the first turn; subsequent Run calls
	// return immediately because reading from a closed channel never blocks.
	block := make(chan struct{})
	ready := make(chan struct{})
	runner := &lifecycleMockRunner{
		output: claudeJSON(100, 50),
		block:  block,
		ready:  ready,
	}
	lm := watcher.NewLifecycleManager(db, allowedCfg(), runner)

	// Start the first turn (blocks on block channel). Fatalf stops the test
	// before the blocking <-ready to avoid a hang in TDD stub mode.
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Synchronise: wait for the first Run call to have started.
	<-ready

	// Queue a second trigger while first turn is still in-flight.
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerQueued {
		t.Errorf("second trigger while running: got %v, want TriggerQueued", r2)
	}

	// Unblock the first turn. The manager should auto-start the queued one.
	// Closing the channel also unblocks any future Run calls immediately,
	// so the second turn completes without additional control.
	close(block)

	// Close waits for both the first turn and the auto-started queued turn
	// to complete before returning.
	lm.Close()

	// Both turns must have produced a turn_metrics row.
	var metrics []watcher.TurnMetric
	if err := db.Where("agent = ?", "mike").Find(&metrics).Error; err != nil {
		t.Fatalf("query turn_metrics: %v", err)
	}
	if len(metrics) != 2 {
		t.Errorf("expected 2 turn_metrics rows for mike after queued auto-start, got %d", len(metrics))
	}
}

// TestLifecycleIntegration_RefusedAgent verifies that TriggerAgent for an
// agent not in Config.AllowedAgents returns TriggerRefused without launching
// any subprocess and without recording any metrics row.
//
// Lifecycle scenario: trigger "kyle" (not allowed) → Refused, no DB row.
func TestLifecycleIntegration_RefusedAgent(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	lm := watcher.NewLifecycleManager(db, allowedCfg(), runner)
	defer lm.Close()

	result := lm.TriggerAgent("kyle")
	if result != watcher.TriggerRefused {
		t.Errorf("TriggerAgent(\"kyle\"): got %v, want TriggerRefused (%v)", result, watcher.TriggerRefused)
	}

	// No subprocess must have been launched.
	if runner.runCount() != 0 {
		t.Errorf("subprocess launch count: got %d, want 0 (kyle is refused)", runner.runCount())
	}

	// No turn_metrics row must exist for kyle.
	var count int64
	if err := db.Model(&watcher.TurnMetric{}).Where("agent = ?", "kyle").Count(&count).Error; err != nil {
		t.Fatalf("query turn_metrics count: %v", err)
	}
	if count != 0 {
		t.Errorf("turn_metrics rows for kyle: got %d, want 0", count)
	}
}

// mockPrecheck is a controllable TurnPrecheck for testing.
type mockPrecheck struct {
	hasWork bool
}

func (m *mockPrecheck) HasWork(_ string) bool {
	return m.hasWork
}

// TestLifecycleIntegration_PrecheckSkips verifies that when a TurnPrecheck
// reports no work, the turn is skipped: no subprocess is launched and no
// turn_metrics row is created.
//
// Lifecycle scenario: trigger "mike" with precheck returning false → Started
// (trigger accepted), but subprocess skipped, no DB row.
func TestLifecycleIntegration_PrecheckSkips(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	cfg := allowedCfg()
	cfg.Precheck = &mockPrecheck{hasWork: false}
	lm := watcher.NewLifecycleManager(db, cfg, runner)

	lm.TriggerAgent("mike")
	lm.Close() // waits for the turn goroutine to finish

	// No subprocess must have been launched.
	if runner.runCount() != 0 {
		t.Errorf("subprocess launch count: got %d, want 0 (precheck returned false)", runner.runCount())
	}

	// No turn_metrics row must exist — skipped turns do not record metrics.
	var count int64
	if err := db.Model(&watcher.TurnMetric{}).Where("agent = ?", "mike").Count(&count).Error; err != nil {
		t.Fatalf("query turn_metrics count: %v", err)
	}
	if count != 0 {
		t.Errorf("turn_metrics rows for mike: got %d, want 0 (turn was skipped)", count)
	}
}

// TestLifecycleIntegration_PrecheckAllowsRun verifies that when a TurnPrecheck
// reports work, the turn proceeds normally: subprocess is launched and
// turn_metrics are recorded.
//
// Lifecycle scenario: trigger "mike" with precheck returning true → Started,
// subprocess runs, DB row persisted.
func TestLifecycleIntegration_PrecheckAllowsRun(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	cfg := allowedCfg()
	cfg.Precheck = &mockPrecheck{hasWork: true}
	lm := watcher.NewLifecycleManager(db, cfg, runner)

	lm.TriggerAgent("mike")
	lm.Close()

	if runner.runCount() != 1 {
		t.Errorf("subprocess launch count: got %d, want 1", runner.runCount())
	}

	var count int64
	if err := db.Model(&watcher.TurnMetric{}).Where("agent = ?", "mike").Count(&count).Error; err != nil {
		t.Fatalf("query turn_metrics count: %v", err)
	}
	if count != 1 {
		t.Errorf("turn_metrics rows for mike: got %d, want 1", count)
	}
}

// TestLifecycleIntegration_PrecheckNilMeansRun verifies that a nil Precheck
// (the default) does not interfere — turns run unconditionally.
func TestLifecycleIntegration_PrecheckNilMeansRun(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	cfg := allowedCfg()
	// cfg.Precheck is nil (default)
	lm := watcher.NewLifecycleManager(db, cfg, runner)

	lm.TriggerAgent("mike")
	lm.Close()

	if runner.runCount() != 1 {
		t.Errorf("subprocess launch count: got %d, want 1 (nil precheck should not block)", runner.runCount())
	}
}

// TestLifecycleIntegration_PrecheckSkipHonoursQueuedTrigger verifies that
// when a turn is skipped by precheck and a queued trigger exists, the queued
// trigger still fires (which may also be skipped if still no work).
func TestLifecycleIntegration_PrecheckSkipHonoursQueuedTrigger(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	// First turn blocks so we can queue a second trigger.
	block := make(chan struct{})
	ready := make(chan struct{})
	runner := &lifecycleMockRunner{
		output: claudeJSON(100, 50),
		block:  block,
		ready:  ready,
	}

	// Precheck starts as hasWork=true (first turn runs), then after the first
	// turn starts, we set hasWork=false and queue a second trigger. The queued
	// trigger should skip because precheck returns false.
	precheck := &mockPrecheck{hasWork: true}
	cfg := allowedCfg()
	cfg.Precheck = precheck
	lm := watcher.NewLifecycleManager(db, cfg, runner)

	// Start first turn (blocks on block channel).
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Wait for the subprocess to begin.
	<-ready

	// Now set precheck to false and queue a second trigger.
	precheck.hasWork = false
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerQueued {
		t.Errorf("second trigger: got %v, want TriggerQueued", r2)
	}

	// Unblock first turn. Queued trigger fires, precheck returns false → skip.
	close(block)
	lm.Close()

	// Only one subprocess should have been launched (the first turn).
	// The queued trigger was skipped by precheck.
	if runner.runCount() != 1 {
		t.Errorf("subprocess launch count: got %d, want 1 (queued trigger should be skipped)", runner.runCount())
	}

	// Only one turn_metrics row should exist (from the first turn).
	var count int64
	if err := db.Model(&watcher.TurnMetric{}).Where("agent = ?", "mike").Count(&count).Error; err != nil {
		t.Fatalf("query turn_metrics count: %v", err)
	}
	if count != 1 {
		t.Errorf("turn_metrics rows for mike: got %d, want 1 (queued turn was skipped)", count)
	}
}
