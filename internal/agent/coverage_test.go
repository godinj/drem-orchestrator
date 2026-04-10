package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// NewRunner
// ---------------------------------------------------------------------------

func TestNewRunner(t *testing.T) {
	db := testutil.NewTestDB(t)

	t.Run("with nil tmux manager", func(t *testing.T) {
		r := NewRunner(db, nil, nil, "/usr/bin/claude", "", 4, nil)
		if r == nil {
			t.Fatal("NewRunner returned nil")
		}
		if r.db != db {
			t.Error("db field not set")
		}
		if r.tmuxMgr != nil {
			t.Error("tmuxMgr should be nil when tm is nil")
		}
		if r.tmuxSessionName != "" {
			t.Errorf("tmuxSessionName = %q, want empty when tm is nil", r.tmuxSessionName)
		}
		if r.claudeBin != "/usr/bin/claude" {
			t.Errorf("claudeBin = %q, want /usr/bin/claude", r.claudeBin)
		}
		if r.maxConcurrent != 4 {
			t.Errorf("maxConcurrent = %d, want 4", r.maxConcurrent)
		}
		if r.running == nil {
			t.Error("running map not initialized")
		}
		if r.completions == nil {
			t.Error("completions channel not initialized")
		}
		if r.semaphore == nil {
			t.Error("semaphore channel not initialized")
		}
		if cap(r.completions) != 4 {
			t.Errorf("completions channel cap = %d, want 4", cap(r.completions))
		}
		if cap(r.semaphore) != 4 {
			t.Errorf("semaphore channel cap = %d, want 4", cap(r.semaphore))
		}
		if r.startProcess == nil {
			t.Error("startProcess should be set to StartAgentProcess")
		}
	})
}

// ---------------------------------------------------------------------------
// Simple getters: TmuxSessionName, TmuxManager, ClaudeBin
// ---------------------------------------------------------------------------

func TestRunnerGetters(t *testing.T) {
	r := &Runner{
		tmuxSessionName: "test-session",
		claudeBin:       "/path/to/claude",
		maxConcurrent:   2,
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 2),
		semaphore:       make(chan struct{}, 2),
	}

	t.Run("TmuxSessionName", func(t *testing.T) {
		got := r.TmuxSessionName()
		if got != "test-session" {
			t.Errorf("TmuxSessionName() = %q, want %q", got, "test-session")
		}
	})

	t.Run("TmuxManager nil", func(t *testing.T) {
		got := r.TmuxManager()
		if got != nil {
			t.Errorf("TmuxManager() = %v, want nil", got)
		}
	})

	t.Run("ClaudeBin", func(t *testing.T) {
		got := r.ClaudeBin()
		if got != "/path/to/claude" {
			t.Errorf("ClaudeBin() = %q, want %q", got, "/path/to/claude")
		}
	})
}

// ---------------------------------------------------------------------------
// GetContextUsage
// ---------------------------------------------------------------------------

func TestGetContextUsage(t *testing.T) {
	t.Run("agent not running returns nil", func(t *testing.T) {
		r := &Runner{
			running: make(map[uuid.UUID]*RunningAgent),
		}
		usage := r.GetContextUsage(uuid.New())
		if usage != nil {
			t.Error("expected nil for non-running agent")
		}
	})

	t.Run("agent running with no usage returns nil", func(t *testing.T) {
		agentID := uuid.New()
		r := &Runner{
			running: map[uuid.UUID]*RunningAgent{
				agentID: {AgentID: agentID, ContextUsage: nil},
			},
		}
		usage := r.GetContextUsage(agentID)
		if usage != nil {
			t.Error("expected nil when no usage data yet")
		}
	})

	t.Run("agent running with usage returns usage", func(t *testing.T) {
		agentID := uuid.New()
		expected := &ctxmon.Usage{UsedPercent: 42}
		r := &Runner{
			running: map[uuid.UUID]*RunningAgent{
				agentID: {AgentID: agentID, ContextUsage: expected},
			},
		}
		usage := r.GetContextUsage(agentID)
		if usage == nil {
			t.Fatal("expected non-nil usage")
		}
		if usage.UsedPercent != 42 {
			t.Errorf("UsedPercent = %d, want 42", usage.UsedPercent)
		}
	})
}

// ---------------------------------------------------------------------------
// readLogTail
// ---------------------------------------------------------------------------

