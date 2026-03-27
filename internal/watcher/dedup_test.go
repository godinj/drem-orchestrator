package watcher_test

import (
	"sync"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// mockRunner is a TurnRunner whose RunTurn blocks until the test signals
// completion. Call prepareRun before each expected turn to obtain control
// channels.
type mockRunner struct {
	mu     sync.Mutex
	counts map[string]int
	queue  map[string][]runSlot
}

type runSlot struct {
	started chan struct{} // closed when RunTurn begins
	done    chan struct{} // close to unblock RunTurn
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		counts: make(map[string]int),
		queue:  make(map[string][]runSlot),
	}
}

// prepareRun enqueues a blocking slot for agent's next RunTurn call. It
// returns a started channel that is closed when RunTurn begins, and a
// complete function that unblocks RunTurn when called.
func (m *mockRunner) prepareRun(agent string) (<-chan struct{}, func()) {
	slot := runSlot{
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	m.mu.Lock()
	m.queue[agent] = append(m.queue[agent], slot)
	m.mu.Unlock()
	return slot.started, func() { close(slot.done) }
}

// RunTurn implements TurnRunner. It pops the next queued slot, signals
// started, then blocks until done is closed. Returns immediately if no
// slot is prepared.
func (m *mockRunner) RunTurn(agent string) {
	m.mu.Lock()
	m.counts[agent]++
	var slot runSlot
	if len(m.queue[agent]) > 0 {
		slot = m.queue[agent][0]
		m.queue[agent] = m.queue[agent][1:]
	}
	m.mu.Unlock()

	if slot.started != nil {
		close(slot.started)
		<-slot.done
	}
}

// count returns how many times RunTurn was invoked for agent.
func (m *mockRunner) count(agent string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[agent]
}

// mustStart waits for the started channel to be closed, failing the test if
// it does not close within one second.
func mustStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timeout: turn did not start within 1s")
	}
}

// TestTriggerAgent_Idle_Started verifies that TriggerAgent returns Started
// when no turn is running and that the turn actually executes via RunTurn.
func TestTriggerAgent_Idle_Started(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	started, complete := runner.prepareRun("alice")
	defer complete()

	result := lm.TriggerAgent("alice")
	if result != watcher.Started {
		t.Fatalf("expected Started, got %v", result)
	}

	mustStart(t, started)

	if runner.count("alice") != 1 {
		t.Fatalf("expected 1 RunTurn call, got %d", runner.count("alice"))
	}
}

// TestTriggerAgent_Running_Queued verifies that TriggerAgent returns Queued
// when a turn is already running for the same agent.
func TestTriggerAgent_Running_Queued(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	started, complete := runner.prepareRun("alice")
	defer complete()

	result1 := lm.TriggerAgent("alice")
	if result1 != watcher.Started {
		t.Fatalf("first call: expected Started, got %v", result1)
	}
	mustStart(t, started)

	result2 := lm.TriggerAgent("alice")
	if result2 != watcher.Queued {
		t.Fatalf("while running: expected Queued, got %v", result2)
	}
}

// TestTriggerAgent_AlreadyQueued_Dropped verifies that a second trigger while
// a trigger is already queued returns Dropped (at most one queued trigger).
func TestTriggerAgent_AlreadyQueued_Dropped(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	started, complete := runner.prepareRun("alice")
	defer complete()

	lm.TriggerAgent("alice") // → Started
	mustStart(t, started)

	result2 := lm.TriggerAgent("alice") // → Queued
	if result2 != watcher.Queued {
		t.Fatalf("expected Queued, got %v", result2)
	}

	result3 := lm.TriggerAgent("alice") // → Dropped
	if result3 != watcher.Dropped {
		t.Fatalf("expected Dropped, got %v", result3)
	}
}

