package orchestrator

import (
	"log/slog"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// completionTestOrchestrator creates an Orchestrator for testing agent
// completion field population. It provides a real runner and memory manager
// to avoid panics during agent completion processing.
func completionTestOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{
		ID:   projectID,
		Name: "test-project",
	}
	db.Create(&project)

	// Create a minimal runner. No tmux manager is needed — the tests in
	// this file exercise completion processing, not supervisor sessions.
	wtMgr := &FakeWorktreeManager{Default: "main"}
	runner := agent.NewRunner(db, nil, nil, "/usr/bin/false", "", 4, nil)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wtMgr,
		runner:    runner,
		memory:    memory.NewManager(db),
		events:    events,
		logger:    slog.Default().With("component", "test-completion"),
	}
	return o
}

// setupAgentAndTask creates a project, agent, and task for testing.
// Returns the orchestrator, agent ID, and task ID.
func setupAgentAndTask(t *testing.T, o *Orchestrator) (uuid.UUID, uuid.UUID) {
	t.Helper()
	agentID := uuid.New()
	taskID := uuid.New()

	agnt := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "test-agent",
		Status:    model.AgentWorking,
	}
	o.db.Create(&agnt)

	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "test-task",
		Description: "test description",
		Status:      model.StatusInProgress,
	}
	o.db.Create(&task)

	agnt.CurrentTaskID = &taskID
	o.db.Save(&agnt)

	return agentID, taskID
}

// ---------------------------------------------------------------------------
// CompletedAt field population tests
// ---------------------------------------------------------------------------

// TestProcessAgentResult_CompletedAtIsSet verifies that CompletedAt is set
// to approximately the current time when processAgentResult() runs.
func TestProcessAgentResult_CompletedAtIsSet(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	beforeTime := time.Now()
	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	}
	err := o.processAgentResult(comp)
	afterTime := time.Now()

	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.CompletedAt == nil {
		t.Error("CompletedAt should be set, but is nil")
	} else if updatedAgent.CompletedAt.Before(beforeTime) || updatedAgent.CompletedAt.After(afterTime.Add(time.Second)) {
		t.Errorf("CompletedAt %v is outside expected range [%v, %v]",
			updatedAgent.CompletedAt, beforeTime, afterTime)
	}
}

// ---------------------------------------------------------------------------
// ExitReason field population tests
// ---------------------------------------------------------------------------

// TestProcessAgentResult_ExitReason_Success verifies that ExitReason is
// mapped to "success" constant when exit reason is success.
func TestProcessAgentResult_ExitReason_Success(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "success",
			LastTool:    "write",
			ExitSummary: "completed successfully",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.ExitReason != model.ExitReasonSuccess {
		t.Errorf("ExitReason = %q, want %q", updatedAgent.ExitReason, model.ExitReasonSuccess)
	}
}

// TestProcessAgentResult_ExitReason_Error verifies that ExitReason is
// mapped to "error" constant when exit reason is error.
func TestProcessAgentResult_ExitReason_Error(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 1,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "error",
			LastTool:    "bash",
			ExitSummary: "command failed",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.ExitReason != model.ExitReasonError {
		t.Errorf("ExitReason = %q, want %q", updatedAgent.ExitReason, model.ExitReasonError)
	}
}

// TestProcessAgentResult_ExitReason_ContextLimit verifies that ExitReason is
// mapped to "context_limit" constant when context limit is reached.
func TestProcessAgentResult_ExitReason_ContextLimit(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "context_limit",
			LastTool:    "read",
			ExitSummary: "context limit reached",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.ExitReason != model.ExitReasonContextLimit {
		t.Errorf("ExitReason = %q, want %q", updatedAgent.ExitReason, model.ExitReasonContextLimit)
	}
}

// TestProcessAgentResult_ExitReason_Killed verifies that ExitReason is
// mapped to "killed" constant when agent is killed.
func TestProcessAgentResult_ExitReason_Killed(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 137,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "killed",
			LastTool:    "bash",
			ExitSummary: "process was killed",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.ExitReason != model.ExitReasonKilled {
		t.Errorf("ExitReason = %q, want %q", updatedAgent.ExitReason, model.ExitReasonKilled)
	}
}

// TestProcessAgentResult_ExitReason_Timeout verifies that ExitReason is
// mapped to "timeout" constant when timeout occurs.
func TestProcessAgentResult_ExitReason_Timeout(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 124,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "timeout",
			LastTool:    "read",
			ExitSummary: "timeout occurred",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.ExitReason != model.ExitReasonTimeout {
		t.Errorf("ExitReason = %q, want %q", updatedAgent.ExitReason, model.ExitReasonTimeout)
	}
}

// TestProcessAgentResult_ExitReason_NilExitInfo verifies that ExitReason is
// set to default value when ExitInfo is nil.
func TestProcessAgentResult_ExitReason_NilExitInfo(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo:   nil,
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.ExitReason != model.ExitReasonDefault {
		t.Errorf("ExitReason = %q, want %q (default when ExitInfo is nil)", updatedAgent.ExitReason, model.ExitReasonDefault)
	}
}

// ---------------------------------------------------------------------------
// Context monitor field population tests
// ---------------------------------------------------------------------------