func TestReadLogTail(t *testing.T) {
	t.Run("nonexistent file returns empty string", func(t *testing.T) {
		output, err := readLogTail("/nonexistent/path/file.log", 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "" {
			t.Errorf("expected empty string, got %q", output)
		}
	})

	t.Run("small file read completely", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.log")
		content := "hello world"
		os.WriteFile(path, []byte(content), 0o644)

		output, err := readLogTail(path, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != content {
			t.Errorf("output = %q, want %q", output, content)
		}
	})

	t.Run("large file truncated to last N bytes", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.log")
		// Create a file larger than maxBytes
		content := strings.Repeat("abcdefghij", 100) // 1000 bytes
		os.WriteFile(path, []byte(content), 0o644)

		output, err := readLogTail(path, 200)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(output) != 200 {
			t.Errorf("output length = %d, want 200", len(output))
		}
		// Should be the last 200 bytes of the content.
		expected := content[800:]
		if output != expected {
			t.Errorf("output = %q, want %q", output, expected)
		}
	})

	t.Run("file exactly at maxBytes limit", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.log")
		content := strings.Repeat("x", 100)
		os.WriteFile(path, []byte(content), 0o644)

		output, err := readLogTail(path, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != content {
			t.Errorf("output = %q, want %q", output, content)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.log")
		os.WriteFile(path, []byte{}, 0o644)

		output, err := readLogTail(path, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "" {
			t.Errorf("expected empty string for empty file, got %q", output)
		}
	})
}

// ---------------------------------------------------------------------------
// KillStaleProcess
// ---------------------------------------------------------------------------

func TestKillStaleProcess(t *testing.T) {
	r := &Runner{
		running: make(map[uuid.UUID]*RunningAgent),
	}

	t.Run("nil config does not panic", func(t *testing.T) {
		ag := &model.Agent{Config: nil}
		// Should not panic.
		r.KillStaleProcess(ag)
	})

	t.Run("config without pid does not panic", func(t *testing.T) {
		ag := &model.Agent{Config: model.JSONField{"other": "value"}}
		r.KillStaleProcess(ag)
	})

	t.Run("config with zero pid is skipped", func(t *testing.T) {
		ag := &model.Agent{Config: model.JSONField{"pid": float64(0)}}
		r.KillStaleProcess(ag)
	})

	t.Run("config with negative pid is skipped", func(t *testing.T) {
		ag := &model.Agent{Config: model.JSONField{"pid": float64(-1)}}
		r.KillStaleProcess(ag)
	})

	t.Run("config with non-numeric pid is skipped", func(t *testing.T) {
		ag := &model.Agent{Config: model.JSONField{"pid": "not-a-number"}}
		r.KillStaleProcess(ag)
	})

	t.Run("config with valid pid attempts kill", func(t *testing.T) {
		// Use PID 1 which exists but we can't kill (permission denied).
		// The important thing is that FindProcess is called and no panic.
		ag := &model.Agent{Config: model.JSONField{"pid": float64(1)}}
		r.KillStaleProcess(ag)
		// No panic = success. Kill will return permission denied but that's fine.
	})
}

// ---------------------------------------------------------------------------
// GetRunningAgents with entries
// ---------------------------------------------------------------------------

func TestGetRunningAgents_WithEntries(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	r := &Runner{
		running: map[uuid.UUID]*RunningAgent{
			id1: {
				AgentID:      id1,
				TaskID:       uuid.New(),
				WorktreePath: "/tmp/wt1",
				Branch:       "branch-1",
				TmuxSession:  "session-1",
				LogPath:      "/tmp/wt1/log",
				StartedAt:    time.Now(),
			},
			id2: {
				AgentID:      id2,
				TaskID:       uuid.New(),
				WorktreePath: "/tmp/wt2",
				Branch:       "branch-2",
				TmuxSession:  "session-2",
				LogPath:      "/tmp/wt2/log",
				StartedAt:    time.Now(),
			},
		},
	}

	agents := r.GetRunningAgents()
	if len(agents) != 2 {
		t.Fatalf("GetRunningAgents() returned %d agents, want 2", len(agents))
	}

	// Verify returned agents are copies with correct data.
	found := map[uuid.UUID]bool{}
	for _, a := range agents {
		found[a.AgentID] = true
		if a.WorktreePath == "" {
			t.Errorf("agent %s has empty worktree path", a.AgentID)
		}
	}
	if !found[id1] || !found[id2] {
		t.Error("not all expected agents found in result")
	}
}

// ---------------------------------------------------------------------------
// DrainCompletions with items
// ---------------------------------------------------------------------------

func TestDrainCompletions_WithItems(t *testing.T) {
	r := &Runner{
		completions: make(chan Completion, 4),
	}

	// Add completions.
	id1 := uuid.New()
	id2 := uuid.New()
	r.completions <- Completion{AgentID: id1, ReturnCode: 0}
	r.completions <- Completion{AgentID: id2, ReturnCode: 1, ExitInfo: &ExitInfo{ExitReason: "error"}}

	results := r.DrainCompletions()
	if len(results) != 2 {
		t.Fatalf("DrainCompletions() returned %d, want 2", len(results))
	}

	// Verify completions are drained in order.
	if results[0].AgentID != id1 {
		t.Errorf("first completion AgentID = %s, want %s", results[0].AgentID, id1)
	}
	if results[0].ReturnCode != 0 {
		t.Errorf("first completion ReturnCode = %d, want 0", results[0].ReturnCode)
	}
	if results[1].AgentID != id2 {
		t.Errorf("second completion AgentID = %s, want %s", results[1].AgentID, id2)
	}
	if results[1].ReturnCode != 1 {
		t.Errorf("second completion ReturnCode = %d, want 1", results[1].ReturnCode)
	}
	if results[1].ExitInfo == nil || results[1].ExitInfo.ExitReason != "error" {
		t.Error("second completion should have ExitInfo with reason 'error'")
	}

	// Subsequent drain should return empty.
	after := r.DrainCompletions()
	if len(after) != 0 {
		t.Errorf("subsequent DrainCompletions() returned %d, want 0", len(after))
	}
}

// ---------------------------------------------------------------------------
// loadParentTitle
// ---------------------------------------------------------------------------

func TestLoadParentTitle(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:              db,
		tmuxSessionName: "test",
		running:         make(map[uuid.UUID]*RunningAgent),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	t.Run("nil parent returns unknown", func(t *testing.T) {
		task := &model.Task{
			ID:           uuid.New(),
			ProjectID:    project.ID,
			ParentTaskID: nil,
		}
		got := r.loadParentTitle(task)
		if got != "unknown" {
			t.Errorf("loadParentTitle() = %q, want %q", got, "unknown")
		}
	})

	t.Run("parent not found returns unknown", func(t *testing.T) {
		missingID := uuid.New()
		task := &model.Task{
			ID:           uuid.New(),
			ProjectID:    project.ID,
			ParentTaskID: &missingID,
		}
		got := r.loadParentTitle(task)
		if got != "unknown" {
			t.Errorf("loadParentTitle() = %q, want %q", got, "unknown")
		}
	})

	t.Run("parent found returns title", func(t *testing.T) {
		parent := testutil.CreateTask(t, db, project.ID, "Parent Task Title", model.StatusInProgress)
		task := &model.Task{
			ID:           uuid.New(),
			ProjectID:    project.ID,
			ParentTaskID: &parent.ID,
		}
		got := r.loadParentTitle(task)
		if got != "Parent Task Title" {
			t.Errorf("loadParentTitle() = %q, want %q", got, "Parent Task Title")
		}
	})
}

// ---------------------------------------------------------------------------
// buildAgentNames
// ---------------------------------------------------------------------------

func TestBuildAgentNames(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:              db,
		tmuxSessionName: "drem",
		running:         make(map[uuid.UUID]*RunningAgent),
	}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	t.Run("planner agent names", func(t *testing.T) {
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     "Add authentication",
		}
		agentID := uuid.New()
		name, session := r.buildAgentNames(task, model.AgentPlanner, agentID)

		if !strings.HasPrefix(name, "plan - ") {
			t.Errorf("planner name should start with 'plan - ', got %q", name)
		}
		if !strings.Contains(name, "Add authentication") {
			t.Errorf("planner name should contain task title, got %q", name)
		}
		shortID := agentID.String()[:shortIDLen]
		if !strings.Contains(session, shortID) {
			t.Errorf("session should contain short ID %q, got %q", shortID, session)
		}
		if !strings.HasPrefix(session, "drem/") {
			t.Errorf("session should start with 'drem/', got %q", session)
		}
	})

	t.Run("coder agent names with parent", func(t *testing.T) {
		parent := testutil.CreateTask(t, db, project.ID, "Feature X", model.StatusInProgress)
		task := &model.Task{
			ID:           uuid.New(),
			ProjectID:    project.ID,
			Title:        "Implement module A",
			ParentTaskID: &parent.ID,
		}
		agentID := uuid.New()
		name, _ := r.buildAgentNames(task, model.AgentCoder, agentID)

		if !strings.HasPrefix(name, "code - ") {
			t.Errorf("coder name should start with 'code - ', got %q", name)
		}
		if !strings.Contains(name, "Feature X") {
			t.Errorf("coder name should contain parent title, got %q", name)
		}
		if !strings.Contains(name, "Implement module A") {
			t.Errorf("coder name should contain subtask title, got %q", name)
		}
	})

	t.Run("researcher agent names", func(t *testing.T) {
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     "Research API options",
		}
		agentID := uuid.New()
		name, _ := r.buildAgentNames(task, model.AgentResearcher, agentID)

		if !strings.HasPrefix(name, "research - ") {
			t.Errorf("researcher name should start with 'research - ', got %q", name)
		}
	})

	t.Run("reviewer agent names", func(t *testing.T) {
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     "Review changes",
		}
		agentID := uuid.New()
		name, _ := r.buildAgentNames(task, model.AgentReviewer, agentID)

		if !strings.HasPrefix(name, "review - ") {
			t.Errorf("reviewer name should start with 'review - ', got %q", name)
		}
	})

	t.Run("fixer agent names", func(t *testing.T) {
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     "Fix lint errors",
		}
		agentID := uuid.New()
		name, _ := r.buildAgentNames(task, model.AgentFixer, agentID)

		if !strings.HasPrefix(name, "fix - ") {
			t.Errorf("fixer name should start with 'fix - ', got %q", name)
		}
	})

	t.Run("title with slashes replaced", func(t *testing.T) {
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     "internal/agent/runner.go",
		}
		agentID := uuid.New()
		name, _ := r.buildAgentNames(task, model.AgentPlanner, agentID)

		if strings.Contains(name, "/") {
			t.Errorf("name should not contain '/' (slashes replaced), got %q", name)
		}
	})

	t.Run("long title is truncated", func(t *testing.T) {
		longTitle := strings.Repeat("A", 100)
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     longTitle,
		}
		agentID := uuid.New()
		name, _ := r.buildAgentNames(task, model.AgentPlanner, agentID)

		// Name = "plan - <truncated-title>", total should be reasonable length.
		if len([]rune(name)) > maxDisplayNameLen+20 {
			t.Errorf("name is too long: %d runes, expected <= %d", len([]rune(name)), maxDisplayNameLen+20)
		}
	})

	t.Run("session name sanitized", func(t *testing.T) {
		task := &model.Task{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Title:     "task.with:special.chars",
		}
		agentID := uuid.New()
		_, session := r.buildAgentNames(task, model.AgentPlanner, agentID)

		if strings.Contains(session, ".") || strings.Contains(session, ":") {
			t.Errorf("session should have dots and colons replaced, got %q", session)
		}
	})
}

