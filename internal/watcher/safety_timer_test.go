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

// TestSafetyTimer_Disabled verifies that the safety timer never fires
// TriggerAgent now that the run loop is a no-op.
func TestSafetyTimer_Disabled(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(10*time.Millisecond, triggerer)
	timer.Start()
	defer timer.Stop()

	// Wait long enough that the old timer would have fired many times.
	fired := triggerer.waitForCall(200 * time.Millisecond)
	if fired {
		t.Fatal("SafetyTimer should be disabled but TriggerAgent was called")
	}

	if triggerer.callCount("mike") != 0 {
		t.Fatalf("expected 0 TriggerAgent(\"mike\") calls, got %d", triggerer.callCount("mike"))
	}
}

// TestSafetyTimer_StopsCleanly verifies that Stop() returns promptly and
// does not block or panic even though the timer is a no-op.
func TestSafetyTimer_StopsCleanly(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(10*time.Millisecond, triggerer)
	timer.Start()

	done := make(chan struct{})
	go func() {
		timer.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned — good.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}

	if triggerer.callCount("mike") != 0 {
		t.Fatal("TriggerAgent(\"mike\") was called but timer should be disabled")
	}
}

// TestSafetyTimer_DefaultIntervalAccepted verifies that NewSafetyTimer
// with a zero interval does not panic (the default is still assigned
// internally even though run is a no-op).
func TestSafetyTimer_DefaultIntervalAccepted(t *testing.T) {
	triggerer := newMockTriggerer()
	timer := watcher.NewSafetyTimer(0, triggerer)
	timer.Start()
	timer.Stop()

	if triggerer.callCount("mike") != 0 {
		t.Fatal("no calls expected from disabled safety timer")
	}
}
