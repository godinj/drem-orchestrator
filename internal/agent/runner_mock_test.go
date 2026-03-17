package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// mockSessionManager implements SessionManager for testing without tmux.
type mockSessionManager struct {
	mu sync.Mutex

	// CreateAgentSession behavior.
	createCalled  bool
	createSession string
	createCmd     string
	createCwd     string
	createErr     error

	// IsAgentSessionAlive behavior.
	aliveSessions map[string]bool
	aliveErr      error

	// KillAgentSession behavior.
	killedSessions []string
	killErr        error

	// CaptureAgentPane behavior.
	capturedOutput map[string]string
	captureErr     error

	// ListAgentSessions behavior.
	listedSessions []string
	listErr        error

	// WaitForAgentIdle behavior.
	waitExitCode int
	waitErr      error
}

func newMockSessionManager() *mockSessionManager {
	return &mockSessionManager{
		aliveSessions:  make(map[string]bool),
		capturedOutput: make(map[string]string),
	}
}

func (m *mockSessionManager) CreateAgentSession(sessionName, cmd, cwd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalled = true
	m.createSession = sessionName
	m.createCmd = cmd
	m.createCwd = cwd
	return m.createErr
}

func (m *mockSessionManager) IsAgentSessionAlive(sessionName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aliveErr != nil {
		return false, m.aliveErr
	}
	return m.aliveSessions[sessionName], nil
}

func (m *mockSessionManager) KillAgentSession(sessionName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killedSessions = append(m.killedSessions, sessionName)
	return m.killErr
}

func (m *mockSessionManager) CaptureAgentPane(sessionName string, lines int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.captureErr != nil {
		return "", m.captureErr
	}
	return m.capturedOutput[sessionName], nil
}

func (m *mockSessionManager) ListAgentSessions() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listedSessions, nil
}

func (m *mockSessionManager) WaitForAgentIdle(ctx context.Context, sessionName, idleSignalPath string) (int, error) {
	m.mu.Lock()
	exitCode := m.waitExitCode
	waitErr := m.waitErr
	m.mu.Unlock()

	// Block until context is cancelled, simulating a long-running wait.
	<-ctx.Done()
	if waitErr != nil {
		return -1, waitErr
	}
	return exitCode, ctx.Err()
}

// mockTestDB creates an in-memory SQLite database with auto-migration for tests.
func mockTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// Use a unique connection to avoid shared cache conflicts between tests.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get underlying db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// newMockRunner creates a Runner backed by a mock SessionManager and in-memory DB.
func newMockRunner(t *testing.T, mock *mockSessionManager, db *gorm.DB) *Runner {
	t.Helper()
	return &Runner{
		db:              db,
		tmux:            mock,
		tmuxSessionName: "test-session",
		claudeBin:       "/usr/bin/true",
		maxConcurrent:   4,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 4),
		semaphore:       make(chan struct{}, 4),
	}
}

// createTestProject inserts a project and returns it.
func createTestProject(t *testing.T, db *gorm.DB) *model.Project {
	t.Helper()
	p := &model.Project{
		ID:           uuid.New(),
		Name:         "test-project",
		BareRepoPath: "/tmp/test.git",
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("create test project: %v", err)
	}
	return p
}

// createTestTask inserts a task for the given project and returns it.
func createTestTask(t *testing.T, db *gorm.DB, projectID uuid.UUID) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "Implement auth module",
		Description: "Build JWT-based authentication",
		Status:      model.StatusInProgress,
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("create test task: %v", err)
	}
	return task
}

func TestSpawnAgentInWorktree_MockTmux(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)
	task := createTestTask(t, db, project.ID)

	// Mark the mock session as alive so verifySpawn doesn't fire a completion.
	mock.mu.Lock()
	mock.aliveSessions["*"] = true // will be set properly after we know the session name
	mock.mu.Unlock()

	worktreeDir := t.TempDir()
	prompt := "You are a coding agent. Implement the auth module."

	agent, err := r.SpawnAgentInWorktree(task, worktreeDir, model.AgentCoder, prompt)
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Mark the actual session alive for verifySpawn.
	mock.mu.Lock()
	mock.aliveSessions[agent.TmuxSession] = true
	mock.mu.Unlock()

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
		t.Error("agent tmux session is empty")
	}

	// Verify the mock's CreateAgentSession was called.
	mock.mu.Lock()
	if !mock.createCalled {
		t.Error("CreateAgentSession was not called")
	}
	if mock.createCwd != worktreeDir {
		t.Errorf("CreateAgentSession cwd = %q, want %q", mock.createCwd, worktreeDir)
	}
	mock.mu.Unlock()

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

	// Verify agent is in the running map.
	r.mu.Lock()
	_, inRunning := r.running[agent.ID]
	r.mu.Unlock()
	if !inRunning {
		t.Error("agent not found in running map")
	}

	// Clean up: stop the agent to cancel background goroutines.
	_ = r.StopAgent(agent.ID)
}

