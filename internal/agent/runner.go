// Package agent manages Claude Code agent lifecycles.
//
// Headless agents (planners, coders, reviewers, fixers) run as direct
// subprocesses. Interactive sessions (supervisors, shells) still use the
// TmuxSessionManager adapter (see tmux_adapter.go) — the host-mode tmux
// package is not imported here while the containerization migration in
// docs/containerization/remaining-work.md is in flight.
//
// Container-based agents route through Manager.Spawn/Teardown (spawn.go,
// teardown.go) and Manager.Attach/RespawnIfDashboardExited (attach.go).
//
// Each agent runs in its own git worktree with full visibility and persistence
// independent of the dashboard lifecycle.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
)

const (
	// maxLogTailBytes is the maximum number of bytes to read from the tail of
	// an agent's output log file when capturing output.
	maxLogTailBytes = 64 * 1024

	// shortIDLen is the number of characters used from a UUID for display purposes.
	shortIDLen = 4

	// maxDisplayNameLen is the maximum length of task/parent titles in agent display names.
	maxDisplayNameLen = 30

	// idleNotifyTimeoutSecs is the hook timeout (in seconds) for the idle_prompt notification command.
	idleNotifyTimeoutSecs = 5

	// sendExitGracePeriod is how long to wait after SendExit before killing.
	sendExitGracePeriod = 30 * time.Second

	// stopGracePeriod is how long StopAgent waits after SendExit before Kill.
	stopGracePeriod = 5 * time.Second
)

// ExitInfo holds structured information about why an agent exited, populated
// from Claude Code hook JSONL entries when available.
type ExitInfo struct {
	ExitReason  string `json:"exit_reason"`  // e.g. "success", "error", "context_limit", "user_interrupt"
	LastTool    string `json:"last_tool"`    // last tool call before exit
	ExitSummary string `json:"exit_summary"` // summary of work done
}

// Completion records the result of an agent process exit.
type Completion struct {
	AgentID    uuid.UUID
	ReturnCode int
	ExitInfo   *ExitInfo // nil when hook didn't fire or no log available
}

// RunningAgent tracks an active agent process.
type RunningAgent struct {
	AgentID      uuid.UUID
	TaskID       uuid.UUID
	WorktreePath string
	Branch       string
	TmuxSession  string
	Process      *AgentProcess      // subprocess handle; nil for supervisors
	LogPath      string             // path to agent-output.log
	Provider     model.ProviderType // provider backend ("claude" or "opencode")
	StartedAt    time.Time
	ContextUsage *ctxmon.Usage      // latest context window usage; nil until first read
	ActivityMon  *agentmon.Monitor  // activity monitor; nil until started
	Activity     *agentmon.Activity // latest activity snapshot; nil until first scan
	cancel       context.CancelFunc // cancels the monitor and heartbeat goroutines
}

// MetricsRecorder records time-series metric samples for running agents.
// Implementations must be safe for concurrent use.
type MetricsRecorder interface {
	Record(agentID uuid.UUID, name string, value float64, labels map[string]string) error
}

// Runner manages Claude Code agent lifecycles. Headless agents run as
// subprocesses; supervisors and shells still use tmux via the
// TmuxSessionManager adapter (see tmux_adapter.go).
type Runner struct {
	db                    *gorm.DB
	startProcess          ProcessStarter     // creates subprocess; overridable for tests
	tmuxMgr               TmuxSessionManager // for supervisors/shells only
	tmuxSessionName       string             // dashboard session name prefix
	worktree              WorktreeManager
	claudeBin             string
	openCodeBin           string
	codexBin              string
	maxConcurrent         int
	agentConfigs          func(model.AgentType) model.AgentCLIConfig // per-type CLI flags
	metricsRecorder       MetricsRecorder                            // optional; nil means no time-series recording
	openCodeContextWindow int                                        // vLLM context window size; 0 → ctxmon default

	mu              sync.Mutex
	running         map[uuid.UUID]*RunningAgent
	completions     chan Completion
	semaphore       chan struct{}   // buffered channel of size maxConcurrent
	dispatchLimiter dispatchLimiter // optional rate limiter; nil means no limit
}

// AgentConfig returns the resolved CLI configuration for a given agent type.
// This allows the orchestrator to query what model/provider a coder will use
// without spawning it, e.g. for model-aware planner prompts.
func (r *Runner) AgentConfig(agentType model.AgentType) model.AgentCLIConfig {
	return r.agentConfigs(agentType)
}

