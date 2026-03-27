package watcher

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
func NewLifecycleManager(db *gorm.DB, cfg Config, runner CommandRunner) *LifecycleManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &LifecycleManager{
		db:      db,
		cfg:     cfg,
		runner:  runner,
		running: make(map[string]bool),
		queued:  make(map[string]bool),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// TriggerAgent schedules a turn for the named agent and returns immediately:
//   - TriggerStarted: no turn is active; a new turn subprocess was launched.
//   - TriggerQueued: a turn is already running; this trigger is queued and
//     will auto-start as soon as the current turn completes.
//   - TriggerRefused: name is not in Config.AllowedAgents; no action taken.
func (m *LifecycleManager) TriggerAgent(name string) TriggerResult {
	if !m.isAllowed(name) {
		return TriggerRefused
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running[name] {
		m.queued[name] = true
		return TriggerQueued
	}

	m.running[name] = true
	m.wg.Add(1)
	go m.runTurnAsync(name)
	return TriggerStarted
}

// Close shuts down the LifecycleManager, waiting for any active or queued
// turns to complete before returning.
func (m *LifecycleManager) Close() {
	m.wg.Wait()
	m.cancel()
}

// isAllowed reports whether name is in cfg.AllowedAgents.
func (m *LifecycleManager) isAllowed(name string) bool {
	for _, a := range m.cfg.AllowedAgents {
		if a == name {
			return true
		}
	}
	return false
}

// runTurnAsync executes one agent turn, persists metrics, then auto-starts a
// queued trigger if one was registered during this turn.
func (m *LifecycleManager) runTurnAsync(agent string) {
	defer m.wg.Done()

	startedAt := time.Now()
	output, exitCode, _ := m.runner.Run(m.ctx, agent)
	endedAt := time.Now()

	var resp claudeResponse
	_ = json.Unmarshal(output, &resp)

	metric := TurnMetric{
		ID:              uuid.New(),
		Agent:           agent,
		InputTokens:     resp.Usage.InputTokens,
		OutputTokens:    resp.Usage.OutputTokens,
		ExitStatus:      exitCode,
		Duration:        endedAt.Sub(startedAt),
		StartedAt:       startedAt,
		EndedAt:         endedAt,
		EventsProcessed: resp.EventsProcessed,
		MessagesSent:    resp.MessagesSent,
	}
	_ = m.db.Create(&metric).Error

	m.mu.Lock()
	wasQueued := m.queued[agent]
	delete(m.queued, agent)
	if wasQueued {
		// Keep running[agent] = true and launch a new goroutine.
		m.wg.Add(1)
		m.mu.Unlock()
		go m.runTurnAsync(agent)
	} else {
		delete(m.running, agent)
		m.mu.Unlock()
	}
}