// TestTriggerAgent_QueuedExecutesAfterCompletion verifies that when a running
// turn completes with a queued trigger, the next turn starts automatically.
func TestTriggerAgent_QueuedExecutesAfterCompletion(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	started1, complete1 := runner.prepareRun("alice")
	started2, complete2 := runner.prepareRun("alice")
	defer complete2()

	lm.TriggerAgent("alice") // → Started
	mustStart(t, started1)

	result2 := lm.TriggerAgent("alice") // → Queued
	if result2 != watcher.Queued {
		t.Fatalf("expected Queued, got %v", result2)
	}

	complete1() // first turn completes; queued trigger should auto-start

	mustStart(t, started2) // second turn must start automatically

	if runner.count("alice") != 2 {
		t.Fatalf("expected 2 RunTurn calls after queued execution, got %d", runner.count("alice"))
	}
}

// TestTriggerAgent_DifferentAgents_Concurrent verifies that triggers for
// different agents are independent: both return Started and run concurrently
// without blocking each other.
func TestTriggerAgent_DifferentAgents_Concurrent(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	startedA, completeA := runner.prepareRun("alice")
	startedB, completeB := runner.prepareRun("bob")
	defer completeA()
	defer completeB()

	resultA := lm.TriggerAgent("alice")
	resultB := lm.TriggerAgent("bob")

	if resultA != watcher.Started {
		t.Errorf("alice: expected Started, got %v", resultA)
	}
	if resultB != watcher.Started {
		t.Errorf("bob: expected Started, got %v", resultB)
	}

	// Both turns must start (they run concurrently — neither blocks the other).
	mustStart(t, startedA)
	mustStart(t, startedB)
}

// TestTriggerAgent_Kyle_Refused verifies that TriggerAgent("kyle") always
// returns Refused and never invokes RunTurn.
func TestTriggerAgent_Kyle_Refused(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	for i := range 3 {
		result := lm.TriggerAgent("kyle")
		if result != watcher.Refused {
			t.Fatalf("call %d: expected Refused for kyle, got %v", i+1, result)
		}
	}

	if runner.count("kyle") != 0 {
		t.Fatalf("kyle must never have RunTurn called; got %d calls", runner.count("kyle"))
	}
}

// TestTriggerAgent_ConcurrentSafety verifies that concurrent calls to
// TriggerAgent from multiple goroutines do not panic or introduce data races.
// Run with -race to exercise the Go race detector.
func TestTriggerAgent_ConcurrentSafety(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	// Pre-queue non-blocking slots so RunTurn returns immediately.
	const N = 50
	for range N {
		_, complete := runner.prepareRun("alice")
		complete() // pre-signal: RunTurn will unblock immediately
	}

	var wg sync.WaitGroup
	for range N {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lm.TriggerAgent("alice")
		}()
	}
	wg.Wait()
	// Reaching here without panic or detected race satisfies the test.
}

// TestTriggerAgent_IdleAfterDrain verifies that after all turns complete the
// agent's state is cleaned up, so a subsequent trigger returns Started rather
// than Queued or Dropped.
func TestTriggerAgent_IdleAfterDrain(t *testing.T) {
	runner := newMockRunner()
	lm := watcher.New(runner)

	// Run a full turn cycle to completion.
	started1, complete1 := runner.prepareRun("alice")

	result1 := lm.TriggerAgent("alice")
	if result1 != watcher.Started {
		t.Fatalf("expected Started, got %v", result1)
	}
	mustStart(t, started1)
	complete1() // turn completes; state must return to Idle

	// Give the goroutine time to finish state cleanup before re-triggering.
	time.Sleep(50 * time.Millisecond)

	// After full drain, TriggerAgent must return Started (state was cleaned up).
	started2, complete2 := runner.prepareRun("alice")
	defer complete2()

	result2 := lm.TriggerAgent("alice")
	if result2 != watcher.Started {
		t.Fatalf("after drain, expected Started (state cleaned up), got %v", result2)
	}
	mustStart(t, started2)

	if runner.count("alice") != 2 {
		t.Fatalf("expected 2 total RunTurn calls, got %d", runner.count("alice"))
	}
}