// TestProcessAgentResult_TotalCostUSD_Captured verifies that TotalCostUSD is
// captured from the context monitor's last reading.
func TestProcessAgentResult_TotalCostUSD_Captured(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	// Set up agent with context monitor readings
	var agnt model.Agent
	o.db.First(&agnt, "id = ?", agentID)
	agnt.Config = model.JSONField{
		"total_cost_usd": 12.5,
	}
	o.db.Save(&agnt)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "success",
			LastTool:    "write",
			ExitSummary: "completed",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.TotalCostUSD != 12.5 {
		t.Errorf("TotalCostUSD = %f, want 12.5", updatedAgent.TotalCostUSD)
	}
}

// TestProcessAgentResult_FinalContextPct_Captured verifies that FinalContextPct
// is captured from the context monitor's last reading.
func TestProcessAgentResult_FinalContextPct_Captured(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	// Set up agent with context monitor readings
	var agnt model.Agent
	o.db.First(&agnt, "id = ?", agentID)
	agnt.Config = model.JSONField{
		"context_used_pct": 85,
	}
	o.db.Save(&agnt)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "success",
			LastTool:    "write",
			ExitSummary: "completed",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.FinalContextPct != 85 {
		t.Errorf("FinalContextPct = %d, want 85", updatedAgent.FinalContextPct)
	}
}

// ---------------------------------------------------------------------------
// Database persistence tests
// ---------------------------------------------------------------------------

// TestProcessAgentResult_AllFieldsPersisted verifies that all four fields
// (CompletedAt, ExitReason, TotalCostUSD, FinalContextPct) are persisted
// to the database.
func TestProcessAgentResult_AllFieldsPersisted(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	// Set up agent with context monitor readings
	var agnt model.Agent
	o.db.First(&agnt, "id = ?", agentID)
	agnt.Config = model.JSONField{
		"total_cost_usd":   8.75,
		"context_used_pct": 72,
	}
	o.db.Save(&agnt)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "success",
			LastTool:    "write",
			ExitSummary: "completed successfully",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	// Verify all fields are set
	if updatedAgent.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
	if updatedAgent.ExitReason != model.ExitReasonSuccess {
		t.Errorf("ExitReason = %q, want %q", updatedAgent.ExitReason, model.ExitReasonSuccess)
	}
	if updatedAgent.TotalCostUSD != 8.75 {
		t.Errorf("TotalCostUSD = %f, want 8.75", updatedAgent.TotalCostUSD)
	}
	if updatedAgent.FinalContextPct != 72 {
		t.Errorf("FinalContextPct = %d, want 72", updatedAgent.FinalContextPct)
	}

	// Verify they were persisted by querying again
	var persistedAgent model.Agent
	o.db.First(&persistedAgent, "id = ?", agentID)

	if persistedAgent.CompletedAt == nil {
		t.Error("CompletedAt not persisted to database")
	}
	if persistedAgent.ExitReason != model.ExitReasonSuccess {
		t.Errorf("ExitReason not persisted: got %q, want %q", persistedAgent.ExitReason, model.ExitReasonSuccess)
	}
	if persistedAgent.TotalCostUSD != 8.75 {
		t.Errorf("TotalCostUSD not persisted: got %f, want 8.75", persistedAgent.TotalCostUSD)
	}
	if persistedAgent.FinalContextPct != 72 {
		t.Errorf("FinalContextPct not persisted: got %d, want 72", persistedAgent.FinalContextPct)
	}
}

// ---------------------------------------------------------------------------
// Edge case tests
// ---------------------------------------------------------------------------

// TestProcessAgentResult_ZeroCost_IsHandled verifies that zero cost
// (agent that uses minimal API resources) is correctly captured.
func TestProcessAgentResult_ZeroCost_IsHandled(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	var agnt model.Agent
	o.db.First(&agnt, "id = ?", agentID)
	agnt.Config = model.JSONField{
		"total_cost_usd":   0.0,
		"context_used_pct": 10,
	}
	o.db.Save(&agnt)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "success",
			LastTool:    "write",
			ExitSummary: "completed",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.TotalCostUSD != 0.0 {
		t.Errorf("TotalCostUSD = %f, want 0.0", updatedAgent.TotalCostUSD)
	}
}

// TestProcessAgentResult_HighContextUsage_IsHandled verifies that high
// context usage percentage (near limit) is correctly captured.
func TestProcessAgentResult_HighContextUsage_IsHandled(t *testing.T) {
	o := completionTestOrchestrator(t)
	agentID, _ := setupAgentAndTask(t, o)

	var agnt model.Agent
	o.db.First(&agnt, "id = ?", agentID)
	agnt.Config = model.JSONField{
		"total_cost_usd":   45.25,
		"context_used_pct": 95,
	}
	o.db.Save(&agnt)

	comp := agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
		ExitInfo: &agent.ExitInfo{
			ExitReason:  "success",
			LastTool:    "write",
			ExitSummary: "completed",
		},
	}
	err := o.processAgentResult(comp)
	if err != nil {
		t.Fatalf("processAgentResult failed: %v", err)
	}

	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)

	if updatedAgent.FinalContextPct != 95 {
		t.Errorf("FinalContextPct = %d, want 95", updatedAgent.FinalContextPct)
	}
}