// ---------------------------------------------------------------------------
// heartbeatLoop — cancellation
// ---------------------------------------------------------------------------

func TestHeartbeatLoop_CancelledImmediately(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := &Runner{
		db:      db,
		running: make(map[uuid.UUID]*RunningAgent),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	done := make(chan struct{})
	go func() {
		r.heartbeatLoop(ctx, uuid.New())
		close(done)
	}()

	select {
	case <-done:
		// Good — heartbeatLoop exited promptly.
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeatLoop did not exit after context cancel")
	}
}

// ---------------------------------------------------------------------------
// enrichCompletion edge cases
// ---------------------------------------------------------------------------

func TestEnrichCompletion_NoMatchingAgent(t *testing.T) {
	projectDir := t.TempDir()

	// Write an exit log with entries for a different agent.
	otherID := uuid.New()
	entry := agentmon.ExitLogEntry{
		AgentID:    otherID.String(),
		TaskID:     uuid.New().String(),
		ExitReason: "success",
	}
	data, _ := json.Marshal(entry)
	exitLogPath := filepath.Join(projectDir, "exit-log.jsonl")
	os.WriteFile(exitLogPath, append(data, '\n'), 0o644)

	comp := &Completion{AgentID: uuid.New(), ReturnCode: 0}
	enrichCompletion(comp, projectDir)

	if comp.ExitInfo != nil {
		t.Error("ExitInfo should be nil when no matching agent in log")
	}
}

func TestEnrichCompletion_EmptyLogFile(t *testing.T) {
	projectDir := t.TempDir()

	// Create an empty exit-log.jsonl.
	exitLogPath := filepath.Join(projectDir, "exit-log.jsonl")
	os.WriteFile(exitLogPath, []byte{}, 0o644)

	comp := &Completion{AgentID: uuid.New(), ReturnCode: 0}
	enrichCompletion(comp, projectDir)

	if comp.ExitInfo != nil {
		t.Error("ExitInfo should be nil for empty log file")
	}
}

func TestEnrichCompletion_ExitSummaryPopulated(t *testing.T) {
	agentID := uuid.New()
	projectDir := t.TempDir()

	entry := agentmon.ExitLogEntry{
		AgentID:    agentID.String(),
		TaskID:     uuid.New().String(),
		ExitReason: "context_limit",
		LastTool:   "Bash",
		Summary:    "Ran out of context window",
	}
	data, _ := json.Marshal(entry)
	exitLogPath := filepath.Join(projectDir, "exit-log.jsonl")
	os.WriteFile(exitLogPath, append(data, '\n'), 0o644)

	comp := &Completion{AgentID: agentID, ReturnCode: 1}
	enrichCompletion(comp, projectDir)

	if comp.ExitInfo == nil {
		t.Fatal("ExitInfo should be populated")
	}
	if comp.ExitInfo.ExitSummary != "Ran out of context window" {
		t.Errorf("ExitSummary = %q, want %q", comp.ExitInfo.ExitSummary, "Ran out of context window")
	}
	if comp.ExitInfo.ExitReason != "context_limit" {
		t.Errorf("ExitReason = %q, want %q", comp.ExitInfo.ExitReason, "context_limit")
	}
}

// ---------------------------------------------------------------------------
// monitorAgent — process exits immediately
// ---------------------------------------------------------------------------

func TestMonitorAgent_ProcessExitsImmediately(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:          db,
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	agentID := uuid.New()
	worktreeDir := t.TempDir()

	// Create .claude directory for idle signal path.
	os.MkdirAll(filepath.Join(worktreeDir, ".claude"), 0o755)

	// Create a mock process that is already done.
	proc := &AgentProcess{
		logPath:  filepath.Join(worktreeDir, ".claude", "agent-output.log"),
		pid:      99999,
		done:     make(chan struct{}),
		exitCode: 0,
	}
	close(proc.done) // Process already exited.

	// Add to running map and acquire semaphore.
	r.mu.Lock()
	r.running[agentID] = &RunningAgent{
		AgentID: agentID,
		Process: proc,
	}
	r.mu.Unlock()
	r.semaphore <- struct{}{}

	// Run monitorAgent in a goroutine.
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		r.monitorAgent(ctx, agentID, proc, worktreeDir)
		close(done)
	}()

	// Wait for monitorAgent to complete.
	select {
	case <-done:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("monitorAgent did not complete in time")
	}

	// Should have produced a completion.
	completions := r.DrainCompletions()
	if len(completions) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(completions))
	}
	if completions[0].AgentID != agentID {
		t.Errorf("completion AgentID = %s, want %s", completions[0].AgentID, agentID)
	}
	if completions[0].ReturnCode != 0 {
		t.Errorf("completion ReturnCode = %d, want 0", completions[0].ReturnCode)
	}

	// Agent should be removed from running map.
	r.mu.Lock()
	_, stillRunning := r.running[agentID]
	r.mu.Unlock()
	if stillRunning {
		t.Error("agent should be removed from running map after monitor completes")
	}
}

