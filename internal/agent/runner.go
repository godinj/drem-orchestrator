// Package agent manages Claude Code agent lifecycles via tmux sessions.
//
// It spawns agents in per-agent tmux sessions, monitors their execution, tracks
// heartbeats, and handles graceful shutdown. Each agent runs in its own git
// worktree with its own tmux session, allowing full visibility, interactivity,
// and persistence independent of the dashboard lifecycle.
package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/tmux"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// maxLogBytes is the maximum number of bytes to read from an agent log file
// when returning output (50 KB).
const maxLogBytes = 50 * 1024

// maxCaptureLines is the number of tmux scrollback lines to capture when
// reading agent output from interactive TUI sessions.
const maxCaptureLines = 500

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
	StartedAt    time.Time
	LogPath      string
	cancel       context.CancelFunc // cancels the monitor and heartbeat goroutines
}

// Runner manages Claude Code agent lifecycles via tmux.
type Runner struct {
	db            *gorm.DB
	tmux          *tmux.Manager
	worktree      *worktree.Manager
	claudeBin     string
	maxConcurrent int

	mu          sync.Mutex
	running     map[uuid.UUID]*RunningAgent
	completions chan Completion
	semaphore   chan struct{} // buffered channel of size maxConcurrent
}

// NewRunner creates an agent Runner.
func NewRunner(db *gorm.DB, tm *tmux.Manager, wt *worktree.Manager, claudeBin string, maxConcurrent int) *Runner {
	return &Runner{
		db:            db,
		tmux:          tm,
		worktree:      wt,
		claudeBin:     claudeBin,
		maxConcurrent: maxConcurrent,
		running:       make(map[uuid.UUID]*RunningAgent),
		completions:   make(chan Completion, maxConcurrent),
		semaphore:     make(chan struct{}, maxConcurrent),
	}
}

// agentTypeLabel returns a short human-readable label for an agent type.
func agentTypeLabel(at model.AgentType) string {
	switch at {
	case model.AgentPlanner:
		return "plan"
	case model.AgentCoder:
		return "code"
	case model.AgentResearcher:
		return "research"
	default:
		return string(at)
	}
}

