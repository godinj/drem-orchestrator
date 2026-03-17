// Package agent manages Claude Code agent lifecycles.
//
// Headless agents (planners, coders, reviewers, fixers) run as direct
// subprocesses. Interactive sessions (supervisors, shells) still use tmux.
// Each agent runs in its own git worktree with full visibility and persistence
// independent of the dashboard lifecycle.
package agent

import (
	"context"
	"encoding/json"
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
	"github.com/godinj/drem-orchestrator/internal/tmux"
	"github.com/godinj/drem-orchestrator/internal/worktree"
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

// Completion records the result of an agent process exit.
type Completion struct {
	AgentID    uuid.UUID
	ReturnCode int
}

// RunningAgent tracks an active agent process.
type RunningAgent struct {
	AgentID      uuid.UUID
	TaskID       uuid.UUID
	WorktreePath string
	Branch       string
	TmuxSession  string
	Process      *AgentProcess // subprocess handle; nil for supervisors
	LogPath      string        // path to agent-output.log
	StartedAt    time.Time
	ContextUsage *ctxmon.Usage      // latest context window usage; nil until first read
	ActivityMon  *agentmon.Monitor  // activity monitor; nil until started
	Activity     *agentmon.Activity // latest activity snapshot; nil until first scan
	cancel       context.CancelFunc // cancels the monitor and heartbeat goroutines
}

// Runner manages Claude Code agent lifecycles. Headless agents run as
// subprocesses; supervisors and shells still use tmux.
type Runner struct {
	db              *gorm.DB
	startProcess    ProcessStarter // creates subprocess; overridable for tests
	tmuxMgr         *tmux.Manager  // for supervisors/shells only
	tmuxSessionName string         // dashboard session name prefix
	worktree        *worktree.Manager
	claudeBin       string
	maxConcurrent   int

	mu          sync.Mutex
	running     map[uuid.UUID]*RunningAgent
	completions chan Completion
	semaphore   chan struct{} // buffered channel of size maxConcurrent
}

// NewRunner creates an agent Runner. Headless agents are started via
// StartAgentProcess; supervisors and shells use the tmux Manager.
func NewRunner(db *gorm.DB, tm *tmux.Manager, wt *worktree.Manager, claudeBin string, maxConcurrent int) *Runner {
	var sessionName string
	if tm != nil {
		sessionName = tm.SessionName
	}
	return &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxMgr:         tm,
		tmuxSessionName: sessionName,
		worktree:        wt,
		claudeBin:       claudeBin,
		maxConcurrent:   maxConcurrent,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, maxConcurrent),
		semaphore:       make(chan struct{}, maxConcurrent),
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

// TmuxManager returns the underlying tmux Manager for supervisor/shell
// operations. Returns nil if no tmux manager is configured (test code).
func (r *Runner) TmuxManager() *tmux.Manager {
	return r.tmuxMgr
}

// ClaudeBin returns the path to the Claude binary.
func (r *Runner) ClaudeBin() string {
	return r.claudeBin
}

// CanSpawn returns whether there is capacity for another agent.
func (r *Runner) CanSpawn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.running) < r.maxConcurrent
}

// SpawnAgent creates a new worktree, DB record, prompt file, tmux session, and
// starts monitoring. It returns the newly created Agent.
func (r *Runner) SpawnAgent(task *model.Task, featureName string, agentType model.AgentType, prompt string) (*model.Agent, error) {
	wtInfo, err := r.worktree.CreateAgentWorktree(featureName)
	if err != nil {
		return nil, fmt.Errorf("spawn agent: create worktree: %w", err)
	}
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
		HeartbeatAt:    &now,
	}
	if err := r.db.Create(agent).Error; err != nil {
		return nil, fmt.Errorf("spawn agent: create db record: %w", err)
	}

	// Write prompt, start subprocess, begin monitoring.
	if err := r.startAgent(agent.ID, task.ID, worktreePath, branch, sessionName, prompt); err != nil {
		r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		return nil, fmt.Errorf("spawn agent: start: %w", err)
	}

	// Store PID in agent Config for orphan cleanup.
	r.mu.Lock()
	ra := r.running[agent.ID]
	r.mu.Unlock()
	if ra != nil && ra.Process != nil {
		r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("config", model.JSONField{
			"pid": ra.Process.Pid(),
		})
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
	return agent, nil
}