func TestMonitorAgent_ContextCancelled(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:          db,
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	agentID := uuid.New()
	worktreeDir := t.TempDir()
	os.MkdirAll(filepath.Join(worktreeDir, ".claude"), 0o755)

	// Create a process that never exits on its own.
	proc := &AgentProcess{
		logPath: filepath.Join(worktreeDir, ".claude", "agent-output.log"),
		pid:     99999,
		done:    make(chan struct{}),
	}

	r.mu.Lock()
	r.running[agentID] = &RunningAgent{AgentID: agentID, Process: proc}
	r.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.monitorAgent(ctx, agentID, proc, worktreeDir)
		close(done)
	}()

	// Cancel context — monitor should exit without sending completion.
	cancel()

	select {
	case <-done:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("monitorAgent did not exit after context cancel")
	}

	// No completions should be sent.
	completions := r.DrainCompletions()
	if len(completions) != 0 {
		t.Errorf("expected 0 completions after cancel, got %d", len(completions))
	}
}

func TestMonitorAgent_IdleSignal(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:          db,
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	agentID := uuid.New()
	worktreeDir := t.TempDir()
	claudeDir := filepath.Join(worktreeDir, ".claude")
	os.MkdirAll(claudeDir, 0o755)

	// Create a process that will exit when done is closed.
	proc := &AgentProcess{
		logPath: filepath.Join(claudeDir, "agent-output.log"),
		pid:     99999,
		done:    make(chan struct{}),
	}

	r.mu.Lock()
	r.running[agentID] = &RunningAgent{AgentID: agentID, Process: proc}
	r.mu.Unlock()
	r.semaphore <- struct{}{}

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		r.monitorAgent(ctx, agentID, proc, worktreeDir)
		close(done)
	}()

	// Write the idle signal file to trigger shutdown.
	idleSignal := filepath.Join(claudeDir, "agent-idle")
	os.WriteFile(idleSignal, []byte{}, 0o644)

	// Since SendExit won't work (no real process), the monitor will wait
	// for sendExitGracePeriod then call Kill. Since proc.done is never closed
	// by a real process, we close it to simulate process exit after SendExit.
	go func() {
		// Wait for monitor to detect idle and call SendExit, then simulate exit.
		time.Sleep(1 * time.Second)
		close(proc.done)
	}()

	select {
	case <-done:
		// Good — monitor completed.
	case <-time.After(10 * time.Second):
		t.Fatal("monitorAgent did not complete after idle signal")
	}

	completions := r.DrainCompletions()
	if len(completions) != 1 {
		t.Fatalf("expected 1 completion after idle, got %d", len(completions))
	}
}