func TestSpawnAgentInWorktree_SessionFailure(t *testing.T) {
	mock := newMockSessionManager()
	mock.createErr = fmt.Errorf("tmux not available")

	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)
	task := createTestTask(t, db, project.ID)

	worktreeDir := t.TempDir()
	prompt := "Test prompt"

	agent, err := r.SpawnAgentInWorktree(task, worktreeDir, model.AgentCoder, prompt)
	if err == nil {
		t.Fatal("expected error from SpawnAgentInWorktree, got nil")
	}
	if agent != nil {
		t.Errorf("expected nil agent on failure, got %+v", agent)
	}

	// Verify the error is propagated with context.
	if !contains(err.Error(), "tmux not available") {
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

func TestStopAgent_MockTmux(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)
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

	// Add to running map with a cancel func.
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

	// Verify KillAgentSession was called with the correct session name.
	mock.mu.Lock()
	if len(mock.killedSessions) != 1 {
		t.Fatalf("expected 1 killed session, got %d", len(mock.killedSessions))
	}
	if mock.killedSessions[0] != sessionName {
		t.Errorf("killed session = %q, want %q", mock.killedSessions[0], sessionName)
	}
	mock.mu.Unlock()

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
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	unknownID := uuid.New()
	err := r.StopAgent(unknownID)
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
	if !contains(err.Error(), "not running") {
		t.Errorf("error should indicate agent is not running, got: %v", err)
	}

	// Verify no kill calls were made.
	mock.mu.Lock()
	if len(mock.killedSessions) != 0 {
		t.Errorf("expected no killed sessions, got %d", len(mock.killedSessions))
	}
	mock.mu.Unlock()
}

func TestGetAgentOutput_MockTmux(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	agentID := uuid.New()
	sessionName := "test-session/code - output a1b2"
	expectedOutput := "Agent is processing task...\nDone!"

	// Set up captured output in mock.
	mock.mu.Lock()
	mock.capturedOutput[sessionName] = expectedOutput
	mock.mu.Unlock()

	// Add agent to running map.
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID:     agentID,
		TmuxSession: sessionName,
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
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

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
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)
	agentID := uuid.New()
	sessionName := "test-session/code - db-output a1b2"
	expectedOutput := "Output from DB agent"

	// Create agent in DB but not in running map (finished agent).
	dbAgent := &model.Agent{
		ID:          agentID,
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - db-output",
		Status:      model.AgentDead,
		TmuxSession: sessionName,
	}
	if err := db.Create(dbAgent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Set up captured output in mock.
	mock.mu.Lock()
	mock.capturedOutput[sessionName] = expectedOutput
	mock.mu.Unlock()

	output, err := r.GetAgentOutput(agentID)
	if err != nil {
		t.Fatalf("GetAgentOutput: %v", err)
	}
	if output != expectedOutput {
		t.Errorf("output = %q, want %q", output, expectedOutput)
	}
}

