// Package watcher manages the C-Suite agent turn lifecycle, including
// per-agent trigger deduplication with bounded queue depth.
//
// Deduplication prevents parallel turns for the same agent. At most one turn
// runs and at most one additional trigger is queued per agent. Triggers for
// different agents are fully independent.
package watcher

import "sync"

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

type agentState int

const (
	stateRunning       agentState = iota // turn in progress, nothing queued
	stateRunningQueued                   // turn in progress, one trigger queued
)

// kyle is the permanently ineligible agent name.
const kyle = "kyle"

type agentEntry struct {
	state agentState
}

// Deduplicator manages per-agent turn deduplication. Concurrent calls to
// TriggerAgent for the same agent are serialized: at most one turn runs and
// at most one additional trigger is queued. Triggers for different agents are
// independent and may run concurrently.
//
// Use NewDeduplicator to create a Deduplicator.
type Deduplicator struct {
	mu     sync.Mutex
	agents map[string]*agentEntry
	runner TurnRunner
}

// NewDeduplicator creates a Deduplicator backed by runner for turn execution.
func NewDeduplicator(runner TurnRunner) *Deduplicator {
	return &Deduplicator{
		agents: make(map[string]*agentEntry),
		runner: runner,
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
	if agent == kyle {
		return Refused
	}

	d.mu.Lock()
	entry, ok := d.agents[agent]
	if !ok {
		d.agents[agent] = &agentEntry{state: stateRunning}
		d.mu.Unlock()
		go d.runLoop(agent)
		return Started
	}
	if entry.state == stateRunning {
		entry.state = stateRunningQueued
		d.mu.Unlock()
		return Queued
	}
	d.mu.Unlock()
	return Dropped
}

// runLoop executes turns for agent, re-running if a queued trigger exists.
func (d *Deduplicator) runLoop(agent string) {
	for {
		d.runner.RunTurn(agent)

		d.mu.Lock()
		entry := d.agents[agent]
		if entry.state == stateRunningQueued {
			entry.state = stateRunning
			d.mu.Unlock()
			// loop: run the queued turn
			continue
		}
		// state == stateRunning, no queued trigger — clean up
		delete(d.agents, agent)
		d.mu.Unlock()
		return
	}
}
