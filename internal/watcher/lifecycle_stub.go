package watcher

import (
	"context"

	"gorm.io/gorm"
)

// TriggerResult is returned by TriggerAgent to indicate how the request was handled.
type TriggerResult int

const (
	// TriggerStarted means a new turn subprocess was launched immediately.
	TriggerStarted TriggerResult = iota + 1
	// TriggerQueued means the agent was already running; the trigger is
	// queued and will auto-start after the current turn completes.
	TriggerQueued
	// TriggerRefused means the agent name is not in Config.AllowedAgents.
	// No subprocess is launched and no metrics row is recorded.
	TriggerRefused
)

// Config holds Phase 2 LifecycleManager configuration.
type Config struct {
	// AllowedAgents lists the agent names permitted to run turns.
	// Any agent name not in this list will receive TriggerRefused.
	AllowedAgents []string
}

// CommandRunner abstracts agent turn subprocess execution so tests can inject
// a mock without spawning real processes. Implementations must be safe for
// concurrent use.
type CommandRunner interface {
	Run(ctx context.Context, agent string) (stdout []byte, exitCode int, err error)
}

// NewLifecycleManager creates a LifecycleManager for the Phase 2 trigger API:
// asynchronous turn dispatch, per-agent trigger queuing, allowed-agent
// enforcement, and automatic metrics recording via db.
//
// Stub: returns a zero-value manager. TriggerAgent always returns 0 (none of
// the defined TriggerResult constants), so all integration tests fail on
// assertion rather than compilation.
func NewLifecycleManager(db *gorm.DB, cfg Config, runner CommandRunner) *LifecycleManager {
	return &LifecycleManager{}
}

// TriggerAgent schedules a turn for the named agent and returns immediately:
//   - TriggerStarted: no turn is active; a new turn subprocess was launched.
//   - TriggerQueued: a turn is already running; this trigger is queued and
//     will auto-start as soon as the current turn completes.
//   - TriggerRefused: name is not in Config.AllowedAgents; no action taken.
//
// Stub: always returns 0 (not a valid TriggerResult constant).
func (m *LifecycleManager) TriggerAgent(name string) TriggerResult {
	return 0
}

// Close shuts down the LifecycleManager, waiting for any active or queued
// turns to complete before returning.
//
// Stub: no-op.
func (m *LifecycleManager) Close() {}