// ---------------------------------------------------------------------------
// ReapOrphanedSessions — nil tmux manager
// ---------------------------------------------------------------------------

func TestReapOrphanedSessions_NilTmuxManager(t *testing.T) {
	r := &Runner{
		tmuxMgr: nil,
		running: make(map[uuid.UUID]*RunningAgent),
	}

	reaped, err := r.ReapOrphanedSessions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
}

// ---------------------------------------------------------------------------
// spawnNewAgent — max concurrent reached
// ---------------------------------------------------------------------------

func TestSpawnNewAgent_MaxConcurrentReached(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:              db,
		startProcess:    StartAgentProcess,
		tmuxSessionName: "test-session",
		claudeBin:       "/bin/false",
		maxConcurrent:   1,
		agentConfigs:    func(model.AgentType) model.AgentCLIConfig { return model.AgentCLIConfig{Effort: "medium"} },
		running:         make(map[uuid.UUID]*RunningAgent),
		completions:     make(chan Completion, 1),
		semaphore:       make(chan struct{}, 1),
	}

	// Fill the semaphore to max.
	r.semaphore <- struct{}{}

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Test task", model.StatusInProgress)
	worktreeDir := t.TempDir()

	_, err := r.spawnNewAgent(&task, worktreeDir, "test-branch", model.AgentCoder, "test prompt")
	if err == nil {
		t.Fatal("expected error when max concurrent reached")
	}
	if !strings.Contains(err.Error(), "max concurrent") {
		t.Errorf("error should mention max concurrent, got: %v", err)
	}

	// Release the semaphore we manually acquired.
	<-r.semaphore
}