// truncateTitle shortens s to maxLen runes, appending "…" if truncated.
func truncateTitle(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// sanitizeSessionName replaces tmux-illegal characters ("." and ":") with "-",
// preserving "/" which is used as a tree separator.
func sanitizeSessionName(s string) string {
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
	label := agentTypeLabel(agentType)
	shortID := agentID.String()[:4]

	if agentType == model.AgentPlanner {
		title := strings.ReplaceAll(task.Title, "/", "-")
		title = truncateTitle(title, 30)
		name = fmt.Sprintf("%s - %s", label, title)
	} else {
		parentTitle := strings.ReplaceAll(r.loadParentTitle(task), "/", "-")
		parentTitle = truncateTitle(parentTitle, 30)
		subtaskTitle := strings.ReplaceAll(task.Title, "/", "-")
		subtaskTitle = truncateTitle(subtaskTitle, 30)
		name = fmt.Sprintf("%s - %s > %s", label, parentTitle, subtaskTitle)
	}

	session = sanitizeSessionName(fmt.Sprintf("%s/%s %s", r.tmux.SessionName, name, shortID))
	return name, session
}

// TmuxSessionName returns the dashboard tmux session name used as a namespace
// prefix for agent and supervisor sessions.
func (r *Runner) TmuxSessionName() string {
	return r.tmux.SessionName
}

// TmuxManager returns the underlying tmux Manager.
func (r *Runner) TmuxManager() *tmux.Manager {
	return r.tmux
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

// SpawnAgent is the high-level spawn that creates a worktree, DB record, prompt
// file, tmux window, and starts monitoring. It returns the newly created Agent.
func (r *Runner) SpawnAgent(task *model.Task, featureName string, agentType model.AgentType, prompt string) (*model.Agent, error) {
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

	// Create agent worktree.
	wtInfo, err := r.worktree.CreateAgentWorktree(featureName)
	if err != nil {
		return nil, fmt.Errorf("spawn agent: create worktree: %w", err)
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
		WorktreePath:   wtInfo.Path,
		WorktreeBranch: wtInfo.Branch,
		TmuxSession:    sessionName,
		HeartbeatAt:    &now,
	}
	if err := r.db.Create(agent).Error; err != nil {
		return nil, fmt.Errorf("spawn agent: create db record: %w", err)
	}

	// Write prompt, build command, create tmux session, start monitoring.
	if err := r.startAgent(agent.ID, task.ID, wtInfo.Path, wtInfo.Branch, sessionName, prompt); err != nil {
		// Mark agent as dead since we failed to start it.
		r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		return nil, fmt.Errorf("spawn agent: start: %w", err)
	}

	success = true
	return agent, nil
}

// Spawn is a low-level spawn for a pre-existing Agent DB record. It writes the
// prompt, creates the tmux session, and starts monitoring. The caller must have
// already created the agent and worktree.
func (r *Runner) Spawn(agentID, taskID uuid.UUID, worktreePath, branch, prompt string) error {
	// Acquire semaphore (non-blocking).
	select {
	case r.semaphore <- struct{}{}:
	default:
		return fmt.Errorf("spawn: max concurrent agents (%d) reached", r.maxConcurrent)
	}

	success := false
	defer func() {
		if !success {
			<-r.semaphore
		}
	}()

	// Read the agent from DB to get its session name.
	var agent model.Agent
	if err := r.db.First(&agent, "id = ?", agentID).Error; err != nil {
		return fmt.Errorf("spawn: read agent %s: %w", agentID, err)
	}

	sessionName := agent.TmuxSession
	if sessionName == "" {
		var task model.Task
		if err := r.db.First(&task, "id = ?", taskID).Error; err != nil {
			return fmt.Errorf("spawn: load task %s: %w", taskID, err)
		}
		name, sess := r.buildAgentNames(&task, agent.AgentType, agentID)
		sessionName = sess
		// Persist both name and session name.
		r.db.Model(&model.Agent{}).Where("id = ?", agentID).Updates(map[string]interface{}{
			"name":         name,
			"tmux_session": sessionName,
		})
	}

	if err := r.startAgent(agentID, taskID, worktreePath, branch, sessionName, prompt); err != nil {
		return fmt.Errorf("spawn: start: %w", err)
	}

	success = true
	return nil
}

// startAgent performs the common steps shared by SpawnAgent and Spawn:
// write prompt file, build command, create tmux session, store RunningAgent,
// launch monitor and heartbeat goroutines.
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

	// Write settings.json with an idle_prompt notification hook that creates
	// a signal file when Claude finishes processing. The orchestrator polls
	// for this file to detect completion while keeping the TUI alive.
	idleSignal := filepath.Join(claudeDir, "agent-idle")
	settingsJSON := fmt.Sprintf(`{
  "hooks": {
    "Notification": [
      {
        "matcher": "idle_prompt",
        "hooks": [
          {
            "type": "command",
            "command": "touch %s",
            "timeout": 5
          }
        ]
      }
    ]
  }
}`, idleSignal)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0o644); err != nil {
		return fmt.Errorf("write claude settings: %w", err)
	}

	// Build the claude command (interactive TUI mode).
	cmd := fmt.Sprintf(
		"%s --dangerously-skip-permissions \"$(cat %s)\"",
		r.claudeBin, promptPath,
	)

	// Create tmux session for this agent.
	if err := r.tmux.CreateAgentSession(sessionName, cmd, worktreePath); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}

	// Context for monitor and heartbeat goroutines.
	ctx, cancel := context.WithCancel(context.Background())

	ra := &RunningAgent{
		AgentID:      agentID,
		TaskID:       taskID,
		WorktreePath: worktreePath,
		Branch:       branch,
		TmuxSession:  sessionName,
		StartedAt:    time.Now(),
		LogPath:      "",
		cancel:       cancel,
	}

	r.mu.Lock()
	r.running[agentID] = ra
	r.mu.Unlock()

	go r.monitorAgent(ctx, agentID, sessionName, worktreePath)
	go r.heartbeatLoop(ctx, agentID)

	return nil
}

// StopAgent performs a graceful shutdown of an agent: cancels goroutines,
// closes the tmux window, updates the DB, and releases capacity.
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

	// Kill the tmux session (idempotent).
	if err := r.tmux.KillAgentSession(ra.TmuxSession); err != nil {
		// Log but don't fail — best effort.
		_ = err
	}

	// Update agent DB status to DEAD.
	if err := r.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", model.AgentDead).Error; err != nil {
		return fmt.Errorf("stop agent: update db: %w", err)
	}

	// Release semaphore.
	<-r.semaphore

	return nil
}

