package watcher_test

import (
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

func waitForLifecycleRunCount(t *testing.T, runner *lifecycleMockRunner, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runner.runCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d lifecycle runs; got %d", want, runner.runCount())
}

// TestCooldown_TriggerDuringRunning verifies that triggers received while a
// turn is running return Queued (not Cooldown). Cooldown only applies
// BETWEEN turns (after a turn ends), not while one is still running.
func TestCooldown_TriggerDuringRunning(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	block := make(chan struct{})
	ready := make(chan struct{})
	runner := &lifecycleMockRunner{
		output: claudeJSON(100, 50),
		block:  block,
		ready:  ready,
	}

	// Configure 1-second cooldown
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  1 * time.Second,
	}
	lm := watcher.NewLifecycleManager(db, cfg, runner)
	defer func() {
		close(block)
		lm.Close()
	}()

	// First trigger should start immediately
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Wait for the turn to actually begin running
	<-ready

	// Second trigger while running: should queue (not cooldown)
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerQueued {
		t.Errorf("second trigger while running: got %v, want TriggerQueued", r2)
	}

	// Third trigger while running: queued flag is idempotent, returns Queued
	r3 := lm.TriggerAgent("mike")
	if r3 != watcher.TriggerQueued {
		t.Errorf("third trigger while running: got %v, want TriggerQueued", r3)
	}
}

// TestCooldown_AfterTurnEnds verifies that cooldown kicks in AFTER a turn
// completes. A trigger during the cooldown period returns Cooldown.
func TestCooldown_AfterTurnEnds(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}

	// Configure 2-second cooldown so we have time to trigger during it
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  2 * time.Second,
	}
	lm := watcher.NewLifecycleManager(db, cfg, runner)
	defer lm.Close()

	// First trigger starts and completes quickly (no block channel)
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Wait for the turn to complete and lastTurnEnd to be set
	// The turn runs quickly (mock runner), drainQueue sets lastTurnEnd
	// and clears running state
	time.Sleep(200 * time.Millisecond)

	// Now trigger during the cooldown window — should get Cooldown
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerCooldown {
		t.Errorf("trigger during cooldown: got %v, want Cooldown (%v)", r2, watcher.TriggerCooldown)
	}
}

// TestCooldown_IdleTriggerDuringCooldownEventuallyRuns verifies that a trigger
// after a turn has completed, but before cooldown expires, is not lost.
func TestCooldown_IdleTriggerDuringCooldownEventuallyRuns(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  200 * time.Millisecond,
	}
	lm := watcher.NewLifecycleManager(db, cfg, runner)
	defer lm.Close()

	if r := lm.TriggerAgent("mike"); r != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r)
	}
	waitForLifecycleRunCount(t, runner, 1)
	time.Sleep(20 * time.Millisecond)

	if r := lm.TriggerAgent("mike"); r != watcher.TriggerCooldown {
		t.Fatalf("cooldown trigger: got %v, want TriggerCooldown", r)
	}

	waitForLifecycleRunCount(t, runner, 2)
}

// TestCooldown_IdleCooldownTriggersDedup verifies that repeated triggers while
// a delayed cooldown turn is already scheduled coalesce into that one turn.
func TestCooldown_IdleCooldownTriggersDedup(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})
	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  150 * time.Millisecond,
	}
	lm := watcher.NewLifecycleManager(db, cfg, runner)
	defer lm.Close()

	if r := lm.TriggerAgent("mike"); r != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r)
	}
	waitForLifecycleRunCount(t, runner, 1)
	time.Sleep(20 * time.Millisecond)

	if r := lm.TriggerAgent("mike"); r != watcher.TriggerCooldown {
		t.Fatalf("first cooldown trigger: got %v, want TriggerCooldown", r)
	}
	if r := lm.TriggerAgent("mike"); r != watcher.TriggerCooldown {
		t.Fatalf("second cooldown trigger: got %v, want TriggerCooldown", r)
	}
	if r := lm.TriggerAgent("mike"); r != watcher.TriggerCooldown {
		t.Fatalf("third cooldown trigger: got %v, want TriggerCooldown", r)
	}

	waitForLifecycleRunCount(t, runner, 2)
	time.Sleep(250 * time.Millisecond)
	if got := runner.runCount(); got != 2 {
		t.Fatalf("cooldown triggers should coalesce into one delayed run; got %d runs", got)
	}
}

