package watcher_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// mockAgentTriggerer records TriggerAgent calls and supports per-agent
// blocking so sequential-processing tests can synchronise on turn boundaries.
//
// Call prepareTrigger to obtain a started channel (closed when TriggerAgent
// begins) and a release function (call to unblock TriggerAgent). Calls with
// no prepared slot complete immediately and always return watcher.Started.
type mockAgentTriggerer struct {
	mu    sync.Mutex
	calls []string                      // ordered list of triggered agent names
	queue map[string][]triggerAgentSlot // per-agent blocking queue
}

type triggerAgentSlot struct {
	started chan struct{} // closed when TriggerAgent begins
	done    chan struct{} // close to unblock TriggerAgent
}

func newMockAgentTriggerer() *mockAgentTriggerer {
	return &mockAgentTriggerer{queue: make(map[string][]triggerAgentSlot)}
}

// prepareTrigger enqueues a blocking slot for the next TriggerAgent("agent")
// call. Returns a started channel closed when TriggerAgent begins and a
// release function that unblocks TriggerAgent when called.
func (m *mockAgentTriggerer) prepareTrigger(agent string) (<-chan struct{}, func()) {
	slot := triggerAgentSlot{
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	m.mu.Lock()
	m.queue[agent] = append(m.queue[agent], slot)
	m.mu.Unlock()
	var once sync.Once
	return slot.started, func() { once.Do(func() { close(slot.done) }) }
}

// TriggerAgent implements watcher.AgentTriggerer. It pops the next queued
// slot for agent (if any), signals started, blocks until done is closed,
// then records the call and returns Started.
func (m *mockAgentTriggerer) TriggerAgent(agent string) watcher.TriggerResult {
	m.mu.Lock()
	var slot *triggerAgentSlot
	if len(m.queue[agent]) > 0 {
		s := m.queue[agent][0]
		m.queue[agent] = m.queue[agent][1:]
		slot = &s
	}
	m.mu.Unlock()

	if slot != nil {
		close(slot.started)
		<-slot.done
	}

	m.mu.Lock()
	m.calls = append(m.calls, agent)
	m.mu.Unlock()
	return watcher.Started
}

// triggered returns a snapshot of all agent names that TriggerAgent was
// called with, in the order they completed.
func (m *mockAgentTriggerer) triggered() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.calls))
	copy(result, m.calls)
	return result
}

// wasTriggered reports whether TriggerAgent was called at least once for agent.
func (m *mockAgentTriggerer) wasTriggered(agent string) bool {
	for _, a := range m.triggered() {
		if a == agent {
			return true
		}
	}
	return false
}

// mustTriggerStart waits up to 1s for TriggerAgent to begin for agent.
func mustTriggerStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timeout: TriggerAgent did not begin within 1s")
	}
}

// waitUntil polls cond every 5ms until it returns true or the deadline is reached.
func waitUntil(t *testing.T, cond func() bool, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not satisfied within deadline")
}

// startTrigger runs trigger.Run(ctx) in a goroutine and returns a done channel
// closed when Run returns.
func startTrigger(ctx context.Context, trigger *watcher.EventDeliveryTrigger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.Run(ctx)
	}()
	return done
}

// TestEventDeliveryTrigger_TriggersAgents verifies that a notification
// containing agent names causes TriggerAgent to be called for each.
func TestEventDeliveryTrigger_TriggersAgents(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string, 1)

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	ch <- []string{"alice"}

	waitUntil(t, func() bool { return mock.wasTriggered("alice") }, time.Second)
}

// TestEventDeliveryTrigger_MultipleAgents verifies that all agents in a single
// notification are triggered.
func TestEventDeliveryTrigger_MultipleAgents(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string, 1)

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	ch <- []string{"alice", "bob", "mike"}

	waitUntil(t, func() bool {
		return mock.wasTriggered("alice") &&
			mock.wasTriggered("bob") &&
			mock.wasTriggered("mike")
	}, time.Second)
}