// ---------------------------------------------------------------------------
// spawnNewAgent — estimated_files context
// ---------------------------------------------------------------------------

func TestSpawnNewAgent_EstimatedFilesContext(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)
	r := newMockRunner(t, db, claudeBin)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")

	// Create task with estimated_files context.
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   project.ID,
		Title:       "Task with estimated files",
		Description: "Test",
		Status:      model.StatusInProgress,
		Context: model.JSONField{
			"estimated_files": []any{"file1.go", "file2.go"},
		},
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreeDir := t.TempDir()
	agent, err := r.spawnNewAgent(&task, worktreeDir, "test-branch", model.AgentCoder, "test prompt")
	if err != nil {
		t.Fatalf("spawnNewAgent: %v", err)
	}

	// Verify the agent was created and has an activity monitor.
	r.mu.Lock()
	ra := r.running[agent.ID]
	r.mu.Unlock()
	if ra == nil {
		t.Fatal("agent not in running map")
	}
	if ra.ActivityMon == nil {
		t.Error("ActivityMon should be set when estimated_files is provided")
	}

	// Clean up.
	_ = r.StopAgent(agent.ID)
}

// ---------------------------------------------------------------------------
// StopAgent with running process
// ---------------------------------------------------------------------------

func TestStopAgent_WithProcess(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	// Create a script that sleeps and exits on SIGTERM.
	scriptPath := filepath.Join(binDir, "fake-claude")
	script := "#!/bin/sh\ntrap 'exit 0' TERM\ncat > /dev/null\nwhile true; do sleep 1 & wait; done\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	r := newMockRunner(t, db, scriptPath)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Implement feature", model.StatusInProgress)

	worktreeDir := t.TempDir()
	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, "Test prompt")
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Give process time to start.
	time.Sleep(200 * time.Millisecond)

	err = r.StopAgent(agent.ID)
	if err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	// Verify agent is removed from running map.
	r.mu.Lock()
	_, inRunning := r.running[agent.ID]
	r.mu.Unlock()
	if inRunning {
		t.Error("agent should be removed from running map after StopAgent")
	}

	// Verify DB status is dead.
	var dbAgent model.Agent
	if err := db.First(&dbAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatalf("agent not found in DB: %v", err)
	}
	if dbAgent.Status != model.AgentDead {
		t.Errorf("status = %q, want %q", dbAgent.Status, model.AgentDead)
	}
}