// SetMetricsRecorder injects a MetricsRecorder for time-series metric
// collection during agent runs. When set, contextMonitorLoop records five
// metric samples per tick: context_pct, cost_usd, token_input, token_output,
// and tool_count.
func (r *Runner) SetMetricsRecorder(mr MetricsRecorder) {
	r.metricsRecorder = mr
}

// SetOpenCodeContextWindow sets the context window size (in tokens) used for
// computing context usage percentages for OpenCode agents. Pass 0 to use
// the default (ctxmon.DefaultOpenCodeContextWindow = 43904).
func (r *Runner) SetOpenCodeContextWindow(size int) {
	r.openCodeContextWindow = size
}

// NewRunner creates an agent Runner. Headless agents are started via
// StartAgentProcess; supervisors and shells use the tmux session manager.
// agentConfigs maps agent types to CLI flags; pass nil for default behavior
// (effort=medium, no model override).
//
// tm is a TmuxSessionManager: the host-mode tmux manager satisfies it, but
// this file imports the interface, not any concrete type. Pass nil in tests
// or in containerized configurations where supervisor tmux sessions are
// not required.
//
// wt is a WorktreeManager: the host-mode worktree manager satisfies it.
// Pass nil only in tests that never call SpawnAgent.
func NewRunner(db *gorm.DB, tm TmuxSessionManager, wt WorktreeManager, claudeBin, openCodeBin string, maxConcurrent int, agentConfigs func(model.AgentType) model.AgentCLIConfig) *Runner {
	if agentConfigs == nil {
		agentConfigs = func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Effort: "medium"}
		}
	}
	return &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxMgr:         tm,
		tmuxSessionName: dashboardSessionName(tm),
		worktree:        wt,
		claudeBin:       claudeBin,
		openCodeBin:     openCodeBin,
		codexBin:        "codex",
		maxConcurrent:   maxConcurrent,
		agentConfigs:    agentConfigs,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, maxConcurrent),
		semaphore:       make(chan struct{}, maxConcurrent),
	}
}

// SetCodexBin overrides the Codex CLI binary path used by provider=codex.
// Empty values keep the default "codex".
func (r *Runner) SetCodexBin(codexBin string) {
	if codexBin != "" {
		r.codexBin = codexBin
	}
}

// AgentTypeLabel returns a human-readable label for the given agent type.
func AgentTypeLabel(at model.AgentType) string {
	switch at {
	case model.AgentPlanner:
		return "plan"
	case model.AgentCoder:
		return "code"
	case model.AgentResearcher:
		return "research"
	case model.AgentReviewer:
		return "review"
	case model.AgentFixer:
		return "fix"
	case model.AgentPrep:
		return "prep"
	default:
		return string(at)
	}
}

// TruncateTitle truncates s to maxLen characters, appending "..." if truncated.
func TruncateTitle(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// SanitizeSessionName removes characters not valid in tmux session names.
func SanitizeSessionName(s string) string {
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, ":", "-")
	return s
}

// loadParentTitle fetches the title of a task's parent. Returns "unknown" on failure.
func (r *Runner) loadParentTitle(task *model.Task) string {
	if task.ParentTaskID == nil {
		return "unknown"
	}
	var parent model.Task
	if err := r.db.Select("title").First(&parent, "id = ?", *task.ParentTaskID).Error; err != nil {
		return "unknown"
	}
	return parent.Title
}

// buildAgentNames constructs the human-readable Name and the tmux session name
// for an agent. Planners show "plan - <task-title>"; coders and researchers
// show "code - <parent> > <subtask>". The tmux session nests under the
// dashboard via "/" separator.
func (r *Runner) buildAgentNames(task *model.Task, agentType model.AgentType, agentID uuid.UUID) (name, session string) {
	label := AgentTypeLabel(agentType)
	shortID := agentID.String()[:shortIDLen]

	if agentType == model.AgentPlanner {
		title := strings.ReplaceAll(task.Title, "/", "-")
		title = TruncateTitle(title, maxDisplayNameLen)
		name = fmt.Sprintf("%s - %s", label, title)
	} else {
		parentTitle := strings.ReplaceAll(r.loadParentTitle(task), "/", "-")
		parentTitle = TruncateTitle(parentTitle, maxDisplayNameLen)
		subtaskTitle := strings.ReplaceAll(task.Title, "/", "-")
		subtaskTitle = TruncateTitle(subtaskTitle, maxDisplayNameLen)
		name = fmt.Sprintf("%s - %s > %s", label, parentTitle, subtaskTitle)
	}

	session = SanitizeSessionName(fmt.Sprintf("%s/%s %s", r.tmuxSessionName, name, shortID))
	return name, session
}

