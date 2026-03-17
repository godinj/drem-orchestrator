package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// mockProcessStarter returns a ProcessStarter that creates a real subprocess
// using a short-lived shell script as the "claude binary".
func mockProcessStarter(exitCode int, startErr error) ProcessStarter {
	return func(ctx context.Context, claudeBin, promptPath, cwd string) (*AgentProcess, error) {
		if startErr != nil {
			return nil, startErr
		}
		// Use StartAgentProcess with a simple script that reads stdin and exits.
		return StartAgentProcess(ctx, claudeBin, promptPath, cwd)
	}
}

// mockProcessStarterBlocking returns a ProcessStarter whose processes block
// until their idle signal file is created, simulating a real agent.
func mockProcessStarterBlocking() ProcessStarter {
	return func(ctx context.Context, claudeBin, promptPath, cwd string) (*AgentProcess, error) {
		return StartAgentProcess(ctx, claudeBin, promptPath, cwd)
	}
}

// writeFakeClaudeBin creates a shell script that acts as a fake Claude binary.
// It reads stdin (the prompt) and then exits with the given code.
func writeFakeClaudeBin(t *testing.T, dir string, exitCode int) string {
	t.Helper()
	binPath := filepath.Join(dir, "fake-claude")
	script := fmt.Sprintf("#!/bin/sh\ncat > /dev/null\nexit %d\n", exitCode)
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}
	return binPath
}

// newMockRunner creates a Runner backed by a mock ProcessStarter and in-memory DB.
func newMockRunner(t *testing.T, db *gorm.DB, claudeBin string) *Runner {
	t.Helper()
	return &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 4),
		semaphore:       make(chan struct{}, 4),
	}
}

// newMockRunnerWithStarter creates a Runner with a custom ProcessStarter.
func newMockRunnerWithStarter(t *testing.T, db *gorm.DB, starter ProcessStarter) *Runner {
	t.Helper()
	return &Runner{
		db:              db,
		startProcess:    starter,
		tmuxSessionName: "test-session",
		claudeBin:       "/bin/false",
		maxConcurrent:   4,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 4),
		semaphore:       make(chan struct{}, 4),
	}
}

func TestSpawnAgentInWorktree_Subprocess(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)
	r := newMockRunner(t, db, claudeBin)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Implement auth module", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "You are a coding agent. Implement the auth module."

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify agent DB record was created.
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.Status != model.AgentWorking {
		t.Errorf("agent status = %q, want %q", dbAgent.Status, model.AgentWorking)
	}
	if dbAgent.AgentType != model.AgentCoder {
		t.Errorf("agent type = %q, want %q", dbAgent.AgentType, model.AgentCoder)
	}
	if dbAgent.WorktreePath != worktreeDir {
		t.Errorf("agent worktree = %q, want %q", dbAgent.WorktreePath, worktreeDir)
	}
	if dbAgent.TmuxSession == "" {
		t.Error("agent session name is empty")
	}

	// Verify prompt file was written to worktree.
	promptPath := filepath.Join(worktreeDir, ".claude", "agent-prompt.md")
	written, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("prompt file not written: %v", err)
	}
	if string(written) != prompt {
		t.Errorf("prompt content = %q, want %q", string(written), prompt)
	}

	// Verify settings.json was written.
	settingsPath := filepath.Join(worktreeDir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		t.Error("settings.json not written")
	}

	// Verify agent is in the running map with a Process.
	r.mu.Lock()
	ra, inRunning := r.running[agent.ID]
	r.mu.Unlock()
	if !inRunning {
		t.Error("agent not found in running map")
	}
	if ra != nil && ra.Process == nil {
		t.Error("agent running entry has nil Process")
	}

	// Clean up: stop the agent to cancel background goroutines.
	_ = r.StopAgent(agent.ID)
}

