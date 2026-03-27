package watcher

import (
	"context"
	"time"
)

// TriggerEvent is emitted by a Trigger when an agent should be woken.
// AgentName identifies the agent that should receive a new turn.
// Source identifies the trigger that produced the event (e.g., "inbox-signal").
// Timestamp records when the trigger condition was detected.
type TriggerEvent struct {
	AgentName string
	Source    string
	Timestamp time.Time
}

// AgentWaker is the consumption-site interface for waking agents. It is
// satisfied by a thin adapter over LifecycleManager.TriggerAgent (Phase 4
// task 4). Defined here so trigger consumers do not import the lifecycle
// package directly.
type AgentWaker interface {
	// Wake wakes the named agent, causing the lifecycle manager to schedule
	// a new turn for that agent. Returns an error if the agent is unknown or
	// cannot be woken.
	Wake(agentName string) error
}

// Trigger is the common interface implemented by all trigger sources.
// A trigger monitors an external signal source and emits TriggerEvents on its
// Events channel whenever an agent should be woken.
//
// Usage:
//
//	t := NewInboxSignalTrigger(baseDir, pollInterval)
//	if err := t.Start(ctx); err != nil {
//		log.Fatal(err)
//	}
//	defer t.Stop()
//	for event := range t.Events() {
//		lm.TriggerAgent(event.AgentName)
//	}
type Trigger interface {
	// Start begins monitoring for signals. It is non-blocking — monitoring
	// runs in a background goroutine. Start must be called before Events()
	// delivers any events.
	Start(ctx context.Context) error

	// Stop halts monitoring and closes the Events channel. After Stop
	// returns, the Events channel will be closed and no further events
	// will be emitted. Stop is idempotent.
	Stop() error

	// Events returns the read-only channel on which TriggerEvents are
	// delivered. The channel is closed after Stop returns.
	Events() <-chan TriggerEvent
}

// AgentTriggerer schedules an agent turn and reports the outcome.
// Both Deduplicator and LifecycleManager satisfy this interface.
type AgentTriggerer interface {
	TriggerAgent(agent string) TriggerResult
}

// Compile-time assertion: InboxSignalTrigger must satisfy the Trigger interface.
var _ Trigger = (*InboxSignalTrigger)(nil)
