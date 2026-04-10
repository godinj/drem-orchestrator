package watcher_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// mockFilteredBus is a controllable FilteredEventBus for testing.
type mockFilteredBus struct {
	allUnacked      []watcher.EventInfo
	filteredUnacked []watcher.EventInfo
	ackedIDs        []string
}

func (m *mockFilteredBus) UnackedDeliveries(_ string) ([]watcher.EventInfo, error) {
	return m.allUnacked, nil
}

func (m *mockFilteredBus) UnackedDeliveriesByTypes(_ string, _ []string) ([]watcher.EventInfo, error) {
	return m.filteredUnacked, nil
}

func (m *mockFilteredBus) Ack(_ string, eventIDs []string) error {
	m.ackedIDs = append(m.ackedIDs, eventIDs...)
	return nil
}

// TestFilteredPrecheck_RelevantEventReturnsTrue verifies that HasWork returns
// true when the agent has unacked deliveries of a relevant event type.
func TestFilteredPrecheck_RelevantEventReturnsTrue(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		filteredUnacked: []watcher.EventInfo{{ID: "e1", Type: "task_status_changed"}},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	if !fp.HasWork("mike") {
		t.Error("HasWork(mike) = false, want true (relevant event exists)")
	}
}

// TestFilteredPrecheck_IrrelevantEventReturnsFalse verifies that HasWork
// returns false when the agent only has non-relevant unacked events.
func TestFilteredPrecheck_IrrelevantEventReturnsFalse(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		filteredUnacked: []watcher.EventInfo{}, // no relevant events
		allUnacked:      []watcher.EventInfo{{ID: "e1", Type: "comment_added"}},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	if fp.HasWork("mike") {
		t.Error("HasWork(mike) = true, want false (only non-relevant events)")
	}
}

// TestFilteredPrecheck_AutoAcksNonRelevant verifies that non-relevant events
// are auto-acked so they don't pile up.
func TestFilteredPrecheck_AutoAcksNonRelevant(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		filteredUnacked: []watcher.EventInfo{},
		allUnacked: []watcher.EventInfo{
			{ID: "e1", Type: "comment_added"},
			{ID: "e2", Type: "unrelated_event"},
		},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	fp.HasWork("mike")

	if len(bus.ackedIDs) != 2 {
		t.Fatalf("auto-acked %d events, want 2", len(bus.ackedIDs))
	}
	acked := make(map[string]bool)
	for _, id := range bus.ackedIDs {
		acked[id] = true
	}
	if !acked["e1"] || !acked["e2"] {
		t.Errorf("expected e1 and e2 to be auto-acked, got %v", bus.ackedIDs)
	}
}

// TestFilteredPrecheck_InboxOverridesEventCheck verifies that HasWork returns
// true if the agent has inbox messages, even without relevant events.
func TestFilteredPrecheck_InboxOverridesEventCheck(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "mike", "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "msg.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := &mockFilteredBus{
		filteredUnacked: []watcher.EventInfo{},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	if !fp.HasWork("mike") {
		t.Error("HasWork(mike) = false, want true (inbox has messages)")
	}
}

// TestFilteredPrecheck_UnknownAgentChecksAllEvents verifies that an unknown
// agent (not in the mapping) falls back to checking all event types.
func TestFilteredPrecheck_UnknownAgentChecksAllEvents(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		allUnacked: []watcher.EventInfo{{ID: "e1", Type: "whatever_event"}},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	if !fp.HasWork("unknown-agent") {
		t.Error("HasWork(unknown-agent) = false, want true (fallback to all events)")
	}
}

// TestFilteredPrecheck_UnknownAgentNoEventsReturnsFalse verifies that an
// unknown agent with no events returns false.
func TestFilteredPrecheck_UnknownAgentNoEventsReturnsFalse(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		allUnacked: []watcher.EventInfo{},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	if fp.HasWork("unknown-agent") {
		t.Error("HasWork(unknown-agent) = true, want false (no events)")
	}
}

// TestFilteredPrecheck_NoEventsNoInbox verifies that HasWork returns false
// when the agent has no inbox messages and no relevant events.
func TestFilteredPrecheck_NoEventsNoInbox(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		filteredUnacked: []watcher.EventInfo{},
		allUnacked:      []watcher.EventInfo{},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	if fp.HasWork("mike") {
		t.Error("HasWork(mike) = true, want false (empty inbox, no relevant events)")
	}
}

// TestFilteredPrecheck_DoesNotAckRelevantEvents verifies that relevant
// events are NOT auto-acked (only non-relevant ones are).
func TestFilteredPrecheck_DoesNotAckRelevantEvents(t *testing.T) {
	base := t.TempDir()
	bus := &mockFilteredBus{
		filteredUnacked: []watcher.EventInfo{{ID: "e1", Type: "task_status_changed"}},
		allUnacked: []watcher.EventInfo{
			{ID: "e1", Type: "task_status_changed"},
			{ID: "e2", Type: "comment_added"},
		},
	}

	fp := watcher.NewFilteredPrecheck(base, bus)
	fp.HasWork("mike")

	// Only e2 should be auto-acked, not e1
	if len(bus.ackedIDs) != 1 {
		t.Fatalf("auto-acked %d events, want 1", len(bus.ackedIDs))
	}
	if bus.ackedIDs[0] != "e2" {
		t.Errorf("auto-acked %q, want e2", bus.ackedIDs[0])
	}
}