// TestCooldown_QueuedTurnRespectsCooldown verifies that a queued trigger
// fires after the cooldown period when the first turn completes.
func TestCooldown_QueuedTurnRespectsCooldown(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	// Configure 1-second cooldown
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  1 * time.Second,
	}

	// Block the first run to allow the second trigger to queue
	block := make(chan struct{})
	ready := make(chan struct{})
	runner := &lifecycleMockRunner{
		output: claudeJSON(100, 50),
		block:  block,
		ready:  ready,
	}

	lm := watcher.NewLifecycleManager(db, cfg, runner)

	// First trigger: launches a subprocess that blocks until we close(block)
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Wait until the subprocess goroutine has actually entered Run
	<-ready

	// Second trigger while first turn is still running: must be Queued
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerQueued {
		t.Errorf("second trigger while running: got %v, want TriggerQueued", r2)
	}

	// Third trigger while running+queued: idempotent queue, returns Queued
	r3 := lm.TriggerAgent("mike")
	if r3 != watcher.TriggerQueued {
		t.Errorf("third trigger while running: got %v, want TriggerQueued", r3)
	}

	// Unblock the first turn — should auto-start the queued turn after cooldown
	close(block)

	// Wait for both turns to complete (should take ~1 second for cooldown)
	lm.Close()
}

// TestCooldown_ZeroMeansNoCooldown verifies that a zero cooldown (default)
// means no cooldown and normal dedup behavior applies.
func TestCooldown_ZeroMeansNoCooldown(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	block := make(chan struct{})
	ready := make(chan struct{})
	runner := &lifecycleMockRunner{
		output: claudeJSON(100, 50),
		block:  block,
		ready:  ready,
	}

	// Zero cooldown (default) should behave like original behavior
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  0, // No cooldown
	}
	lm := watcher.NewLifecycleManager(db, cfg, runner)
	defer func() {
		close(block)
		lm.Close()
	}()

	// First trigger should start immediately
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger: got %v, want TriggerStarted", r1)
	}

	// Wait for run to begin
	<-ready

	// Second trigger should queue (normal behavior)
	r2 := lm.TriggerAgent("mike")
	if r2 != watcher.TriggerQueued {
		t.Errorf("second trigger while running: got %v, want TriggerQueued", r2)
	}

	// Third trigger while running+queued: idempotent queue, returns Queued
	r3 := lm.TriggerAgent("mike")
	if r3 != watcher.TriggerQueued {
		t.Errorf("third trigger while running: got %v, want TriggerQueued", r3)
	}
}

// TestCooldown_IndependentPerAgent verifies that cooldowns are independent
// per agent.
func TestCooldown_IndependentPerAgent(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	// Configure 1-second cooldown
	cfg := watcher.Config{
		AllowedAgents: []string{"mike", "alex"},
		TurnCooldown:  1 * time.Second,
	}

	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	lm := watcher.NewLifecycleManager(db, cfg, runner)
	defer lm.Close()

	// Trigger for mike
	r1 := lm.TriggerAgent("mike")
	if r1 != watcher.TriggerStarted {
		t.Fatalf("first trigger for mike: got %v, want TriggerStarted", r1)
	}

	// Trigger for alex (should not be affected by mike's cooldown)
	r2 := lm.TriggerAgent("alex")
	if r2 != watcher.TriggerStarted {
		t.Errorf("trigger for alex: got %v, want TriggerStarted", r2)
	}
}

// TestCooldown_CloseDoesNotHang verifies that Close() does not hang
// even if there are pending cooldown delays.
func TestCooldown_CloseDoesNotHang(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &watcher.TurnMetric{})

	// Configure 10-second cooldown to give us time to test Close()
	cfg := watcher.Config{
		AllowedAgents: []string{"mike"},
		TurnCooldown:  10 * time.Second,
	}

	runner := &lifecycleMockRunner{output: claudeJSON(100, 50)}
	lm := watcher.NewLifecycleManager(db, cfg, runner)

	// Trigger the first turn to start cooldown
	lm.TriggerAgent("mike")

	// Close should not hang even though cooldown is pending
	done := make(chan struct{})
	go func() {
		lm.Close()
		close(done)
	}()

	select {
	case <-done:
		// Success - Close didn't hang
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung - should not block on pending cooldown")
	}
}
