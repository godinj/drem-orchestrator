// Package watcher manages the C-Suite agent turn lifecycle.
package watcher

// TriggerResult describes the outcome of a TriggerAgent call.
//
// The values form a priority order reflecting the agent's state at call time:
//
//	Started  — no turn was running; a new turn was launched
//	Queued   — a turn was running; this trigger will fire after it completes
//	Dropped  — a queued trigger already exists; this one is discarded
//	Refused  — the agent is permanently ineligible (kyle exception)
//	Cooldown — the agent is in a post-turn cooldown period; trigger is queued
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
	// Cooldown indicates the agent is in a post-turn cooldown period; trigger is queued.
	Cooldown
)

// TurnRunner executes a single agent turn synchronously. The call blocks until
// the turn completes or fails. Implementations must be safe for concurrent
// calls with different agent names.
type TurnRunner interface {
	RunTurn(agent string)
}

// kyle is the permanently ineligible agent name.
const kyle = "kyle"

// Deduplicator manages per-agent turn deduplication. Concurrent calls to
// TriggerAgent for the same agent are serialized: at most one turn runs and
// at most one additional trigger is queued. Triggers for different agents are
// independent and may run concurrently.
//
// Use NewDeduplicator to create a Deduplicator.
type Deduplicator struct {
	scheduler *turnScheduler
	runner    TurnRunner
}

// NewDeduplicator creates a Deduplicator backed by runner for turn execution.
func NewDeduplicator(runner TurnRunner) *Deduplicator {
	return &Deduplicator{
		scheduler: newTurnScheduler(nil, 0, Dropped),
		runner:    runner,
	}
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
func (d *Deduplicator) TriggerAgent(agent string) TriggerResult {
	result, action := d.scheduler.Trigger(agent)
	d.launch(agent, action)
	return result
}

func (d *Deduplicator) launch(agent string, action schedulerAction) {
	if action.kind != schedulerStartNow {
		return
	}
	go d.run(agent)
}

func (d *Deduplicator) run(agent string) {
	d.runner.RunTurn(agent)
	d.launch(agent, d.scheduler.Complete(agent))
}