func TestSpawnAgentInWorktree_ProcessFailure(t *testing.T) {
	db := testutil.NewTestDB(t)

	starter := func(ctx context.Context, claudeBin, promptPath, cwd string) (*AgentProcess, error) {
		return nil, fmt.Errorf("process start failed")
	}
	r := newMockRunnerWithStarter(t, db, starter)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Implement auth module", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Test prompt"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err == nil {
		t.Fatal("expected error from SpawnAgentInWorktree, got nil")
	}
	if agent != nil {
		t.Errorf("expected nil agent on failure, got %+v", agent)
	}

	// Verify the error is propagated with context.
	if !contains(err.Error(), "process start failed") {
		t.Errorf("error should contain cause, got: %v", err)
	}

	// Verify the DB record was created but marked as dead.
	var agents []model.Agent
	db.Find(&agents)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent record, got %d", len(agents))
	}
	if agents[0].Status != model.AgentDead {
		t.Errorf("agent status = %q, want %q (should be dead on failure)", agents[0].Status, model.AgentDead)
	}

	// Verify the running map is empty (no orphan).
	r.mu.Lock()
	runningCount := len(r.running)
	r.mu.Unlock()
	if runningCount != 0 {
		t.Errorf("running map has %d entries, expected 0 (no orphan)", runningCount)
	}
}

func TestStopAgent_Subprocess(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)
	r := newMockRunner(t, db, claudeBin)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	agentID := uuid.New()
	sessionName := "test-session/code - auth a1b2"

	// Create agent DB record in working state.
	now := time.Now()
	dbAgent := &model.Agent{
		ID:          agentID,
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - auth",
		Status:      model.AgentWorking,
		TmuxSession: sessionName,
		HeartbeatAt: &now,
	}
	if err := db.Create(dbAgent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Add to running map with a cancel func (no real process).
	_, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID:     agentID,
		TmuxSession: sessionName,
		cancel:      cancel,
	}
	r.mu.Unlock()

	// Acquire one semaphore slot (simulating a running agent).
	r.semaphore <- struct{}{}

	if err := r.StopAgent(agentID); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	// Verify DB record updated to dead.
	var updatedAgent model.Agent
	if err := db.First(&updatedAgent, "id = ?", agentID).Error; err != nil {
		t.Fatalf("agent not found: %v", err)
	}
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("agent status = %q, want %q", updatedAgent.Status, model.AgentDead)
	}

	// Verify agent removed from running map.
	r.mu.Lock()
	_, inRunning := r.running[agentID]
	r.mu.Unlock()
	if inRunning {
		t.Error("agent still in running map after StopAgent")
	}
}

func TestStopAgent_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	unknownID := uuid.New()
	err := r.StopAgent(unknownID)
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
	if !contains(err.Error(), "not running") {
		t.Errorf("error should indicate agent is not running, got: %v", err)
	}
}

func TestGetAgentOutput_FromLogFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	agentID := uuid.New()
	worktreeDir := t.TempDir()
	expectedOutput := "Agent is processing task...\nDone!"

	// Write a log file.
	claudeDir := filepath.Join(worktreeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(claudeDir, "agent-output.log")
	if err := os.WriteFile(logPath, []byte(expectedOutput), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	// Add agent to running map.
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID: agentID,
		LogPath: logPath,
	}
	r.mu.Unlock()

	output, err := r.GetAgentOutput(agentID)
	if err != nil {
		t.Fatalf("GetAgentOutput: %v", err)
	}
	if output != expectedOutput {
		t.Errorf("output = %q, want %q", output, expectedOutput)
	}
}

func TestGetAgentOutput_NotRunning(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	unknownID := uuid.New()

	// Agent not in running map and not in DB.
	_, err := r.GetAgentOutput(unknownID)
	if err == nil {
		t.Fatal("expected error for non-existent agent, got nil")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("error should indicate agent not found, got: %v", err)
	}
}

