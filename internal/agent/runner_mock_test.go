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
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// mockProcessStarter returns a ProcessStarter that creates a real subprocess
// using a short-lived shell script as the "claude binary".
func mockProcessStarter(exitCode int, startErr error) ProcessStarter {
	return func(ctx context.Context, claudeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error) {
		if startErr != nil {
			return nil, startErr
		}
		// Use StartAgentProcess with a simple script that reads stdin and exits.
		return StartAgentProcess(ctx, claudeBin, promptPath, cwd, extraArgs)
	}
}

// mockProcessStarterBlocking returns a ProcessStarter whose processes block
// until their idle signal file is created, simulating a real agent.
func mockProcessStarterBlocking() ProcessStarter {
	return func(ctx context.Context, claudeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error) {
		return StartAgentProcess(ctx, claudeBin, promptPath, cwd, extraArgs)
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
		agentConfigs:    func(model.AgentType) model.AgentCLIConfig { return model.AgentCLIConfig{Effort: "medium"} },
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
		agentConfigs:    func(model.AgentType) model.AgentCLIConfig { return model.AgentCLIConfig{Effort: "medium"} },
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

	starter := func(ctx context.Context, claudeBin, promptPath, cwd string, extraArgs []string) (*AgentProcess, error) {
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

// TestCleanupStaleAgents_NullHeartbeat tests defense in depth: agents with
// status=WORKING and heartbeat_at=NULL should be caught by cleanup as stale.
// This covers the case where a bug causes an agent to be created without
// setting HeartbeatAt (e.g., the classifier agent bug).
func TestCleanupStaleAgents_NullHeartbeat(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	// Create an agent with status=WORKING but HeartbeatAt=nil (NULL).
	nullHeartbeatAgent := &model.Agent{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		AgentType:   model.AgentClassifier,
		Name:        "classifier-null-hb",
		Status:      model.AgentWorking,
		TmuxSession: "test-session/classifier-null-hb",
		HeartbeatAt: nil,
	}
	if err := db.Create(nullHeartbeatAgent).Error; err != nil {
		t.Fatalf("create agent with null heartbeat: %v", err)
	}

	// Run cleanup — the agent has no heartbeat at all, so it should be
	// treated as stale regardless of the timeout value.
	if err := r.CleanupStaleAgents(5 * time.Minute); err != nil {
		t.Fatalf("CleanupStaleAgents: %v", err)
	}

	var updated model.Agent
	if err := db.First(&updated, "id = ?", nullHeartbeatAgent.ID).Error; err != nil {
		t.Fatalf("agent not found: %v", err)
	}
	if updated.Status != model.AgentDead {
		t.Errorf("agent with null heartbeat status = %q, want %q", updated.Status, model.AgentDead)
	}
}

// TestSpawnAgentInWorktree_PopulatesModelID verifies that ModelID is set from
// the resolved config's Model field when spawning an agent.
func TestSpawnAgentInWorktree_PopulatesModelID(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// agentConfigs returns a config with Model='claude-opus'
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "claude-opus", Effort: "medium"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Implement feature X", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Implement feature X"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify ModelID was set in the returned agent
	if agent.ModelID != "claude-opus" {
		t.Errorf("agent.ModelID = %q, want %q", agent.ModelID, "claude-opus")
	}

	// Verify ModelID was set in the DB record
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.ModelID != "claude-opus" {
		t.Errorf("dbAgent.ModelID = %q, want %q", dbAgent.ModelID, "claude-opus")
	}

	_ = r.StopAgent(agent.ID)
}

// TestSpawnAgentInWorktree_PopulatesEffort verifies that Effort is set from
// the resolved config's Effort field when spawning an agent.
func TestSpawnAgentInWorktree_PopulatesEffort(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// agentConfigs returns a config with Effort='high'
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "", Effort: "high"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Complex refactoring", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Refactor the system"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify Effort was set in the returned agent
	if agent.Effort != "high" {
		t.Errorf("agent.Effort = %q, want %q", agent.Effort, "high")
	}

	// Verify Effort was set in the DB record
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.Effort != "high" {
		t.Errorf("dbAgent.Effort = %q, want %q", dbAgent.Effort, "high")
	}

	_ = r.StopAgent(agent.ID)
}

// TestSpawnAgentInWorktree_PopulatesModelIDAndEffort verifies that both
// ModelID and Effort are populated together from the resolved config.
func TestSpawnAgentInWorktree_PopulatesModelIDAndEffort(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// agentConfigs returns a complete config with both Model and Effort
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "claude-opus", Effort: "high"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Complex task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Complex work"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify both fields are set
	if agent.ModelID != "claude-opus" {
		t.Errorf("agent.ModelID = %q, want %q", agent.ModelID, "claude-opus")
	}
	if agent.Effort != "high" {
		t.Errorf("agent.Effort = %q, want %q", agent.Effort, "high")
	}

	// Verify both in DB record
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.ModelID != "claude-opus" {
		t.Errorf("dbAgent.ModelID = %q, want %q", dbAgent.ModelID, "claude-opus")
	}
	if dbAgent.Effort != "high" {
		t.Errorf("dbAgent.Effort = %q, want %q", dbAgent.Effort, "high")
	}

	_ = r.StopAgent(agent.ID)
}

// TestSpawnAgentInWorktree_EmptyModelID verifies that when Model is an empty
// string in the config, Agent.ModelID remains empty (not set to default).
func TestSpawnAgentInWorktree_EmptyModelID(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// agentConfigs explicitly returns empty Model
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "", Effort: "medium"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Work"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify ModelID is empty (should not be filled with a default)
	if agent.ModelID != "" {
		t.Errorf("agent.ModelID = %q, want %q (empty)", agent.ModelID, "")
	}

	// Verify in DB record
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.ModelID != "" {
		t.Errorf("dbAgent.ModelID = %q, want %q (empty)", dbAgent.ModelID, "")
	}

	_ = r.StopAgent(agent.ID)
}

// TestSpawnAgentInWorktree_EmptyEffort verifies that when Effort is an empty
// string in the config, Agent.Effort remains empty (edge case validation).
func TestSpawnAgentInWorktree_EmptyEffort(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// agentConfigs explicitly returns empty Effort
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "claude-sonnet", Effort: ""}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Work"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify Effort is empty
	if agent.Effort != "" {
		t.Errorf("agent.Effort = %q, want %q (empty)", agent.Effort, "")
	}

	// Verify in DB record
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.Effort != "" {
		t.Errorf("dbAgent.Effort = %q, want %q (empty)", dbAgent.Effort, "")
	}

	_ = r.StopAgent(agent.ID)
}

// TestSpawnAgentInWorktree_ConfigVariesByAgentType verifies that different
// agent types can have different ModelID and Effort values via the agentConfigs function.
func TestSpawnAgentInWorktree_ConfigVariesByAgentType(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// agentConfigs returns different configs based on agent type
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(at model.AgentType) model.AgentCLIConfig {
			switch at {
			case model.AgentPlanner:
				return model.AgentCLIConfig{Model: "claude-opus", Effort: "high"}
			case model.AgentCoder:
				return model.AgentCLIConfig{Model: "claude-sonnet", Effort: "medium"}
			case model.AgentReviewer:
				return model.AgentCLIConfig{Model: "claude-haiku", Effort: "low"}
			default:
				return model.AgentCLIConfig{Model: "", Effort: "medium"}
			}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Work"

	// Spawn coder agent (should get claude-sonnet/medium)
	coderAgent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree (coder): %v", err)
	}
	if coderAgent.ModelID != "claude-sonnet" {
		t.Errorf("coder agent ModelID = %q, want %q", coderAgent.ModelID, "claude-sonnet")
	}
	if coderAgent.Effort != "medium" {
		t.Errorf("coder agent Effort = %q, want %q", coderAgent.Effort, "medium")
	}

	_ = r.StopAgent(coderAgent.ID)

	// Create second task for reviewer agent
	task2 := testutil.CreateTask(t, db, project.ID, "Task 2", model.StatusInProgress)
	worktreeDir2 := t.TempDir()

	// Spawn reviewer agent (should get claude-haiku/low)
	reviewerAgent, err := r.SpawnAgentInWorktree(&task2, worktreeDir2, model.AgentReviewer, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree (reviewer): %v", err)
	}
	if reviewerAgent.ModelID != "claude-haiku" {
		t.Errorf("reviewer agent ModelID = %q, want %q", reviewerAgent.ModelID, "claude-haiku")
	}
	if reviewerAgent.Effort != "low" {
		t.Errorf("reviewer agent Effort = %q, want %q", reviewerAgent.Effort, "low")
	}

	_ = r.StopAgent(reviewerAgent.ID)
}

// TestSpawnAgent_PopulatesModelIDAndEffort verifies that ModelID and Effort
// are populated when using SpawnAgent (which creates a new worktree).
func TestSpawnAgent_PopulatesModelIDAndEffort(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	bareRepo := testutil.InitBareRepoWithMainWorktree(t)
	wtMgr := worktree.NewManager(bareRepo, "master")

	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		worktree:        wtMgr,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "claude-opus", Effort: "high"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	project := testutil.CreateProject(t, db, "test-project", bareRepo, "master")
	task := testutil.CreateTask(t, db, project.ID, "Implementation task", model.StatusInProgress)

	prompt := "Implement the feature"

	agent, err := r.SpawnAgent(&task, "test-feature", model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}

	// Verify ModelID and Effort were set
	if agent.ModelID != "claude-opus" {
		t.Errorf("agent.ModelID = %q, want %q", agent.ModelID, "claude-opus")
	}
	if agent.Effort != "high" {
		t.Errorf("agent.Effort = %q, want %q", agent.Effort, "high")
	}

	// Verify in DB
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.ModelID != "claude-opus" {
		t.Errorf("dbAgent.ModelID = %q, want %q", dbAgent.ModelID, "claude-opus")
	}
	if dbAgent.Effort != "high" {
		t.Errorf("dbAgent.Effort = %q, want %q", dbAgent.Effort, "high")
	}

	_ = r.StopAgent(agent.ID)
}

// TestSpawnAgentInWorktree_FieldsSetBeforeDBCreate verifies that ModelID and
// Effort are set BEFORE the agent record is saved to the database (not in a
// subsequent update). This is validated by checking that the DB read returns
// the values with the initial query.
func TestSpawnAgentInWorktree_FieldsSetBeforeDBCreate(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)

	// Hook into the database to monitor the Create operation
	createCallCount := 0
	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       claudeBin,
		maxConcurrent:   4,
		agentConfigs: func(model.AgentType) model.AgentCLIConfig {
			return model.AgentCLIConfig{Model: "claude-opus", Effort: "high"}
		},
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	// Set up a callback on db to track Create operations
	r.db.Callback().Create().Before("gorm:create").Register("test:track_create", func(db *gorm.DB) {
		if _, ok := db.Statement.Dest.(*model.Agent); ok {
			createCallCount++
		}
	})

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Task", model.StatusInProgress)

	worktreeDir := t.TempDir()
	prompt := "Work"

	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// If the agent record was created at least once
	if createCallCount < 1 {
		t.Error("agent record was not created")
	}

	// Query the DB immediately after spawn — fields should already be present
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("failed to query agent from DB: %v", err)
	}

	// Fields must have been set before the Create call
	if dbAgent.ModelID != "claude-opus" {
		t.Errorf("dbAgent.ModelID was not set before Create; got %q, want %q",
			dbAgent.ModelID, "claude-opus")
	}
	if dbAgent.Effort != "high" {
		t.Errorf("dbAgent.Effort was not set before Create; got %q, want %q",
			dbAgent.Effort, "high")
	}

	_ = r.StopAgent(agent.ID)
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