// TestEventDeliveryTrigger_FiltersKyle verifies that "kyle" is never passed to
// TriggerAgent — the Kyle exception applies at the trigger level. Other agents
// in the same notification are still triggered.
func TestEventDeliveryTrigger_FiltersKyle(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string, 1)

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	ch <- []string{"alice", "kyle", "bob"}

	waitUntil(t, func() bool {
		return mock.wasTriggered("alice") && mock.wasTriggered("bob")
	}, time.Second)

	// Give any stray kyle call time to land.
	time.Sleep(20 * time.Millisecond)

	if mock.wasTriggered("kyle") {
		t.Fatal("TriggerAgent must never be called for kyle")
	}
}

// TestEventDeliveryTrigger_KyleOnly verifies that a notification containing
// only "kyle" results in zero TriggerAgent calls.
func TestEventDeliveryTrigger_KyleOnly(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string, 2)

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	ch <- []string{"kyle"}

	// Use a sentinel second notification to know the first was processed.
	ch <- []string{"alice"}
	waitUntil(t, func() bool { return mock.wasTriggered("alice") }, time.Second)

	if mock.wasTriggered("kyle") {
		t.Fatal("TriggerAgent must never be called for kyle")
	}
	if len(mock.triggered()) != 1 {
		t.Fatalf("expected exactly 1 TriggerAgent call (alice), got %v", mock.triggered())
	}
}

// TestEventDeliveryTrigger_EmptyList verifies that an empty notification
// does not cause any TriggerAgent calls.
func TestEventDeliveryTrigger_EmptyList(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string, 2)

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	ch <- []string{}

	// Use a sentinel second notification to know the first was processed.
	ch <- []string{"alice"}
	waitUntil(t, func() bool { return mock.wasTriggered("alice") }, time.Second)

	if len(mock.triggered()) != 1 {
		t.Fatalf("expected exactly 1 TriggerAgent call (alice), got %v", mock.triggered())
	}
}

// TestEventDeliveryTrigger_Sequential verifies that EventDeliveryTrigger
// processes notifications sequentially: the second notification is not
// consumed until all agents in the first notification have been triggered.
func TestEventDeliveryTrigger_Sequential(t *testing.T) {
	mock := newMockAgentTriggerer()

	// Block alice's TriggerAgent call so notification 1 stalls.
	started, release := mock.prepareTrigger("alice")
	defer release()

	ch := make(chan []string, 2) // buffered so both sends don't block
	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	// Send notification 1 (will block in alice's TriggerAgent).
	ch <- []string{"alice"}

	// Wait for alice's TriggerAgent to begin.
	mustTriggerStart(t, started)

	// Send notification 2 while notification 1 is still being processed.
	ch <- []string{"bob"}

	// Bob must NOT be triggered while alice is blocked.
	time.Sleep(20 * time.Millisecond)
	if mock.wasTriggered("bob") {
		t.Fatal("bob was triggered before alice completed — processing is not sequential")
	}

	// Release alice — notification 1 completes.
	release()

	// Now bob should be triggered.
	waitUntil(t, func() bool { return mock.wasTriggered("bob") }, time.Second)
}

// TestEventDeliveryTrigger_ContextCancelled verifies that Run blocks until the
// context is cancelled and then returns cleanly.
func TestEventDeliveryTrigger_ContextCancelled(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string) // unbuffered: Run will block waiting for notifications

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())

	done := startTrigger(ctx, trigger)

	// Run must not return before context is cancelled.
	select {
	case <-done:
		t.Fatal("Run returned before context was cancelled — should block on channel")
	case <-time.After(20 * time.Millisecond):
		// Run is blocking as expected.
	}

	cancel()

	select {
	case <-done:
		// Run returned cleanly after cancellation.
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after context cancellation")
	}
}

// TestEventDeliveryTrigger_MultipleNotifications verifies that Run continues
// processing notifications after the first, triggering all expected agents.
func TestEventDeliveryTrigger_MultipleNotifications(t *testing.T) {
	mock := newMockAgentTriggerer()
	ch := make(chan []string, 3)

	trigger := watcher.NewEventDeliveryTrigger(ch, mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTrigger(ctx, trigger)

	ch <- []string{"alice"}
	ch <- []string{"bob"}
	ch <- []string{"mike"}

	waitUntil(t, func() bool {
		return mock.wasTriggered("alice") &&
			mock.wasTriggered("bob") &&
			mock.wasTriggered("mike")
	}, time.Second)
}