// TmuxSessionName returns the dashboard tmux session name used as a namespace
// prefix for agent and supervisor sessions.
func (r *Runner) TmuxSessionName() string {
	return r.tmuxSessionName
}

// TmuxManager returns the underlying tmux session manager for supervisor/shell
// operations. Returns nil if no tmux manager is configured (test code).
// The returned value is the TmuxSessionManager interface rather than a
// concrete type, letting callers stay off the host-mode tmux package.
func (r *Runner) TmuxManager() TmuxSessionManager {
	return r.tmuxMgr
}

// ClaudeBin returns the path to the Claude binary.
func (r *Runner) ClaudeBin() string {
	return r.claudeBin
}

// CanSpawn returns whether there is capacity for another agent.
// When a dispatchLimiter is set, it must also permit the dispatch.
func (r *Runner) CanSpawn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check in-memory running agents first
	if len(r.running) >= r.maxConcurrent {
		return false
	}

	// Also check database for WORKING agents to account for agents not yet
	// tracked in memory (e.g., those created outside the runner)
	if r.db != nil {
		var count int64
		if err := r.db.Model(&model.Agent{}).Where("status = ?", model.AgentWorking).Count(&count).Error; err == nil {
			if count >= int64(r.maxConcurrent) {
				return false
			}
		}
	}

	if r.dispatchLimiter != nil {
		ok, _ := r.dispatchLimiter.Allow()
		return ok
	}
	return true
}

// SpawnAgent creates a new worktree, DB record, prompt file, tmux session, and
// starts monitoring. It returns the newly created Agent.
func (r *Runner) SpawnAgent(task *model.Task, featureName string, agentType model.AgentType, prompt string) (*model.Agent, error) {
	wtInfo, err := r.worktree.CreateAgentWorktree(featureName)
	if err != nil {
		return nil, fmt.Errorf("spawn agent: create worktree: %w", err)
	}

	// Generate repo map in the new worktree (non-blocking on failure).
	r.worktree.GenerateRepoMapAsync(wtInfo.Path)

	return r.spawnNewAgent(task, wtInfo.Path, wtInfo.Branch, agentType, prompt)
}

// SpawnAgentInWorktree starts an agent in an existing worktree without creating
// a new branch. Used for reviewer and fixer agents that operate on the
// integration branch directly. Returns the newly created Agent.
func (r *Runner) SpawnAgentInWorktree(task *model.Task, worktreePath string, agentType model.AgentType, prompt string) (*model.Agent, error) {
	return r.spawnNewAgent(task, worktreePath, "", agentType, prompt)
}

// spawnNewAgent is the shared implementation for SpawnAgent and SpawnAgentInWorktree.
// It acquires the concurrency semaphore, creates the DB record, starts the agent
// process, and begins monitoring. The dbBranch is stored in the Agent record; if
// empty, the runtime branch is derived from the worktree path.
func (r *Runner) spawnNewAgent(task *model.Task, worktreePath, dbBranch string, agentType model.AgentType, prompt string) (*model.Agent, error) {
	// Acquire semaphore (non-blocking).
	select {
	case r.semaphore <- struct{}{}:
	default:
		return nil, fmt.Errorf("spawn agent: max concurrent agents (%d) reached", r.maxConcurrent)
	}

	// On any error below, release the semaphore.
	success := false
	defer func() {
		if !success {
			<-r.semaphore
		}
	}()

	// Runtime branch for startAgent: use dbBranch if set, otherwise derive from path.
	branch := dbBranch
	if branch == "" {
		branch = filepath.Base(worktreePath)
	}

	// Create Agent DB record.
	agentID := uuid.New()
	agentName, sessionName := r.buildAgentNames(task, agentType, agentID)
	now := time.Now()

	// Resolve agent config for this agent type and populate ModelID and Effort
	// BEFORE creating the DB record, so these fields are present from the start.
	cliConfig := r.agentConfigs(agentType)

	agent := &model.Agent{
		ID:             agentID,
		ProjectID:      task.ProjectID,
		AgentType:      agentType,
		Name:           agentName,
		Status:         model.AgentWorking,
		CurrentTaskID:  &task.ID,
		WorktreePath:   worktreePath,
		WorktreeBranch: dbBranch,
		TmuxSession:    sessionName,
		ModelID:        cliConfig.Model,
		Effort:         cliConfig.Effort,
		Provider:       string(cliConfig.EffectiveProvider()),
		HeartbeatAt:    &now,
	}
	if err := r.db.Create(agent).Error; err != nil {
		return nil, fmt.Errorf("spawn agent: create db record: %w", err)
	}

	// Write prompt, start subprocess, begin monitoring.
	if err := r.startAgent(agent.ID, task.ID, worktreePath, branch, sessionName, prompt, agentType); err != nil {
		r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		return nil, fmt.Errorf("spawn agent: start: %w", err)
	}

	// Store PID and log path in agent Config for orphan cleanup and log retrieval.
	r.mu.Lock()
	ra := r.running[agent.ID]
	r.mu.Unlock()
	if ra != nil && ra.Process != nil {
		cfg := model.JSONField{
			"pid": ra.Process.Pid(),
		}
		if ra.LogPath != "" {
			cfg["log_path"] = ra.LogPath
		}
		r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("config", cfg)
	}

	// Create activity monitor with estimated files from task context.
	var estimatedFiles []string
	if task.Context != nil {
		if raw, ok := task.Context["estimated_files"]; ok {
			switch v := raw.(type) {
			case []any:
				for _, f := range v {
					if s, ok := f.(string); ok {
						estimatedFiles = append(estimatedFiles, s)
					}
				}
			case []string:
				estimatedFiles = v
			}
		}
	}
	if ra != nil {
		r.mu.Lock()
		ra.ActivityMon = agentmon.NewMonitor(worktreePath, estimatedFiles)
		r.mu.Unlock()
	}

	slog.Info("agent spawned",
		"agent_id", agent.ID,
		"task", task.Title,
		"agent_type", agentType,
		"worktree", worktreePath,
		"prompt_bytes", len(prompt),
	)

	success = true

	// Record the dispatch in the rate limiter's sliding window.
	r.mu.Lock()
	dl := r.dispatchLimiter
	r.mu.Unlock()
	if dl != nil {
		dl.Record()
	}

	return agent, nil
}

