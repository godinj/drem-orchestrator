package watcher

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const defaultTimeout = 60 * time.Minute

// LifecycleManager launches agent turns as claude subprocesses, waits for
// completion, parses JSON output for token counts, and records metrics.
//
// Configure ClaudeBin, WorkDir, and Timeout before calling RunTurn.
// ClaudeBin defaults to "claude"; Timeout defaults to 3 minutes.
//
// For the Phase 2 trigger-based API use NewLifecycleManager.
type LifecycleManager struct {
	// ClaudeBin is the path to the claude CLI binary. Defaults to "claude".
	ClaudeBin string
	// WorkDir is the working directory for the subprocess. Empty uses the
	// current process working directory.
	WorkDir string
	// Timeout is the maximum duration a turn may run. Defaults to 3 minutes.
	Timeout time.Duration
	// MetricsStore is used to persist LifecycleResult after each turn.
	MetricsStore *MetricsStore

	// Phase 2 trigger-based fields (set by NewLifecycleManager).
	db     *gorm.DB
	cfg    Config
	runner CommandRunner

	mu      sync.Mutex
	running map[string]bool
	queued  map[string]bool
	wg      sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc
}

// NewLifecycleManagerFromStore creates a LifecycleManager with default settings
// and the provided MetricsStore. For the Phase 2 trigger-based API, use
// NewLifecycleManager instead.
func NewLifecycleManagerFromStore(store *MetricsStore) *LifecycleManager {
	return &LifecycleManager{
		ClaudeBin:    "claude",
		Timeout:      defaultTimeout,
		MetricsStore: store,
	}
}

// RunTurn launches a claude subprocess for the given agent using systemPrompt,
// waits for it to exit, parses the --output-format json response for token
// counts, records a TurnMetric, and returns a LifecycleResult.
//
// Returns ErrKyleException immediately — without launching any subprocess —
// if agent is "kyle".
func (m *LifecycleManager) RunTurn(agent string, systemPrompt string) (*LifecycleResult, error) {
	if agent == "kyle" {
		return nil, ErrKyleException
	}

	timeout := m.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	claudeBin := m.ClaudeBin
	if claudeBin == "" {
		claudeBin = "claude"
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin,
		"-p", "Process your turn",
		"--system-prompt", systemPrompt,
		"--output-format", "json",
	)
	if m.WorkDir != "" {
		cmd.Dir = m.WorkDir
	}
	// Put the subprocess in its own process group so we can kill the entire
	// group (including grandchild processes like `sleep`) on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now()
	runErr := cmd.Run()
	endedAt := time.Now()

	result := &LifecycleResult{
		Agent:     agent,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Duration:  endedAt.Sub(startedAt),
	}

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.ExitStatus = -1
			result.ErrorDetails = "turn killed: timeout exceeded"
		} else if exitErr, ok := runErr.(*exec.ExitError); ok {
			result.ExitStatus = exitErr.ExitCode()
			result.ErrorDetails = strings.TrimSpace(stderr.String())
		} else {
			result.ExitStatus = -1
			result.ErrorDetails = runErr.Error()
		}
	}

	// Parse JSON output for token counts and event metrics; silently degrade on parse failure.
	var resp claudeResponse
	if jsonErr := json.Unmarshal(stdout.Bytes(), &resp); jsonErr == nil {
		result.InputTokens = resp.Usage.InputTokens
		result.OutputTokens = resp.Usage.OutputTokens
		result.EventsProcessed = resp.EventsProcessed
		result.MessagesSent = resp.MessagesSent
	}

	_ = m.MetricsStore.RecordTurn(result)

	return result, nil
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
//   - Started: no turn is active; a new turn subprocess was launched.
//   - Queued: a turn is already running; this trigger is queued and
//     will auto-start as soon as the current turn completes.
//   - Refused: name is not in Config.AllowedAgents; no action taken.
func (m *LifecycleManager) TriggerAgent(name string) TriggerResult {
	if !m.isAllowed(name) {
		return Refused
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running[name] {
		m.queued[name] = true
		return Queued
	}

	m.running[name] = true
	m.wg.Add(1)
	go m.runTurnAsync(name)
	return Started
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
//
// A turn_metrics row is created at turn start with zero EndedAt so the TUI
// can detect running agents via "ended_at = zero-time" queries. The row is
// updated with completion data when the subprocess exits.
//
// If a TurnPrecheck is configured and reports no actionable work for the
// agent, the turn is skipped without launching a subprocess. Queued triggers
// are still honoured so a message arriving between the check and the skip
// is picked up on the next trigger.
func (m *LifecycleManager) runTurnAsync(agent string) {
	defer m.wg.Done()

	// Pre-check: skip if agent has no actionable work.
	if m.cfg.Precheck != nil && !m.cfg.Precheck.HasWork(agent) {
		log.Printf("watcher: skipping %s: no inbox messages and no unacked events", agent)
		m.drainQueue(agent)
		return
	}

	startedAt := time.Now()

	// Record turn start — EndedAt remains zero to signal "in progress".
	metricID := uuid.New()
	_ = m.db.Create(&TurnMetric{
		ID:        metricID,
		Agent:     agent,
		StartedAt: startedAt,
	}).Error

	output, exitCode, _ := m.runner.Run(m.ctx, agent)
	endedAt := time.Now()

	var resp claudeResponse
	_ = json.Unmarshal(output, &resp)

	// Update the existing row with completion data.
	_ = m.db.Model(&TurnMetric{}).Where("id = ?", metricID).Updates(map[string]interface{}{
		"input_tokens":     resp.Usage.InputTokens,
		"output_tokens":    resp.Usage.OutputTokens,
		"exit_status":      exitCode,
		"duration":         endedAt.Sub(startedAt),
		"ended_at":         endedAt,
		"started_at":       startedAt,
		"events_processed": resp.EventsProcessed,
		"messages_sent":    resp.MessagesSent,
	}).Error

	m.drainQueue(agent)
}

// drainQueue checks for a queued trigger and either launches the next turn
// or cleans up the running state for the agent.
func (m *LifecycleManager) drainQueue(agent string) {
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
