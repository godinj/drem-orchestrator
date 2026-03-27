package watcher_test

// trigger_integration_test.go exercises the full channel-based integration
// between the eventbus and the watcher trigger system.
//
// Key architectural invariant under test: the watcher package does NOT import
// eventbus. The integration seam is a <-chan []string: the caller (here the
// test itself, in production the routing layer) publishes agent names after
// creating delivery rows; EventDeliveryTrigger consumes from that channel and
// wakes agents. This file imports both packages to wire the two together.
//
// Three integration scenarios are verified:
//
//  1. Publish event routed to "mike" → delivery created → channel notification
//     → EventDeliveryTrigger → TriggerAgent("mike") called.
//
//  2. Publish event routed to "kyle" → delivery created and persisted → channel
//     notification with ["kyle"] → EventDeliveryTrigger filters kyle → zero
//     TriggerAgent calls.
//
//  3. SafetyTimer fires TriggerAgent("mike") independently — no eventbus
//     interaction required, demonstrating that the two trigger sources are
//     fully decoupled from each other.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/eventbus"
	"github.com/godinj/drem-orchestrator/internal/watcher"
)

// openEventBus opens a real eventbus.Bus backed by a temp-file SQLite database.
// The database file is placed in t.TempDir() so it is removed automatically
// after the test. The Bus is closed via t.Cleanup.
func openEventBus(t *testing.T) *eventbus.Bus {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "csuite_integration.db")
	bus, err := eventbus.New(dbPath)
	if err != nil {
		t.Fatalf("open test eventbus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// TestTriggerIntegration_PublishWakesMike verifies the end-to-end pathway:
//
//	Bus.Publish() → Bus.Deliver(eventID, "mike") → ch ← ["mike"]
//	→ EventDeliveryTrigger → TriggerAgent("mike")
//
// The watcher package is unaware of eventbus; it only observes the channel.
func TestTriggerIntegration_PublishWakesMike(t *testing.T) {
	bus := openEventBus(t)

	ch := make(chan []string, 1)
	mock := newMockAgentTriggerer()
	trigger := watcher.NewEventDeliveryTrigger(ch, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startTrigger(ctx, trigger)

	// Publish an event and create a delivery record for mike.
	ev := &eventbus.Event{
		Type:   "task_status_changed",
		Source: "orchestrator",
		TaskID: "task-1",
	}
	if err := bus.Publish(ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := bus.Deliver(ev.ID, "mike"); err != nil {
		t.Fatalf("Deliver to mike: %v", err)
	}

	// Verify the delivery row exists.
	unacked, err := bus.UnackedDeliveries("mike")
	if err != nil {
		t.Fatalf("UnackedDeliveries: %v", err)
	}
	if len(unacked) != 1 {
		t.Fatalf("expected 1 unacked delivery for mike, got %d", len(unacked))
	}

	// The routing layer would compute the target agent list and push it onto
	// the channel. Here the test does this directly to demonstrate the seam.
	ch <- []string{"mike"}

	waitUntil(t, func() bool { return mock.wasTriggered("mike") }, time.Second)
}

// TestTriggerIntegration_KyleDeliveredNoWake verifies the Kyle exception:
// events ARE delivered to Kyle (delivery rows exist in the DB) but the
// EventDeliveryTrigger never calls TriggerAgent("kyle").
func TestTriggerIntegration_KyleDeliveredNoWake(t *testing.T) {
	bus := openEventBus(t)

	// Use a buffered channel so we can also send a sentinel notification.
	ch := make(chan []string, 2)
	mock := newMockAgentTriggerer()
	trigger := watcher.NewEventDeliveryTrigger(ch, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startTrigger(ctx, trigger)

	// Publish event and create delivery for kyle.
	ev := &eventbus.Event{
		Type:   "task_status_changed",
		Source: "orchestrator",
		TaskID: "task-2",
	}
	if err := bus.Publish(ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := bus.Deliver(ev.ID, "kyle"); err != nil {
		t.Fatalf("Deliver to kyle: %v", err)
	}

	// Delivery record must exist for kyle (event tracking still happens).
	unacked, err := bus.UnackedDeliveries("kyle")
	if err != nil {
		t.Fatalf("UnackedDeliveries for kyle: %v", err)
	}
	if len(unacked) != 1 {
		t.Fatalf("expected 1 unacked delivery for kyle, got %d", len(unacked))
	}

	// Routing layer sends kyle-only notification; trigger must filter it.
	ch <- []string{"kyle"}

	// Use a sentinel notification to confirm the first was fully processed
	// before asserting that kyle was never triggered.
	ch <- []string{"mike"}
	waitUntil(t, func() bool { return mock.wasTriggered("mike") }, time.Second)

	if mock.wasTriggered("kyle") {
		t.Fatal("TriggerAgent must never be called for kyle (Kyle exception)")
	}
}

// TestTriggerIntegration_SafetyTimerIndependent verifies that SafetyTimer
// wakes "mike" at its configured interval without any eventbus involvement.
// This demonstrates that the two trigger sources (event delivery and safety
// timer) are fully decoupled from each other.
func TestTriggerIntegration_SafetyTimerIndependent(t *testing.T) {
	// The triggerer used here is mockTriggerer (from safety_timer_test.go);
	// it provides waitForCall which is convenient for timer-based assertions.
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

	// No eventbus was created or consulted — the timer is entirely self-contained.
}