// startAgent dispatches to the provider-specific agent start method based on
// the resolved CLI config for the agent type.
func (r *Runner) startAgent(agentID, taskID uuid.UUID, worktreePath, branch, sessionName, prompt string, agentType model.AgentType) error {
	cliConfig := r.agentConfigs(agentType)
	switch cliConfig.EffectiveProvider() {
	case model.ProviderOpenCode:
		return r.startOpenCodeAgent(agentID, taskID, worktreePath, branch, sessionName, prompt, agentType)
	case model.ProviderCodex:
		return r.startCodexAgent(agentID, taskID, worktreePath, branch, sessionName, prompt, agentType)
	}
	return r.startClaudeAgent(agentID, taskID, worktreePath, branch, sessionName, prompt, agentType)
}

// StopAgent performs a graceful shutdown of an agent: cancels goroutines,
// stops the subprocess, updates the DB, and releases capacity.
func (r *Runner) StopAgent(agentID uuid.UUID) error {
	r.mu.Lock()
	ra, ok := r.running[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("stop agent: agent %s is not running", agentID)
	}
	delete(r.running, agentID)
	r.mu.Unlock()

	// Cancel monitor and heartbeat goroutines.
	ra.cancel()

	// Gracefully stop the subprocess.
	if ra.Process != nil {
		ra.Process.SendExit()
		// Wait briefly for graceful exit.
		timer := time.NewTimer(stopGracePeriod)
		select {
		case <-ra.Process.done:
			timer.Stop()
		case <-timer.C:
			ra.Process.Kill()
		}
	}

	// Update agent DB status to DEAD.
	if err := r.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", model.AgentDead).Error; err != nil {
		return fmt.Errorf("stop agent: update db: %w", err)
	}

	// Release semaphore.
	<-r.semaphore

	return nil
}

// GetAgentOutput reads the agent's output log file. For running agents it
// reads from the in-memory log path; for finished agents it looks up the
// agent from the DB and reads from the worktree's log file.
func (r *Runner) GetAgentOutput(agentID uuid.UUID) (string, error) {
	r.mu.Lock()
	ra, ok := r.running[agentID]
	r.mu.Unlock()

	var logPath string
	if ok {
		logPath = ra.LogPath
	} else {
		var agent model.Agent
		if err := r.db.First(&agent, "id = ?", agentID).Error; err != nil {
			return "", fmt.Errorf("get agent output: agent %s not found: %w", agentID, err)
		}
		if agent.WorktreePath == "" {
			return "", nil
		}
		if agent.Provider == string(model.ProviderOpenCode) || agent.Provider == string(model.ProviderCodex) {
			// Check Config for per-agent log path (stored at spawn time);
			// fall back to legacy shared path for older agents.
			if agent.Config != nil {
				if lp, ok := agent.Config["log_path"].(string); ok && lp != "" {
					logPath = lp
				}
			}
			if logPath == "" {
				if agent.Provider == string(model.ProviderCodex) {
					logPath = filepath.Join(agent.WorktreePath, ".codex", "agent-output.jsonl")
				} else {
					logPath = filepath.Join(agent.WorktreePath, ".opencode", "agent-output.jsonl")
				}
			}
		} else {
			logPath = filepath.Join(agent.WorktreePath, ".claude", "agent-output.log")
		}
	}

	return readLogTail(logPath, maxLogTailBytes)
}

