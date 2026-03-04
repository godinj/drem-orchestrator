// Package agent manages Claude Code agent lifecycles via tmux windows.
//
// It spawns agents in tmux windows, monitors their execution, tracks heartbeats,
// and handles graceful shutdown. Each agent runs in its own git worktree with
// its own tmux window, allowing full visibility and interactivity.
package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	TmuxWindow   string
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
	windowName := fmt.Sprintf("%s-%s", agentType, agentID.String()[:8])
	now := time.Now()
	agent := &model.Agent{
		ID:             agentID,
		ProjectID:      task.ProjectID,
		AgentType:      agentType,
		Name:           windowName,
		Status:         model.AgentWorking,
		CurrentTaskID:  &task.ID,
		WorktreePath:   wtInfo.Path,
		WorktreeBranch: wtInfo.Branch,
		TmuxWindow:     windowName,
		HeartbeatAt:    &now,
	}
	if err := r.db.Create(agent).Error; err != nil {
		return nil, fmt.Errorf("spawn agent: create db record: %w", err)
	}

	// Write prompt, build command, create tmux window, start monitoring.
	if err := r.startAgent(agent.ID, task.ID, wtInfo.Path, wtInfo.Branch, windowName, prompt); err != nil {
		// Mark agent as dead since we failed to start it.
		r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		return nil, fmt.Errorf("spawn agent: start: %w", err)
	}

	success = true
	return agent, nil
}

// Spawn is a low-level spawn for a pre-existing Agent DB record. It writes the
// prompt, creates the tmux window, and starts monitoring. The caller must have
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

	// Read the agent from DB to get its window name.
	var agent model.Agent
	if err := r.db.First(&agent, "id = ?", agentID).Error; err != nil {
		return fmt.Errorf("spawn: read agent %s: %w", agentID, err)
	}

	windowName := agent.TmuxWindow
	if windowName == "" {
		windowName = fmt.Sprintf("%s-%s", agent.AgentType, agentID.String()[:8])
		// Persist the window name.
		r.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("tmux_window", windowName)
	}

	if err := r.startAgent(agentID, taskID, worktreePath, branch, windowName, prompt); err != nil {
		return fmt.Errorf("spawn: start: %w", err)
	}

	success = true
	return nil
}

// startAgent performs the common steps shared by SpawnAgent and Spawn:
// write prompt file, build command, create tmux window, store RunningAgent,
// launch monitor and heartbeat goroutines.
func (r *Runner) startAgent(agentID, taskID uuid.UUID, worktreePath, branch, windowName, prompt string) error {
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

	// Build the claude command.
	logPath := filepath.Join(claudeDir, "agent.log")
	cmd := fmt.Sprintf(
		"%s -p --dangerously-skip-permissions < %s 2>&1 | tee %s",
		r.claudeBin, promptPath, logPath,
	)

	// Create tmux window.
	if err := r.tmux.CreateWindow(windowName, cmd, worktreePath); err != nil {
		return fmt.Errorf("create tmux window: %w", err)
	}

	// Context for monitor and heartbeat goroutines.
	ctx, cancel := context.WithCancel(context.Background())

	ra := &RunningAgent{
		AgentID:      agentID,
		TaskID:       taskID,
		WorktreePath: worktreePath,
		Branch:       branch,
		TmuxWindow:   windowName,
		StartedAt:    time.Now(),
		LogPath:      logPath,
		cancel:       cancel,
	}

	r.mu.Lock()
	r.running[agentID] = ra
	r.mu.Unlock()

	go r.monitorAgent(ctx, agentID, windowName)
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

	// Close the tmux window (idempotent).
	if err := r.tmux.CloseWindow(ra.TmuxWindow); err != nil {
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

// GetAgentOutput reads the agent's log file. Returns the last maxLogBytes of
// content if the file is large. Returns an empty string if the file does not exist.
func (r *Runner) GetAgentOutput(agentID uuid.UUID) (string, error) {
	r.mu.Lock()
	ra, ok := r.running[agentID]
	r.mu.Unlock()

	if !ok {
		// Try reading the agent from DB to get the worktree path.
		var agent model.Agent
		if err := r.db.First(&agent, "id = ?", agentID).Error; err != nil {
			return "", fmt.Errorf("get agent output: agent %s not found: %w", agentID, err)
		}
		if agent.WorktreePath == "" {
			return "", nil
		}
		logPath := filepath.Join(agent.WorktreePath, ".claude", "agent.log")
		return readLogTail(logPath)
	}

	return readLogTail(ra.LogPath)
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
			TmuxWindow:   ra.TmuxWindow,
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
			// Not in our running map — close tmux window if it exists and update DB.
			if agent.TmuxWindow != "" {
				_ = r.tmux.CloseWindow(agent.TmuxWindow)
			}
			r.db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("status", model.AgentDead)
		}
	}

	return nil
}

// monitorAgent is a background goroutine that waits for the tmux window's
// process to exit and records the completion.
func (r *Runner) monitorAgent(ctx context.Context, agentID uuid.UUID, windowName string) {
	// WaitForExit blocks until the command exits.
	exitCode, err := r.tmux.WaitForExit(windowName)
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