// GetAgentOutput captures the agent's tmux pane output. For running agents it
// reads from the in-memory session name; for finished agents it looks up the
// session name from the DB. Returns an empty string if no session is available.
func (r *Runner) GetAgentOutput(agentID uuid.UUID) (string, error) {
	r.mu.Lock()
	ra, ok := r.running[agentID]
	r.mu.Unlock()

	if !ok {
		var agent model.Agent
		if err := r.db.First(&agent, "id = ?", agentID).Error; err != nil {
			return "", fmt.Errorf("get agent output: agent %s not found: %w", agentID, err)
		}
		if agent.TmuxSession == "" {
			return "", nil
		}
		return r.tmux.CaptureAgentPane(agent.TmuxSession, maxCaptureLines)
	}
	return r.tmux.CaptureAgentPane(ra.TmuxSession, maxCaptureLines)
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
			StartedAt:    ra.StartedAt,
			LogPath:      ra.LogPath,
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
			// Not in our running map — kill tmux session if it exists and update DB.
			if agent.TmuxSession != "" {
				_ = r.tmux.KillAgentSession(agent.TmuxSession)
			}
			r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		}
	}

	// Reap orphaned tmux sessions — sessions that exist in tmux but have no
	// corresponding WORKING agent in the DB (e.g. after a crash or restart).
	if err := r.reapOrphanedSessions(); err != nil {
		return fmt.Errorf("cleanup stale agents: reap orphaned sessions: %w", err)
	}

	return nil
}

// reapOrphanedSessions kills tmux agent sessions that are not associated with
// any active (WORKING) agent. This catches sessions left behind after crashes,
// restarts, or agents that completed but whose sessions persisted due to
// remain-on-exit.
func (r *Runner) reapOrphanedSessions() error {
	sessions, err := r.tmux.ListAgentSessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return nil
	}

	// Build the set of tmux session names for agents still actively working.
	var activeAgents []model.Agent
	if err := r.db.Where("status = ?", model.AgentWorking).Find(&activeAgents).Error; err != nil {
		return fmt.Errorf("query active agents: %w", err)
	}

	activeSessions := make(map[string]bool, len(activeAgents))
	for _, a := range activeAgents {
		if a.TmuxSession != "" {
			activeSessions[a.TmuxSession] = true
		}
	}

	// Also keep sessions for agents in the running map (may not have been
	// persisted to DB yet).
	r.mu.Lock()
	for _, ra := range r.running {
		activeSessions[ra.TmuxSession] = true
	}
	r.mu.Unlock()

	for _, sess := range sessions {
		if activeSessions[sess] {
			continue
		}
		// Supervisor sessions are interactive and not tracked as agents —
		// skip them so they aren't reaped.
		if strings.Contains(sess, "/supervisor ") {
			continue
		}
		_ = r.tmux.KillAgentSession(sess)
	}

	return nil
}

// monitorAgent is a background goroutine that waits for the agent's idle
// signal file (created by the idle_prompt notification hook) and then
// gracefully exits the Claude TUI. Falls back to WaitForAgentExit if
// the idle signal approach fails.
func (r *Runner) monitorAgent(ctx context.Context, agentID uuid.UUID, sessionName, worktreePath string) {
	idleSignal := filepath.Join(worktreePath, ".claude", "agent-idle")

	exitCode, err := r.tmux.WaitForAgentIdle(ctx, sessionName, idleSignal)
	if err != nil {
		exitCode = -1
	}

	// If the context was cancelled (StopAgent), don't send a completion.
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Send completion.
	r.completions <- Completion{AgentID: agentID, ReturnCode: exitCode}

	// Remove from running map.
	r.mu.Lock()
	delete(r.running, agentID)
	r.mu.Unlock()

	// Release semaphore.
	<-r.semaphore
}

// readLogTail reads the tail of a log file, returning at most maxLogBytes of content.
// Returns an empty string if the file does not exist.
func readLogTail(logPath string) (string, error) {
	f, err := os.Open(logPath)
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

	size := info.Size()
	if size <= maxLogBytes {
		data, err := io.ReadAll(f)
		if err != nil {
			return "", fmt.Errorf("read log: read: %w", err)
		}
		return string(data), nil
	}

	// Seek to the last maxLogBytes.
	if _, err := f.Seek(-maxLogBytes, io.SeekEnd); err != nil {
		return "", fmt.Errorf("read log: seek: %w", err)
	}

	data := make([]byte, maxLogBytes)
	n, err := io.ReadFull(f, data)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("read log: read tail: %w", err)
	}

	return string(data[:n]), nil
}