// readLogTail reads the last maxBytes from a file and returns them as a string.
func readLogTail(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read log: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read log: stat: %w", err)
	}

	offset := info.Size() - maxBytes
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return "", fmt.Errorf("read log: seek: %w", err)
		}
	}

	data := make([]byte, info.Size()-offset)
	n, err := f.Read(data)
	if err != nil && err.Error() != "EOF" {
		return "", fmt.Errorf("read log: read: %w", err)
	}
	return string(data[:n]), nil
}

// KillStaleProcess kills a stale agent by PID from the agent's Config.
// Used by the orchestrator when StopAgent fails (agent not in running map).
func (r *Runner) KillStaleProcess(ag *model.Agent) {
	if ag.Config != nil {
		if pidVal, ok := ag.Config["pid"].(float64); ok && pidVal > 0 {
			pid := int(pidVal)
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Kill()
			}
		}
	}
}

// GetRunningAgents returns a copy of all currently running agent entries.
func (r *Runner) GetRunningAgents() []RunningAgent {
	r.mu.Lock()
	defer r.mu.Unlock()

	agents := make([]RunningAgent, 0, len(r.running))
	for _, ra := range r.running {
		agents = append(agents, RunningAgent{
			AgentID:      ra.AgentID,
			TaskID:       ra.TaskID,
			WorktreePath: ra.WorktreePath,
			Branch:       ra.Branch,
			TmuxSession:  ra.TmuxSession,
			Process:      ra.Process,
			LogPath:      ra.LogPath,
			Provider:     ra.Provider,
			StartedAt:    ra.StartedAt,
			ContextUsage: ra.ContextUsage,
			Activity:     ra.Activity,
		})
	}
	return agents
}

// SendCompletion enqueues a completion for the next tick's drain pass.
// Used by the orchestrator's async direct-tool-agent goroutines to funnel
// results back through the standard processAgentResult flow.
func (r *Runner) SendCompletion(c Completion) {
	r.completions <- c
}

// DrainCompletions performs a non-blocking drain of all pending completions
// from the completions channel.
func (r *Runner) DrainCompletions() []Completion {
	var results []Completion
	for {
		select {
		case c := <-r.completions:
			results = append(results, c)
		default:
			return results
		}
	}
}

// CleanupStaleAgents finds agents in the DB with status=WORKING whose heartbeat
// is older than the given timeout and stops them.
func (r *Runner) CleanupStaleAgents(timeout time.Duration) error {
	cutoff := time.Now().Add(-timeout)

	var staleAgents []model.Agent
	err := r.db.Where("status = ? AND (heartbeat_at < ? OR heartbeat_at IS NULL)", model.AgentWorking, cutoff).Find(&staleAgents).Error
	if err != nil {
		return fmt.Errorf("cleanup stale agents: query: %w", err)
	}

	for _, agent := range staleAgents {
		// If the agent is in our running map, stop it normally.
		r.mu.Lock()
		_, isRunning := r.running[agent.ID]
		r.mu.Unlock()

		if isRunning {
			if err := r.StopAgent(agent.ID); err != nil {
				// Best effort: continue to next agent.
				continue
			}
		} else {
			// Not in our running map — kill by PID and update DB.
			r.KillStaleProcess(&agent)
			r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		}
	}

	return nil
}

// ReapOrphanedSessions kills tmux sessions (supervisors) that are not
// associated with any active work. Headless agents no longer create tmux
// sessions, so this only cleans up supervisor/shell sessions.
func (r *Runner) ReapOrphanedSessions() (int, error) {
	if r.tmuxMgr == nil {
		return 0, nil
	}

	sessions, err := r.tmuxMgr.ListAgentSessions()
	if err != nil {
		return 0, err
	}
	if len(sessions) == 0 {
		return 0, nil
	}

	reaped := 0
	for _, sess := range sessions {
		// Only reap sessions whose process has exited.
		alive, err := r.tmuxMgr.IsAgentSessionAlive(sess)
		if err != nil || alive {
			continue
		}
		_ = r.tmuxMgr.KillAgentSession(sess)
		reaped++
	}

	return reaped, nil
}
