// Package watcher manages the C-Suite agent turn lifecycle, including
// per-agent trigger deduplication with bounded queue depth.
//
// Deduplication prevents parallel turns for the same agent. At most one turn
// runs and at most one additional trigger is queued per agent. Triggers for
// different agents are fully independent.
package watcher

// TriggerResult describes the outcome of a TriggerAgent call.
//
// The values form a priority order reflecting the agent's state at call time:
//
//	Started  — no turn was running; a new turn was launched
//	Queued   — a turn was running; this trigger will fire after it completes
//	Dropped  — a queued trigger already exists; this one is discarded
//	Refused  — the agent is permanently ineligible (kyle exception)
type TriggerResult int

const (
	// Started indicates a turn was launched immediately.
	Started TriggerResult = iota
	// Queued indicates the trigger was accepted and will run after the
	// current turn completes.
	Queued
	// Dropped indicates a queued trigger already exists; this one is
	// discarded. The existing queued trigger will pick up all pending work.
	Dropped
	// Refused indicates the agent is permanently ineligible for turns.
	// Currently only "kyle" triggers this result.
	Refused
)

// TurnRunner executes a single agent turn synchronously. The call blocks
// until the turn completes or fails. Implementations must be safe for
// concurrent calls with different agent names.
type TurnRunner interface {
	RunTurn(agent string)
}

// LifecycleManager manages per-agent turn deduplication. Concurrent calls to
// TriggerAgent for the same agent are serialized: at most one turn runs and
// at most one additional trigger is queued. Triggers for different agents are
// independent and may run concurrently.
//
// Use New to create a LifecycleManager.
type LifecycleManager struct{}

// New creates a LifecycleManager backed by runner for turn execution.
func New(runner TurnRunner) *LifecycleManager {
	return &LifecycleManager{}
}

// TriggerAgent requests a turn for agent and returns the outcome.
//
// Behavior:
//   - If agent is "kyle", returns Refused immediately without running a turn.
//   - If no turn is running, launches a turn in a goroutine and returns Started.
//   - If a turn is running and nothing is queued, queues a trigger and returns Queued.
//   - If a turn is running and a trigger is already queued, returns Dropped.
//
// When a running turn completes, any queued trigger fires automatically.
// After all turns drain, the agent's state is cleaned up.
//
// TriggerAgent is safe for concurrent use by multiple goroutines.
func (lm *LifecycleManager) TriggerAgent(agent string) TriggerResult {
	return Dropped // stub — replaced by real implementation
}