// startAgent writes the prompt file and settings, starts the agent as a
// subprocess, stores the RunningAgent, and launches monitoring goroutines.
func (r *Runner) startAgent(agentID, taskID uuid.UUID, worktreePath, branch, sessionName, prompt string) error {
	// Ensure .claude directory exists.
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("mkdir .claude: %w", err)
	}

	// Write prompt to .claude/agent-prompt.md.
	promptPath := filepath.Join(claudeDir, "agent-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}

	// Verify prompt was written completely.
	written, err := os.ReadFile(promptPath)
	if err != nil || len(written) != len(prompt) {
		return fmt.Errorf("prompt write verification failed: wrote %d of %d bytes",
			len(written), len(prompt))
	}

	// Write the context window status line script.
	statusScriptPath := filepath.Join(claudeDir, "context-status.sh")
	statusOutputPath := ctxmon.UsageFilePath(worktreePath)
	scriptContent := ctxmon.StatusScript(statusOutputPath)
	if err := os.WriteFile(statusScriptPath, []byte(scriptContent), 0o755); err != nil {
		return fmt.Errorf("write status script: %w", err)
	}

	// Build settings.json with idle_prompt notification hook, status line
	// script, and PreCompact hook for context window monitoring.
	idleSignal := filepath.Join(claudeDir, "agent-idle")
	compactionSignal := ctxmon.CompactionSignalPath(worktreePath)

	hooks := map[string]any{
		"Notification": []any{
			map[string]any{
				"matcher": "idle_prompt",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": fmt.Sprintf("touch %s", idleSignal),
						"timeout": idleNotifyTimeoutSecs,
					},
				},
			},
		},
	}
	// Merge PreCompact hooks.
	for k, v := range ctxmon.HooksJSON(compactionSignal) {
		hooks[k] = v
	}

	settings := map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": statusScriptPath,
		},
		"hooks": hooks,
	}

	settingsBytes, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal claude settings: %w", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsBytes, 0o644); err != nil {
		return fmt.Errorf("write claude settings: %w", err)
	}

	// Start agent as a subprocess.
	proc, err := r.startProcess(context.Background(), r.claudeBin, promptPath, worktreePath)
	if err != nil {
		return fmt.Errorf("start subprocess: %w", err)
	}

	// Context for monitor and heartbeat goroutines.
	ctx, cancel := context.WithCancel(context.Background())

	ra := &RunningAgent{
		AgentID:      agentID,
		TaskID:       taskID,
		WorktreePath: worktreePath,
		Branch:       branch,
		TmuxSession:  sessionName,
		Process:      proc,
		LogPath:      proc.LogPath(),
		StartedAt:    time.Now(),
		cancel:       cancel,
	}

	r.mu.Lock()
	r.running[agentID] = ra
	r.mu.Unlock()

	go r.monitorAgent(ctx, agentID, proc, worktreePath)
	go r.heartbeatLoop(ctx, agentID)
	go r.contextMonitorLoop(ctx, agentID, worktreePath)

	return nil
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
		logPath = filepath.Join(agent.WorktreePath, ".claude", "agent-output.log")
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
			StartedAt:    ra.StartedAt,
			ContextUsage: ra.ContextUsage,
			Activity:     ra.Activity,
		})
	}
	return agents
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
	err := r.db.Where("status = ? AND heartbeat_at < ?", model.AgentWorking, cutoff).Find(&staleAgents).Error
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