func TestCleanupStaleAgents_MockTmux(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)

	// Create a stale agent: heartbeat older than the timeout.
	staleTime := time.Now().Add(-10 * time.Minute)
	staleSession := "test-session/code - stale a1b2"
	staleAgent := &model.Agent{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - stale",
		Status:      model.AgentWorking,
		TmuxSession: staleSession,
		HeartbeatAt: &staleTime,
	}
	if err := db.Create(staleAgent).Error; err != nil {
		t.Fatalf("create stale agent: %v", err)
	}

	// Run cleanup with a 5-minute timeout (stale agent's heartbeat is 10 min old).
	if err := r.CleanupStaleAgents(5 * time.Minute); err != nil {
		t.Fatalf("CleanupStaleAgents: %v", err)
	}

	// Verify KillAgentSession was called for the stale agent's session.
	mock.mu.Lock()
	killed := mock.killedSessions
	mock.mu.Unlock()

	found := false
	for _, s := range killed {
		if s == staleSession {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected stale session %q to be killed, killed: %v", staleSession, killed)
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
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)

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

	// Run cleanup with a 5-minute timeout.
	if err := r.CleanupStaleAgents(5 * time.Minute); err != nil {
		t.Fatalf("CleanupStaleAgents: %v", err)
	}

	// Verify no kill calls were made.
	mock.mu.Lock()
	killedCount := len(mock.killedSessions)
	mock.mu.Unlock()
	if killedCount != 0 {
		t.Errorf("expected no killed sessions, got %d", killedCount)
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

func TestReapOrphanedSessions_MockTmux(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	// Set up mock: two sessions listed, neither is alive (process exited).
	orphan1 := "test-session/code - orphan1 a1b2"
	orphan2 := "test-session/code - orphan2 c3d4"
	mock.mu.Lock()
	mock.listedSessions = []string{orphan1, orphan2}
	mock.aliveSessions[orphan1] = false
	mock.aliveSessions[orphan2] = false
	mock.mu.Unlock()

	// No matching agents in DB or running map.

	reaped, err := r.ReapOrphanedSessions()
	if err != nil {
		t.Fatalf("ReapOrphanedSessions: %v", err)
	}
	if reaped != 2 {
		t.Errorf("reaped = %d, want 2", reaped)
	}

	// Verify both sessions were killed.
	mock.mu.Lock()
	killed := mock.killedSessions
	mock.mu.Unlock()

	if len(killed) != 2 {
		t.Fatalf("expected 2 killed sessions, got %d: %v", len(killed), killed)
	}

	killedSet := make(map[string]bool)
	for _, s := range killed {
		killedSet[s] = true
	}
	if !killedSet[orphan1] {
		t.Errorf("orphan1 %q was not killed", orphan1)
	}
	if !killedSet[orphan2] {
		t.Errorf("orphan2 %q was not killed", orphan2)
	}
}

func TestReapOrphanedSessions_NoOrphans(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)

	activeSession := "test-session/code - active a1b2"

	// Create a working agent in DB for the active session.
	now := time.Now()
	activeAgent := &model.Agent{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		AgentType:   model.AgentCoder,
		Name:        "code - active",
		Status:      model.AgentWorking,
		TmuxSession: activeSession,
		HeartbeatAt: &now,
	}
	if err := db.Create(activeAgent).Error; err != nil {
		t.Fatalf("create active agent: %v", err)
	}

	// Set up mock: the active session is listed.
	mock.mu.Lock()
	mock.listedSessions = []string{activeSession}
	mock.aliveSessions[activeSession] = true
	mock.mu.Unlock()

	reaped, err := r.ReapOrphanedSessions()
	if err != nil {
		t.Fatalf("ReapOrphanedSessions: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d, want 0 (no orphans)", reaped)
	}

	// Verify no kill calls were made.
	mock.mu.Lock()
	killedCount := len(mock.killedSessions)
	mock.mu.Unlock()
	if killedCount != 0 {
		t.Errorf("expected no killed sessions, got %d", killedCount)
	}
}

// TestReapOrphanedSessions_SkipsSupervisor verifies that supervisor sessions
// are not reaped even if they have no matching DB records.
func TestReapOrphanedSessions_SkipsSupervisor(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	supervisorSession := "test-session/supervisor - plan a1b2"
	orphanSession := "test-session/code - orphan c3d4"

	mock.mu.Lock()
	mock.listedSessions = []string{supervisorSession, orphanSession}
	mock.aliveSessions[supervisorSession] = false
	mock.aliveSessions[orphanSession] = false
	mock.mu.Unlock()

	reaped, err := r.ReapOrphanedSessions()
	if err != nil {
		t.Fatalf("ReapOrphanedSessions: %v", err)
	}

	// Only the orphan should be reaped, not the supervisor.
	if reaped != 1 {
		t.Errorf("reaped = %d, want 1 (supervisor should be skipped)", reaped)
	}

	mock.mu.Lock()
	killed := mock.killedSessions
	mock.mu.Unlock()

	if len(killed) != 1 || killed[0] != orphanSession {
		t.Errorf("killed sessions = %v, want [%q]", killed, orphanSession)
	}
}

// TestReapOrphanedSessions_AliveNotReaped verifies that sessions whose
// processes are still alive are not reaped.
func TestReapOrphanedSessions_AliveNotReaped(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	aliveOrphan := "test-session/code - alive-orphan a1b2"

	mock.mu.Lock()
	mock.listedSessions = []string{aliveOrphan}
	mock.aliveSessions[aliveOrphan] = true // process is still alive
	mock.mu.Unlock()

	reaped, err := r.ReapOrphanedSessions()
	if err != nil {
		t.Fatalf("ReapOrphanedSessions: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d, want 0 (alive sessions should not be reaped)", reaped)
	}
}

// TestCleanupStaleAgents_InRunningMap tests cleanup of a stale agent that
// is also present in the running map (uses StopAgent path).
func TestCleanupStaleAgents_InRunningMap(t *testing.T) {
	mock := newMockSessionManager()
	db := mockTestDB(t)
	r := newMockRunner(t, mock, db)

	project := createTestProject(t, db)

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
