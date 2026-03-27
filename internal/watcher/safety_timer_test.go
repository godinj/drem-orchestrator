package watcher_test

import (
	"sync"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// mockTriggerer records TriggerAgent calls for assertion.
type mockTriggerer struct {
	mu    sync.Mutex
	calls []string
	ch    chan string // non-nil when tests want notification of each call
}

func newMockTriggerer() *mockTriggerer {
	return &mockTriggerer{
		ch: make(chan string, 32),
	}
}

func (m *mockTriggerer) TriggerAgent(agent string) watcher.TriggerResult {
	m.mu.Lock()
	m.calls = append(m.calls, agent)
	m.mu.Unlock()
	select {
	case m.ch <- agent:
	default:
	}
	return watcher.Started
}

// callCount returns the number of times TriggerAgent was called for agent.
func (m *mockTriggerer) callCount(agent string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.calls {
		if a == agent {
			n++
		}
	}
	return n
}

// waitForCall blocks until TriggerAgent is called at least once or the
// timeout expires. Returns true if a call was received.
func (m *mockTriggerer) waitForCall(timeout time.Duration) bool {
	select {
	case <-m.ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestSafetyTimer_FiresMike verifies that SafetyTimer calls TriggerAgent("mike")
// at the configured interval.
func TestSafetyTimer_FiresMike(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(10*time.Millisecond, triggerer)
	timer.Start()
	defer timer.Stop()

	if !triggerer.waitForCall(time.Second) {
		t.Fatal("SafetyTimer did not fire TriggerAgent within 1s")
	}

	if triggerer.callCount("mike") == 0 {
		t.Fatal("SafetyTimer fired but did not call TriggerAgent(\"mike\")")
	}
}

// TestSafetyTimer_DefaultInterval verifies that NewSafetyTimer with a zero
// interval defaults to 5 minutes. We cannot wait 5 minutes in a test, so we
// assert the timer does NOT fire within a short window (50ms) when given the
// default interval — confirming the first tick is deferred.
func TestSafetyTimer_DefaultInterval(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(0, triggerer) // 0 → default 5m
	timer.Start()
	defer timer.Stop()

	// With a 5-minute default, no call should arrive within 50ms.
	fired := triggerer.waitForCall(50 * time.Millisecond)
	if fired {
		t.Fatal("SafetyTimer with default 5m interval fired too soon (within 50ms)")
	}
}

// TestSafetyTimer_CustomInterval verifies that a non-zero interval is used
// rather than the 5-minute default. The timer should fire multiple times
// within a short window at the custom interval.
func TestSafetyTimer_CustomInterval(t *testing.T) {
	triggerer := newMockTriggerer()
	interval := 15 * time.Millisecond
	timer := watcher.NewSafetyTimer(interval, triggerer)
	timer.Start()
	defer timer.Stop()

	// Wait for at least 2 fires to confirm repeated ticking.
	if !triggerer.waitForCall(time.Second) {
		t.Fatal("first tick did not fire within 1s")
	}
	if !triggerer.waitForCall(time.Second) {
		t.Fatal("second tick did not fire within 1s")
	}

	count := triggerer.callCount("mike")
	if count < 2 {
		t.Fatalf("expected at least 2 TriggerAgent(\"mike\") calls, got %d", count)
	}
}

// TestSafetyTimer_NoKyle verifies that SafetyTimer never calls
// TriggerAgent("kyle"). Kyle is exempt from lifecycle management by design.
func TestSafetyTimer_NoKyle(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(10*time.Millisecond, triggerer)
	timer.Start()

	// Let the timer fire a few times.
	triggerer.waitForCall(time.Second)
	triggerer.waitForCall(time.Second)
	timer.Stop()

	if triggerer.callCount("kyle") != 0 {
		t.Fatalf("SafetyTimer must never call TriggerAgent(\"kyle\"), got %d calls",
			triggerer.callCount("kyle"))
	}
}

// TestSafetyTimer_StopsCleanly verifies that Stop() halts the timer and that
// TriggerAgent is not called after Stop() returns.
func TestSafetyTimer_StopsCleanly(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(10*time.Millisecond, triggerer)
	timer.Start()

	// Wait for at least one fire, then stop.
	if !triggerer.waitForCall(time.Second) {
		t.Fatal("timer did not fire before Stop")
	}
	timer.Stop()

	// Drain any already-buffered notifications.
	for {
		select {
		case <-triggerer.ch:
		default:
			goto drained
		}
	}
drained:

	countAfterStop := triggerer.callCount("mike")

	// Allow some time; no new calls should arrive.
	time.Sleep(50 * time.Millisecond)

	if triggerer.callCount("mike") != countAfterStop {
		t.Fatal("TriggerAgent(\"mike\") was called after Stop() returned")
	}
}

// TestSafetyTimer_FirstTickAfterInterval verifies that the timer does NOT
// fire immediately on Start() — the first tick must arrive after one full
// interval has elapsed.
func TestSafetyTimer_FirstTickAfterInterval(t *testing.T) {
	triggerer := newMockTriggerer()
	interval := 100 * time.Millisecond
	timer := watcher.NewSafetyTimer(interval, triggerer)

	start := time.Now()
	timer.Start()
	defer timer.Stop()

	if !triggerer.waitForCall(2 * time.Second) {
		t.Fatal("first tick did not arrive within 2s")
	}
	elapsed := time.Since(start)

	// The first tick must not have arrived before the interval elapsed.
	// Allow a 20ms margin below the interval to account for scheduling jitter.
	margin := 20 * time.Millisecond
	if elapsed < interval-margin {
		t.Fatalf("first tick arrived too early: elapsed=%v interval=%v", elapsed, interval)
	}
}

// TestSafetyTimer_OnlyMike verifies that SafetyTimer only ever calls
// TriggerAgent with "mike" — no other agent names.
func TestSafetyTimer_OnlyMike(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(10*time.Millisecond, triggerer)
	timer.Start()

	// Let it fire several times.
	for i := 0; i < 3; i++ {
		triggerer.waitForCall(time.Second)
	}
	timer.Stop()

	triggerer.mu.Lock()
	defer triggerer.mu.Unlock()
	for _, agent := range triggerer.calls {
		if agent != "mike" {
			t.Fatalf("SafetyTimer called TriggerAgent(%q); only \"mike\" is permitted", agent)
		}
	}
}