// ---------------------------------------------------------------------------
// GetAgentOutput — agent in DB with empty WorktreePath
// ---------------------------------------------------------------------------

func TestGetAgentOutput_EmptyWorktreePath(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := newMockRunner(t, db, "/bin/true")

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	agentID := uuid.New()

	dbAgent := &model.Agent{
		ID:           agentID,
		ProjectID:    project.ID,
		AgentType:    model.AgentCoder,
		Name:         "code - test",
		Status:       model.AgentDead,
		WorktreePath: "", // Empty worktree path.
	}
	if err := db.Create(dbAgent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	output, err := r.GetAgentOutput(agentID)
	if err != nil {
		t.Fatalf("GetAgentOutput: %v", err)
	}
	if output != "" {
		t.Errorf("expected empty output for empty worktree path, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// spawnNewAgent — empty branch derives from path
// ---------------------------------------------------------------------------

func TestSpawnNewAgent_EmptyBranchDerivesFromPath(t *testing.T) {
	db := testutil.NewTestDB(t)

	binDir := t.TempDir()
	claudeBin := writeFakeClaudeBin(t, binDir, 0)
	r := newMockRunner(t, db, claudeBin)

	project := testutil.CreateProject(t, db, "test-project", "/tmp/test.git", "")
	task := testutil.CreateTask(t, db, project.ID, "Test task", model.StatusInProgress)

	worktreeDir := t.TempDir()

	// SpawnAgentInWorktree passes empty branch.
	agent, err := r.SpawnAgentInWorktree(&task, worktreeDir, model.AgentCoder, "test prompt")
	if err != nil {
		t.Fatalf("SpawnAgentInWorktree: %v", err)
	}

	// Verify the running agent has a branch derived from the path.
	r.mu.Lock()
	ra := r.running[agent.ID]
	r.mu.Unlock()
	if ra == nil {
		t.Fatal("agent not in running map")
	}
	expectedBranch := filepath.Base(worktreeDir)
	if ra.Branch != expectedBranch {
		t.Errorf("branch = %q, want %q (derived from path)", ra.Branch, expectedBranch)
	}

	// DB record should have empty branch (it stores the raw dbBranch).
	var dbAgent model.Agent
	db.First(&dbAgent, "id = ?", agent.ID)
	if dbAgent.WorktreeBranch != "" {
		t.Errorf("DB WorktreeBranch = %q, want empty", dbAgent.WorktreeBranch)
	}

	_ = r.StopAgent(agent.ID)
}

// ---------------------------------------------------------------------------
// Process argument verification — StartAgentProcess flags
// ---------------------------------------------------------------------------

func TestStartAgentProcess_Arguments(t *testing.T) {
	cwd := t.TempDir()
	scriptDir := t.TempDir()

	// Create a script that writes its arguments to a file and exits.
	argsFile := filepath.Join(cwd, "args.txt")
	scriptPath := filepath.Join(scriptDir, "fake-claude")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %s\ncat > /dev/null\n", argsFile)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	promptPath := filepath.Join(cwd, "prompt.md")
	os.WriteFile(promptPath, []byte("test prompt"), 0o644)

	ctx := context.Background()
	proc, err := StartAgentProcess(ctx, scriptPath, promptPath, cwd, []string{"--effort", "medium"})
	if err != nil {
		t.Fatalf("StartAgentProcess: %v", err)
	}
	proc.Wait()

	// Read the args file.
	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := strings.TrimSpace(string(argsData))

	// Verify --dangerously-skip-permissions flag is present.
	if !strings.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("args should contain --dangerously-skip-permissions, got: %q", args)
	}

	// Verify --effort medium flag is present (passed via extraArgs).
	if !strings.Contains(args, "--effort medium") {
		t.Errorf("args should contain '--effort medium', got: %q", args)
	}

	// Verify -p - flags for pipe mode.
	if !strings.Contains(args, "-p -") {
		t.Errorf("args should contain '-p -', got: %q", args)
	}
}

// ---------------------------------------------------------------------------
// StartAgentProcess — nonexistent binary
// ---------------------------------------------------------------------------

func TestStartAgentProcess_NonexistentBinary(t *testing.T) {
	cwd := t.TempDir()
	promptPath := filepath.Join(cwd, "prompt.md")
	os.WriteFile(promptPath, []byte("test"), 0o644)

	_, err := StartAgentProcess(context.Background(), "/nonexistent/binary", promptPath, cwd, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

// ---------------------------------------------------------------------------
// StartAgentProcess — process exit code
// ---------------------------------------------------------------------------

func TestStartAgentProcess_NonZeroExit(t *testing.T) {
	cwd := t.TempDir()
	scriptDir := t.TempDir()

	// Create a script that exits with code 42.
	scriptPath := filepath.Join(scriptDir, "fake-claude")
	script := "#!/bin/sh\ncat > /dev/null\nexit 42\n"
	os.WriteFile(scriptPath, []byte(script), 0o755)

	promptPath := filepath.Join(cwd, "prompt.md")
	os.WriteFile(promptPath, []byte("test prompt"), 0o644)

	proc, err := StartAgentProcess(context.Background(), scriptPath, promptPath, cwd, nil)
	if err != nil {
		t.Fatalf("StartAgentProcess: %v", err)
	}

	exitCode, _ := proc.Wait()
	if exitCode != 42 {
		t.Errorf("exit code = %d, want 42", exitCode)
	}
}

// ---------------------------------------------------------------------------
// Completion and ExitInfo types
// ---------------------------------------------------------------------------

func TestCompletionStruct(t *testing.T) {
	t.Run("completion with nil ExitInfo", func(t *testing.T) {
		c := Completion{
			AgentID:    uuid.New(),
			ReturnCode: 0,
			ExitInfo:   nil,
		}
		if c.ExitInfo != nil {
			t.Error("ExitInfo should be nil")
		}
	})

	t.Run("completion with ExitInfo", func(t *testing.T) {
		c := Completion{
			AgentID:    uuid.New(),
			ReturnCode: 1,
			ExitInfo: &ExitInfo{
				ExitReason:  "error",
				LastTool:    "Bash",
				ExitSummary: "Build failed",
			},
		}
		if c.ExitInfo.ExitReason != "error" {
			t.Errorf("ExitReason = %q, want error", c.ExitInfo.ExitReason)
		}
		if c.ExitInfo.LastTool != "Bash" {
			t.Errorf("LastTool = %q, want Bash", c.ExitInfo.LastTool)
		}
		if c.ExitInfo.ExitSummary != "Build failed" {
			t.Errorf("ExitSummary = %q, want Build failed", c.ExitInfo.ExitSummary)
		}
	})
}

// ---------------------------------------------------------------------------
// writeAgentMetadata — round-trip verification
// ---------------------------------------------------------------------------

func TestWriteAgentMetadata_RoundTrip(t *testing.T) {
	agentID := uuid.New()
	taskID := uuid.New()
	claudeDir := t.TempDir()

	err := writeAgentMetadata(claudeDir, agentID, taskID)
	if err != nil {
		t.Fatalf("writeAgentMetadata: %v", err)
	}

	// Read and parse to verify round-trip.
	data, err := os.ReadFile(filepath.Join(claudeDir, "agent-metadata.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify UUIDs survive the round trip.
	parsedAgentID, err := uuid.Parse(parsed["agent_id"])
	if err != nil {
		t.Fatalf("parse agent_id: %v", err)
	}
	if parsedAgentID != agentID {
		t.Errorf("agent_id = %s, want %s", parsedAgentID, agentID)
	}
	parsedTaskID, err := uuid.Parse(parsed["task_id"])
	if err != nil {
		t.Fatalf("parse task_id: %v", err)
	}
	if parsedTaskID != taskID {
		t.Errorf("task_id = %s, want %s", parsedTaskID, taskID)
	}
}

// ---------------------------------------------------------------------------
// monitorAgent — non-zero exit code
// ---------------------------------------------------------------------------

func TestMonitorAgent_NonZeroExitCode(t *testing.T) {
	db := testutil.NewTestDB(t)

	r := &Runner{
		db:          db,
		running:     make(map[uuid.UUID]*RunningAgent),
		completions: make(chan Completion, 4),
		semaphore:   make(chan struct{}, 4),
	}

	agentID := uuid.New()
	worktreeDir := t.TempDir()
	os.MkdirAll(filepath.Join(worktreeDir, ".claude"), 0o755)

	proc := &AgentProcess{
		logPath:  filepath.Join(worktreeDir, ".claude", "agent-output.log"),
		pid:      99999,
		done:     make(chan struct{}),
		exitCode: 1,
	}
	close(proc.done)

	r.mu.Lock()
	r.running[agentID] = &RunningAgent{AgentID: agentID, Process: proc}
	r.mu.Unlock()
	r.semaphore <- struct{}{}

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		r.monitorAgent(ctx, agentID, proc, worktreeDir)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitorAgent did not complete")
	}

	completions := r.DrainCompletions()
	if len(completions) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(completions))
	}
	if completions[0].ReturnCode != 1 {
		t.Errorf("ReturnCode = %d, want 1", completions[0].ReturnCode)
	}
}