func TestGetAgentOutput_NotRunning_InDB(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	agentID := uuid.New()
	expectedOutput := "Output from DB agent"

	// Create a worktree with a log file.
	worktreeDir := t.TempDir()
	claudeDir := filepath.Join(worktreeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(claudeDir, "agent-output.log")
	if err := os.WriteFile(logPath, []byte(expectedOutput), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	// Create agent in DB but not in running map (finished agent).
	dbAgent := &model.Agent{
		ID:           agentID,
		ProjectID:    project.ID,
		AgentType:    model.AgentCoder,
		Name:         "code - db-output",
		Status:       model.AgentDead,
		WorktreePath: worktreeDir,
	}
	if err := db.Create(dbAgent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	output, err := r.GetAgentOutput(agentID)
	if err != nil {
		t.Fatalf("GetAgentOutput: %v", err)
	}
	if output != expectedOutput {
		t.Errorf("output = %q, want %q", output, expectedOutput)
	}
}

func TestCleanupStaleAgents_Subprocess(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	// Create a stale agent: heartbeat older than the timeout.
	staleTime := time.Now().Add(-10 * time.Minute)
	staleAgent := &model.Agent{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - stale",
		Status:      model.AgentWorking,
		TmuxSession: "test-session/code - stale a1b2",
		HeartbeatAt: &staleTime,
	}
	if err := db.Create(staleAgent).Error; err != nil {
		t.Fatalf("create stale agent: %v", err)
	}

	// Run cleanup with a 5-minute timeout (stale agent's heartbeat is 10 min old).
	if err := r.CleanupStaleAgents(5 * time.Minute); err != nil {
		t.Fatalf("CleanupStaleAgents: %v", err)
	}

	// Verify DB record updated to dead.
	var updatedAgent model.Agent
	if err := db.First(&updatedAgent, "id = ?", staleAgent.ID).Error; err != nil {
		t.Fatalf("agent not found: %v", err)
	}
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("stale agent status = %q, want %q", updatedAgent.Status, model.AgentDead)
	}
}

func TestCleanupStaleAgents_NoneStale(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	// Create a fresh agent: heartbeat is recent.
	freshTime := time.Now()
	freshAgent := &model.Agent{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - fresh",
		Status:      model.AgentWorking,
		TmuxSession: "test-session/code - fresh a1b2",
		HeartbeatAt: &freshTime,
	}
	if err := db.Create(freshAgent).Error; err != nil {
		t.Fatalf("create fresh agent: %v", err)
	}

	if err := r.CleanupStaleAgents(5 * time.Minute); err != nil {
		t.Fatalf("CleanupStaleAgents: %v", err)
	}

	// Verify agent is still working.
	var updatedAgent model.Agent
	if err := db.First(&updatedAgent, "id = ?", freshAgent.ID).Error; err != nil {
		t.Fatalf("agent not found: %v", err)
	}
	if updatedAgent.Status != model.AgentWorking {
		t.Errorf("fresh agent status = %q, want %q", updatedAgent.Status, model.AgentWorking)
	}
}

// TestCleanupStaleAgents_InRunningMap tests cleanup of a stale agent that
// is also present in the running map (uses StopAgent path).
func TestCleanupStaleAgents_InRunningMap(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	staleTime := time.Now().Add(-10 * time.Minute)
	sessionName := "test-session/code - stale-running a1b2"
	agentID := uuid.New()

	staleAgent := &model.Agent{
		ID:          agentID,
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - stale-running",
		Status:      model.AgentWorking,
		TmuxSession: sessionName,
		HeartbeatAt: &staleTime,
	}
	if err := db.Create(staleAgent).Error; err != nil {
		t.Fatalf("create stale agent: %v", err)
	}

	// Add to running map.
	_, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID:     agentID,
		TmuxSession: sessionName,
		cancel:      cancel,
	}
	r.mu.Unlock()

	// Acquire a semaphore slot.
	r.semaphore <- struct{}{}

	if err := r.CleanupStaleAgents(5 * time.Minute); err != nil {
		t.Fatalf("CleanupStaleAgents: %v", err)
	}

	// Verify agent was stopped (removed from running map).
	r.mu.Lock()
	_, inRunning := r.running[agentID]
	r.mu.Unlock()
	if inRunning {
		t.Error("stale agent still in running map after cleanup")
	}

	// Verify DB updated.
	var updatedAgent model.Agent
	if err := db.First(&updatedAgent, "id = ?", agentID).Error; err != nil {
		t.Fatalf("agent not found: %v", err)
	}
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("agent status = %q, want %q", updatedAgent.Status, model.AgentDead)
	}
}

// contains checks if s contains substr (helper to avoid importing strings in tests).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
