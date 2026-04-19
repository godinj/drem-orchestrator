package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// setupClassifyingTest creates an Orchestrator with a real DB for testing
// classifier agent orchestration. Returns the orchestrator and project ID.
// The orchestrator has no runner (nil), suitable for testing completion handlers.
func setupClassifyingTest(t *testing.T) (*Orchestrator, uuid.UUID) {
	t.Helper()

	gormDB, err := dbpkg.Init(":memory:")
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}

	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "classifying-test-project",
		BareRepoPath:  "/tmp/test-classifying",
		DefaultBranch: "main",
	}
	if err := gormDB.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:              gormDB,
		projectID:       projectID,
		worktree:        &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"},
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "classifying-test"),
	}

	return orch, projectID
}

// setupClassifyingTestWithRunner creates an Orchestrator backed by a real bare
// repo and agent runner, suitable for testing processClassifyingTasks which
// needs to spawn agents via the runner.
func setupClassifyingTestWithRunner(t *testing.T) (*Orchestrator, uuid.UUID) {
	t.Helper()

	bareRepo := testutil.InitBareRepoWithMainWorktree(t)
	defaultBranch := testutil.GetDefaultBranch(t, bareRepo)
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	wt := worktree.NewManager(bareRepo, defaultBranch)

	project := model.Project{
		ID:            projectID,
		Name:          "classifying-runner-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: defaultBranch,
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a fake Claude binary that reads stdin and exits 0.
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "fake-claude")
	script := "#!/bin/sh\ncat > /dev/null\nexit 0\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude binary: %v", err)
	}

	runner := agent.NewRunner(db, nil, wt, fakeBin, "", 4, nil)

	orch := &Orchestrator{
		db:              db,
		projectID:       projectID,
		worktree:        wt,
		runner:          runner,
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "classifying-runner-test"),
	}

	return orch, projectID
}

func TestProcessClassifyingTasks_NilRunner_Skips(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	// Create a task in CLASSIFYING with no assigned agent.
	testutil.CreateTask(t, orch.db, projectID, "Classify this task", model.StatusClassifying)

	// With nil runner, processClassifyingTasks should return early.
	orch.processClassifyingTasks()

	// No agents should have been created.
	var agents []model.Agent
	if err := orch.db.Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents with nil runner, got %d", len(agents))
	}
}

func TestProcessClassifyingTasks_SpawnsAgentForUnassignedTask(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create a task in CLASSIFYING with no assigned agent.
	task := testutil.CreateTask(t, orch.db, projectID, "Classify this task", model.StatusClassifying)

	orch.processClassifyingTasks()

	// Reload the task to check AssignedAgentID was set.
	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.AssignedAgentID == nil {
		t.Fatal("expected task.AssignedAgentID to be set after processClassifyingTasks")
	}

	// Verify the agent record exists with classifier type.
	var ag model.Agent
	if err := orch.db.First(&ag, "id = ?", *reloaded.AssignedAgentID).Error; err != nil {
		t.Fatalf("load assigned agent: %v", err)
	}
	if ag.AgentType != model.AgentClassifier {
		t.Errorf("agent type = %q, want %q", ag.AgentType, model.AgentClassifier)
	}
	if ag.WorktreePath == "" {
		t.Error("agent WorktreePath should be set (main worktree)")
	}

	// Clean up: stop the spawned agent to cancel monitoring goroutines.
	if orch.runner != nil {
		_ = orch.runner.StopAgent(ag.ID)
	}
}

func TestProcessClassifyingTasks_SetsHeartbeatAt(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Classify with heartbeat", model.StatusClassifying)
	before := time.Now()

	orch.processClassifyingTasks()

	// Reload task to get the assigned agent ID.
	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.AssignedAgentID == nil {
		t.Fatal("expected task.AssignedAgentID to be set")
	}

	// Load the created agent and verify HeartbeatAt is set.
	var ag model.Agent
	if err := orch.db.First(&ag, "id = ?", *reloaded.AssignedAgentID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}
	if ag.HeartbeatAt == nil {
		t.Fatal("expected agent.HeartbeatAt to be non-nil, got nil")
	}

	// HeartbeatAt should be approximately now (within 5 seconds).
	if ag.HeartbeatAt.Before(before) {
		t.Errorf("HeartbeatAt %v is before test start %v", ag.HeartbeatAt, before)
	}
	if ag.HeartbeatAt.After(before.Add(5 * time.Second)) {
		t.Errorf("HeartbeatAt %v is more than 5s after test start %v", ag.HeartbeatAt, before)
	}

	// Clean up: stop the spawned agent to cancel monitoring goroutines.
	if orch.runner != nil {
		_ = orch.runner.StopAgent(ag.ID)
	}
}

func TestProcessClassifyingTasks_SkipsAssignedTask(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create a task in CLASSIFYING that already has an assigned agent.
	task := testutil.CreateTask(t, orch.db, projectID, "Already assigned", model.StatusClassifying)
	existingAgent := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)
	task.AssignedAgentID = &existingAgent.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("assign agent to task: %v", err)
	}

	orch.processClassifyingTasks()

	// No new agents should have been created.
	var agents []model.Agent
	if err := orch.db.Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("expected 1 agent (the existing one), got %d", len(agents))
	}
	if agents[0].ID != existingAgent.ID {
		t.Errorf("agent ID = %s, want %s (original)", agents[0].ID, existingAgent.ID)
	}
}

func TestProcessClassifyingTasks_SetsHeartbeatAtOnAgent(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create a task in CLASSIFYING with no assigned agent.
	task := testutil.CreateTask(t, orch.db, projectID, "Classify with heartbeat", model.StatusClassifying)

	orch.processClassifyingTasks()

	// Reload task to get the assigned agent ID.
	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.AssignedAgentID == nil {
		t.Fatal("expected task.AssignedAgentID to be set")
	}

	// Load the created agent and verify HeartbeatAt is set.
	var ag model.Agent
	if err := orch.db.First(&ag, "id = ?", *reloaded.AssignedAgentID).Error; err != nil {
		t.Fatalf("load assigned agent: %v", err)
	}
	if ag.HeartbeatAt == nil {
		t.Fatal("expected agent.HeartbeatAt to be non-nil on startup; classifier agents must set heartbeat_at so the reconciler can detect dead agents")
	}

	// Clean up: stop the spawned agent to cancel monitoring goroutines.
	if orch.runner != nil {
		_ = orch.runner.StopAgent(ag.ID)
	}
}

func TestOnClassifierCompleted_Success(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Original title", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)

	// Write classification.json to a temp dir simulating the agent's worktree.
	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("update agent worktree path: %v", err)
	}

	output := ClassifierOutput{
		Category:        "quickfix",
		ComplexityScore: 3,
		Title:           "Refined title",
		Description:     "Enriched desc",
		TargetFiles:     []string{"path/to/file.go"},
		Rationale:       "Evidence-based reason",
	}
	writeClassificationJSON(t, wtDir, task.ID, output)

	if err := orch.onClassifierCompleted(&ag, &task); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	// Reload task and verify transition to BACKLOG with enriched fields.
	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.Status != model.StatusBacklog {
		t.Errorf("status = %q, want %q", reloaded.Status, model.StatusBacklog)
	}
	if reloaded.Category != model.CategoryQuickFix {
		t.Errorf("category = %q, want %q", reloaded.Category, model.CategoryQuickFix)
	}
	if reloaded.ComplexityScore != 3 {
		t.Errorf("complexity_score = %d, want 3", reloaded.ComplexityScore)
	}
	if reloaded.Title != "Refined title" {
		t.Errorf("title = %q, want %q", reloaded.Title, "Refined title")
	}
	if reloaded.Description != "Enriched desc" {
		t.Errorf("description = %q, want %q", reloaded.Description, "Enriched desc")
	}

	// Verify target_files and rationale stored in Context.
	if reloaded.Context == nil {
		t.Fatal("task context should not be nil")
	}
	targetFiles, ok := reloaded.Context["target_files"]
	if !ok {
		t.Fatal("context should contain target_files")
	}
	files, ok := targetFiles.([]interface{})
	if !ok {
		t.Fatalf("target_files should be a slice, got %T", targetFiles)
	}
	if len(files) != 1 || files[0] != "path/to/file.go" {
		t.Errorf("target_files = %v, want [path/to/file.go]", files)
	}
	rationale, ok := reloaded.Context["rationale"]
	if !ok {
		t.Fatal("context should contain rationale")
	}
	if rationale != "Evidence-based reason" {
		t.Errorf("rationale = %q, want %q", rationale, "Evidence-based reason")
	}
}

func TestOnClassifierCompleted_NeedsClarification(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Unclear task", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)

	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("update agent worktree path: %v", err)
	}

	output := ClassifierOutput{
		NeedsClarification: true,
		Questions:          []string{"What file?", "Expected behavior?"},
	}
	writeClassificationJSON(t, wtDir, task.ID, output)

	if err := orch.onClassifierCompleted(&ag, &task); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Task should stay in CLASSIFYING.
	if reloaded.Status != model.StatusClassifying {
		t.Errorf("status = %q, want %q", reloaded.Status, model.StatusClassifying)
	}

	// Questions should be stored in context.
	if reloaded.Context == nil {
		t.Fatal("task context should not be nil")
	}
	questions, ok := reloaded.Context["clarification_questions"]
	if !ok {
		t.Fatal("context should contain clarification_questions")
	}
	qSlice, ok := questions.([]interface{})
	if !ok {
		t.Fatalf("clarification_questions should be a slice, got %T", questions)
	}
	if len(qSlice) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(qSlice))
	}
	if qSlice[0] != "What file?" || qSlice[1] != "Expected behavior?" {
		t.Errorf("questions = %v, want [What file?, Expected behavior?]", qSlice)
	}
}

func TestOnClassifierCompleted_MalformedOutput(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Bad output task", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)

	// Set worktree path to a temp dir but do NOT write classification.json.
	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("update agent worktree path: %v", err)
	}

	if err := orch.onClassifierCompleted(&ag, &task); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Task should stay in CLASSIFYING.
	if reloaded.Status != model.StatusClassifying {
		t.Errorf("status = %q, want %q", reloaded.Status, model.StatusClassifying)
	}

	// Should be parked for human triage.
	if reloaded.Context == nil {
		t.Fatal("task context should not be nil")
	}
	humanTriage, ok := reloaded.Context["human_triage"]
	if !ok {
		t.Fatal("context should contain human_triage")
	}
	if humanTriage != true {
		t.Errorf("human_triage = %v, want true", humanTriage)
	}
}

func TestOnClassifierFailed_ParksForTriage(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Failed classifier task", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentDead)

	if err := orch.onClassifierFailed(&ag, &task); err != nil {
		t.Fatalf("onClassifierFailed: %v", err)
	}

	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Task must stay in CLASSIFYING — NOT transition to FAILED.
	if reloaded.Status != model.StatusClassifying {
		t.Errorf("status = %q, want %q (should NOT transition to failed)", reloaded.Status, model.StatusClassifying)
	}

	// Should be parked for human triage.
	if reloaded.Context == nil {
		t.Fatal("task context should not be nil")
	}
	humanTriage, ok := reloaded.Context["human_triage"]
	if !ok {
		t.Fatal("context should contain human_triage")
	}
	if humanTriage != true {
		t.Errorf("human_triage = %v, want true", humanTriage)
	}

	// Error details should be stored.
	_, hasError := reloaded.Context["classifier_error"]
	if !hasError {
		t.Error("context should contain classifier_error with error details")
	}
}

// ---------------------------------------------------------------------------
// Agent lifecycle cleanup tests
//
// These tests verify that classifier completion/failure handlers update agent
// status and clear task.AssignedAgentID — the missing behavior that causes
// the hot-loop bug where recoverStuckAgents() re-parks the same agents every
// 5 seconds.
// ---------------------------------------------------------------------------

func TestOnClassifierCompleted_Success_MarksAgentIdle(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Classify me", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)
	task.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("assign agent: %v", err)
	}

	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("save agent worktree: %v", err)
	}

	writeClassificationJSON(t, wtDir, task.ID, ClassifierOutput{
		Category:        "standard",
		ComplexityScore: 5,
		Title:           "Refined",
		Description:     "Enriched",
		TargetFiles:     []string{"a.go"},
		Rationale:       "reason",
	})

	if err := orch.onClassifierCompleted(&ag, &task); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	// Reload agent from DB and verify status.
	var reloadedAgent model.Agent
	if err := orch.db.First(&reloadedAgent, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if reloadedAgent.Status != model.AgentIdle {
		t.Errorf("agent status = %q, want %q", reloadedAgent.Status, model.AgentIdle)
	}
	if reloadedAgent.CurrentTaskID != nil {
		t.Errorf("agent CurrentTaskID = %v, want nil", reloadedAgent.CurrentTaskID)
	}

	// Verify task.AssignedAgentID is cleared.
	var reloadedTask model.Task
	if err := orch.db.First(&reloadedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloadedTask.AssignedAgentID != nil {
		t.Errorf("task AssignedAgentID = %v, want nil", reloadedTask.AssignedAgentID)
	}
}

func TestOnClassifierCompleted_NeedsClarification_MarksAgentIdle(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Unclear task", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)
	task.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("assign agent: %v", err)
	}

	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("save agent worktree: %v", err)
	}

	writeClassificationJSON(t, wtDir, task.ID, ClassifierOutput{
		NeedsClarification: true,
		Questions:          []string{"What file?"},
	})

	if err := orch.onClassifierCompleted(&ag, &task); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	var reloadedAgent model.Agent
	if err := orch.db.First(&reloadedAgent, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if reloadedAgent.Status != model.AgentIdle {
		t.Errorf("agent status = %q, want %q", reloadedAgent.Status, model.AgentIdle)
	}
	if reloadedAgent.CurrentTaskID != nil {
		t.Errorf("agent CurrentTaskID = %v, want nil", reloadedAgent.CurrentTaskID)
	}

	var reloadedTask model.Task
	if err := orch.db.First(&reloadedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloadedTask.AssignedAgentID != nil {
		t.Errorf("task AssignedAgentID = %v, want nil", reloadedTask.AssignedAgentID)
	}
}

func TestOnClassifierCompleted_MalformedOutput_MarksAgentIdle(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Bad output", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)
	task.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("assign agent: %v", err)
	}

	// No classification.json written — triggers malformed output path.
	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("save agent worktree: %v", err)
	}

	if err := orch.onClassifierCompleted(&ag, &task); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	var reloadedAgent model.Agent
	if err := orch.db.First(&reloadedAgent, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if reloadedAgent.Status != model.AgentIdle {
		t.Errorf("agent status = %q, want %q", reloadedAgent.Status, model.AgentIdle)
	}
	if reloadedAgent.CurrentTaskID != nil {
		t.Errorf("agent CurrentTaskID = %v, want nil", reloadedAgent.CurrentTaskID)
	}

	var reloadedTask model.Task
	if err := orch.db.First(&reloadedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloadedTask.AssignedAgentID != nil {
		t.Errorf("task AssignedAgentID = %v, want nil", reloadedTask.AssignedAgentID)
	}
}

func TestOnClassifierFailed_MarksAgentDead(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Failing classifier", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)
	task.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("assign agent: %v", err)
	}

	if err := orch.onClassifierFailed(&ag, &task); err != nil {
		t.Fatalf("onClassifierFailed: %v", err)
	}

	var reloadedAgent model.Agent
	if err := orch.db.First(&reloadedAgent, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if reloadedAgent.Status != model.AgentDead {
		t.Errorf("agent status = %q, want %q", reloadedAgent.Status, model.AgentDead)
	}
	if reloadedAgent.CurrentTaskID != nil {
		t.Errorf("agent CurrentTaskID = %v, want nil", reloadedAgent.CurrentTaskID)
	}

	var reloadedTask model.Task
	if err := orch.db.First(&reloadedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloadedTask.AssignedAgentID != nil {
		t.Errorf("task AssignedAgentID = %v, want nil", reloadedTask.AssignedAgentID)
	}
}

func TestProcessClassifyingTasks_SkipsHumanTriageTasks(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create a task in CLASSIFYING with human_triage=true and no assigned agent.
	task := testutil.CreateTask(t, orch.db, projectID, "Triaged task", model.StatusClassifying)
	task.Context = model.JSONField{"human_triage": true}
	task.AssignedAgentID = nil
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("save task with human_triage: %v", err)
	}

	orch.processClassifyingTasks()

	// No agents should have been spawned.
	var agents []model.Agent
	if err := orch.db.Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents for human_triage task, got %d", len(agents))
	}

	// Task should remain unassigned.
	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.AssignedAgentID != nil {
		t.Errorf("task AssignedAgentID = %v, want nil (human_triage should skip)", reloaded.AssignedAgentID)
	}
}

// ---------------------------------------------------------------------------
// Integration: verify hot loop is broken end-to-end
// ---------------------------------------------------------------------------

// TestClassifierRecovery_NoHotLoop verifies the full recovery → classifier
// completion → skip path. Before the fix, recoverStuckAgents would re-detect
// the same classifier on every tick because the agent stayed WORKING after
// completion, generating ~4800 log lines/hour.
func TestClassifierRecovery_NoHotLoop(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Hot loop task", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)

	// Give the agent a worktree with an idle signal file and classification.json.
	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	ag.ProjectID = projectID
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("save agent worktree path: %v", err)
	}
	// Backdate past grace period so recovery can act on this agent.
	orch.db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))
	task.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("assign agent to task: %v", err)
	}

	// Place the idle signal file.
	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "agent-idle"), []byte("idle"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write classification-<taskID>.json so onClassifierCompleted succeeds.
	writeClassificationJSON(t, wtDir, task.ID, ClassifierOutput{
		Category:        "standard",
		ComplexityScore: 5,
		Title:           "Classified title",
		Description:     "Classified desc",
		Rationale:       "Reason",
	})

	// --- First call: should recover the stuck classifier ---
	orch.recoverStuckAgents()

	var agAfter model.Agent
	if err := orch.db.First(&agAfter, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if agAfter.Status != model.AgentIdle {
		t.Fatalf("after first recovery: agent status = %q, want %q", agAfter.Status, model.AgentIdle)
	}
	if agAfter.CurrentTaskID != nil {
		t.Errorf("after first recovery: agent CurrentTaskID = %v, want nil", agAfter.CurrentTaskID)
	}

	var taskAfter model.Task
	if err := orch.db.First(&taskAfter, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if taskAfter.Status != model.StatusBacklog {
		t.Errorf("after first recovery: task status = %q, want %q", taskAfter.Status, model.StatusBacklog)
	}

	// --- Second call: should NOT re-process the agent (status is now Idle) ---
	// Reset task to classifying to simulate what would happen if the guard were missing.
	taskAfter.Status = model.StatusClassifying
	taskAfter.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&taskAfter).Error; err != nil {
		t.Fatalf("reset task for second pass: %v", err)
	}

	orch.recoverStuckAgents()

	// Agent should still be Idle — not re-processed.
	var agSecond model.Agent
	if err := orch.db.First(&agSecond, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent after second call: %v", err)
	}
	if agSecond.Status != model.AgentIdle {
		t.Errorf("after second recovery: agent status = %q, want %q (should not have been re-processed)", agSecond.Status, model.AgentIdle)
	}

	// Task should remain classifying (not transitioned again).
	var taskSecond model.Task
	if err := orch.db.First(&taskSecond, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task after second call: %v", err)
	}
	if taskSecond.Status != model.StatusClassifying {
		t.Errorf("after second recovery: task status = %q, want %q (should not be re-processed)", taskSecond.Status, model.StatusClassifying)
	}
}

// TestClassifierRecovery_FailedAgent_NoHotLoop verifies that a failed
// classifier agent is also not re-processed on subsequent ticks.
func TestClassifierRecovery_FailedAgent_NoHotLoop(t *testing.T) {
	orch, projectID := setupClassifyingTest(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Failed classifier task", model.StatusClassifying)
	ag := testutil.CreateAgent(t, orch.db, task.ID, model.AgentClassifier, model.AgentWorking)

	// Agent worktree with idle signal but NO classification.json → triggers onClassifierCompleted
	// which calls parkClassifierForTriage because classification.json is missing.
	wtDir := t.TempDir()
	ag.WorktreePath = wtDir
	ag.ProjectID = projectID
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("save agent: %v", err)
	}
	// Backdate past grace period so recovery can act on this agent.
	orch.db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))
	task.AssignedAgentID = &ag.ID
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	claudeDir := filepath.Join(wtDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "agent-idle"), []byte("idle"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First call: triggers completion → parkClassifierForTriage (no classification.json).
	orch.recoverStuckAgents()

	var agAfter model.Agent
	if err := orch.db.First(&agAfter, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	// onClassifierCompleted marks Idle even when parking for triage.
	if agAfter.Status != model.AgentIdle {
		t.Fatalf("after first recovery: agent status = %q, want %q", agAfter.Status, model.AgentIdle)
	}

	// Second call: agent is Idle, should be skipped entirely.
	orch.recoverStuckAgents()

	var agSecond model.Agent
	if err := orch.db.First(&agSecond, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if agSecond.Status != model.AgentIdle {
		t.Errorf("after second recovery: agent status = %q, want %q", agSecond.Status, model.AgentIdle)
	}
}

// TestProcessClassifyingTasks_SkipsParkedHumanTriage_NilAgent verifies that
// a task parked with human_triage=true and AssignedAgentID=nil does NOT get
// a new classifier spawned — this was the second half of the hot loop bug.
// (Already covered by TestProcessClassifyingTasks_SkipsHumanTriageTasks above,
// but this test makes the nil-agent invariant explicit.)
func TestProcessClassifyingTasks_SkipsParkedHumanTriage_NilAgent(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	task := testutil.CreateTask(t, orch.db, projectID, "Parked triage task", model.StatusClassifying)
	task.Context = model.JSONField{"human_triage": true}
	task.AssignedAgentID = nil
	if err := orch.db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	// Call processClassifyingTasks multiple times to ensure no spawning.
	orch.processClassifyingTasks()
	orch.processClassifyingTasks()

	var agents []model.Agent
	if err := orch.db.Find(&agents).Error; err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents for parked human_triage task, got %d", len(agents))
	}

	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.AssignedAgentID != nil {
		t.Errorf("task AssignedAgentID = %v, want nil", reloaded.AssignedAgentID)
	}
}

// writeClassificationJSON writes a ClassifierOutput as classification-<taskID>.json
// in the given directory.
func writeClassificationJSON(t *testing.T, dir string, taskID uuid.UUID, output ClassifierOutput) {
	t.Helper()
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal classification output: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("classification-%s.json", taskID))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write classification json: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Capacity checking tests
//
// These tests verify that processClassifyingTasks respects the agent pool
// capacity limit and only dispatches classifier agents when capacity is
// available. This prevents the classifier agent dispatch from being blocked
// when max_concurrent_agents slots are exhausted by other agent types.
// ---------------------------------------------------------------------------

// TestProcessClassifyingTasksWithCapacity verifies successful dispatch when
// capacity is available in the agent pool.
func TestProcessClassifyingTasksWithCapacity(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create a task in CLASSIFYING with no assigned agent.
	task := testutil.CreateTask(t, orch.db, projectID, "Task with available capacity", model.StatusClassifying)

	// Dispatch should succeed with available capacity.
	orch.processClassifyingTasks()

	// Verify the task now has an assigned classifier agent.
	var reloaded model.Task
	if err := orch.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.AssignedAgentID == nil {
		t.Fatal("expected task.AssignedAgentID to be set when capacity is available")
	}

	// Verify a classifier agent was created.
	var ag model.Agent
	if err := orch.db.First(&ag, "id = ?", *reloaded.AssignedAgentID).Error; err != nil {
		t.Fatalf("load assigned agent: %v", err)
	}
	if ag.AgentType != model.AgentClassifier {
		t.Errorf("agent type = %q, want %q", ag.AgentType, model.AgentClassifier)
	}
	if ag.Status != model.AgentWorking {
		t.Errorf("agent status = %q, want %q", ag.Status, model.AgentWorking)
	}

	// Clean up: stop the spawned agent to cancel monitoring goroutines.
	if orch.runner != nil {
		_ = orch.runner.StopAgent(ag.ID)
	}
}

// TestProcessClassifyingTasksNoCapacity verifies that no classifier agent
// is dispatched when the agent pool is at max capacity.
func TestProcessClassifyingTasksNoCapacity(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create tasks in CLASSIFYING that need classification.
	task1 := testutil.CreateTask(t, orch.db, projectID, "Task 1", model.StatusClassifying)
	task2 := testutil.CreateTask(t, orch.db, projectID, "Task 2", model.StatusClassifying)

	// Fill the agent pool to max capacity.
	// The runner was created with maxConcurrent=4.
	for i := 0; i < 4; i++ {
		ag := testutil.CreateAgent(t, orch.db, uuid.New(), model.AgentPlanner, model.AgentWorking)
		ag.ProjectID = projectID
		if err := orch.db.Save(&ag).Error; err != nil {
			t.Fatalf("create agent %d: %v", i, err)
		}
	}

	// Attempt to process classifying tasks when pool is full.
	// The first task should fail to dispatch due to no capacity.
	orch.processClassifyingTasks()

	// Verify neither task was assigned an agent (no capacity).
	var reloaded1 model.Task
	if err := orch.db.First(&reloaded1, "id = ?", task1.ID).Error; err != nil {
		t.Fatalf("reload task1: %v", err)
	}
	if reloaded1.AssignedAgentID != nil {
		t.Errorf("task1.AssignedAgentID = %v, want nil when pool is full", reloaded1.AssignedAgentID)
	}

	var reloaded2 model.Task
	if err := orch.db.First(&reloaded2, "id = ?", task2.ID).Error; err != nil {
		t.Fatalf("reload task2: %v", err)
	}
	if reloaded2.AssignedAgentID != nil {
		t.Errorf("task2.AssignedAgentID = %v, want nil when pool is full", reloaded2.AssignedAgentID)
	}

	// Verify both tasks remain in CLASSIFYING status.
	if reloaded1.Status != model.StatusClassifying {
		t.Errorf("task1 status = %q, want %q", reloaded1.Status, model.StatusClassifying)
	}
	if reloaded2.Status != model.StatusClassifying {
		t.Errorf("task2 status = %q, want %q", reloaded2.Status, model.StatusClassifying)
	}
}

// TestProcessClassifyingTasksMultipleTasks verifies that multiple tasks are
// processed up to the capacity limit.
func TestProcessClassifyingTasksMultipleTasks(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create 3 tasks in CLASSIFYING (runner has maxConcurrent=4, so we can
	// dispatch all 3 in one call).
	task1 := testutil.CreateTask(t, orch.db, projectID, "Task 1", model.StatusClassifying)
	task2 := testutil.CreateTask(t, orch.db, projectID, "Task 2", model.StatusClassifying)
	task3 := testutil.CreateTask(t, orch.db, projectID, "Task 3", model.StatusClassifying)

	// Process classifying tasks — all 3 should get dispatched.
	orch.processClassifyingTasks()

	// Verify all 3 tasks were assigned classifier agents.
	var tasks []model.Task
	if err := orch.db.Where("id IN ?", []uuid.UUID{task1.ID, task2.ID, task3.ID}).
		Find(&tasks).Error; err != nil {
		t.Fatalf("reload tasks: %v", err)
	}

	assignedCount := 0
	for i := range tasks {
		if tasks[i].AssignedAgentID != nil {
			assignedCount++
		}
	}
	if assignedCount != 3 {
		t.Errorf("expected 3 tasks to be assigned agents, got %d", assignedCount)
	}

	// Verify 3 classifier agents were created.
	var agents []model.Agent
	if err := orch.db.Where("agent_type = ?", model.AgentClassifier).
		Find(&agents).Error; err != nil {
		t.Fatalf("query classifier agents: %v", err)
	}
	if len(agents) != 3 {
		t.Errorf("expected 3 classifier agents, got %d", len(agents))
	}

	// Clean up: stop the spawned agents.
	for _, ag := range agents {
		if orch.runner != nil {
			_ = orch.runner.StopAgent(ag.ID)
		}
	}
}

// TestProcessClassifyingTasksCapacityFull verifies that when the agent pool
// reaches capacity after dispatching some tasks, remaining tasks stay in
// CLASSIFYING and are not assigned agents.
func TestProcessClassifyingTasksCapacityFull(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)

	// Create 5 tasks in CLASSIFYING (runner has maxConcurrent=4, so the 5th
	// should not be dispatched when the first 4 slots are full).
	task1 := testutil.CreateTask(t, orch.db, projectID, "Task 1", model.StatusClassifying)
	task2 := testutil.CreateTask(t, orch.db, projectID, "Task 2", model.StatusClassifying)
	task3 := testutil.CreateTask(t, orch.db, projectID, "Task 3", model.StatusClassifying)
	task4 := testutil.CreateTask(t, orch.db, projectID, "Task 4", model.StatusClassifying)
	task5 := testutil.CreateTask(t, orch.db, projectID, "Task 5 - should not dispatch", model.StatusClassifying)

	// Process classifying tasks.
	// First 4 should dispatch successfully, but the 5th should fail due to capacity.
	orch.processClassifyingTasks()

	// Reload all tasks.
	var tasks []model.Task
	if err := orch.db.Where("id IN ?", []uuid.UUID{task1.ID, task2.ID, task3.ID, task4.ID, task5.ID}).
		Find(&tasks).Error; err != nil {
		t.Fatalf("reload tasks: %v", err)
	}

	// Count how many tasks were assigned agents.
	assignedCount := 0
	var assignedIDs map[uuid.UUID]bool = make(map[uuid.UUID]bool)
	for i := range tasks {
		if tasks[i].AssignedAgentID != nil {
			assignedCount++
			assignedIDs[tasks[i].ID] = true
		}
	}

	// At most 4 tasks should be assigned (first 4 succeed, 5th fails due to capacity).
	if assignedCount > 4 {
		t.Errorf("expected at most 4 tasks to be assigned, got %d", assignedCount)
	}

	// Verify task5 was NOT assigned (hit capacity limit).
	if assignedIDs[task5.ID] {
		t.Error("task5 should not be assigned when pool reaches capacity")
	}

	// Verify task5 is still in CLASSIFYING.
	var reloadedTask5 model.Task
	if err := orch.db.First(&reloadedTask5, "id = ?", task5.ID).Error; err != nil {
		t.Fatalf("reload task5: %v", err)
	}
	if reloadedTask5.Status != model.StatusClassifying {
		t.Errorf("task5 status = %q, want %q when capacity is reached", reloadedTask5.Status, model.StatusClassifying)
	}
	if reloadedTask5.AssignedAgentID != nil {
		t.Errorf("task5.AssignedAgentID = %v, want nil when capacity is reached", reloadedTask5.AssignedAgentID)
	}

	// Clean up: stop the spawned agents.
	var agents []model.Agent
	if err := orch.db.Where("agent_type = ?", model.AgentClassifier).
		Find(&agents).Error; err == nil {
		for _, ag := range agents {
			if orch.runner != nil {
				_ = orch.runner.StopAgent(ag.ID)
			}
		}
	}
}

// TestClassifyingTasksEndToEnd verifies the full pipeline for classifier dispatch
// and task enrichment:
// 1. Task filing creates task in CLASSIFYING status
// 2. Orchestrator tick dispatches classifier agent
// 3. Classifier completes and produces classification.json
// 4. Task transitions from CLASSIFYING → BACKLOG
// 5. Task has enriched data: complexity_score, category, title, description
func TestClassifyingTasksEndToEnd(t *testing.T) {
	orch, projectID := setupClassifyingTestWithRunner(t)
	defer func() {
		// Clean up: stop any running agents
		var agents []model.Agent
		if err := orch.db.Where("agent_type = ?", model.AgentClassifier).
			Find(&agents).Error; err == nil {
			for _, ag := range agents {
				if orch.runner != nil {
					_ = orch.runner.StopAgent(ag.ID)
				}
			}
		}
	}()

	// Step 1: Create a task in CLASSIFYING status (simulating task filing)
	task := testutil.CreateTask(t, orch.db, projectID, "Fix authentication bug", model.StatusClassifying)

	// Verify initial state
	var initial model.Task
	if err := orch.db.First(&initial, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load initial task: %v", err)
	}
	if initial.Status != model.StatusClassifying {
		t.Fatalf("expected task in CLASSIFYING, got %q", initial.Status)
	}
	if initial.AssignedAgentID != nil {
		t.Fatal("expected no assigned agent initially")
	}

	// Step 2: Process classifying tasks to trigger classifier agent dispatch
	orch.processClassifyingTasks()

	// Reload task to verify agent was assigned
	var afterDispatch model.Task
	if err := orch.db.First(&afterDispatch, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task after dispatch: %v", err)
	}
	if afterDispatch.AssignedAgentID == nil {
		t.Fatal("expected task.AssignedAgentID to be set after processClassifyingTasks")
	}

	// Load the assigned agent
	var ag model.Agent
	if err := orch.db.First(&ag, "id = ?", *afterDispatch.AssignedAgentID).Error; err != nil {
		t.Fatalf("load assigned agent: %v", err)
	}

	// Verify agent is classifier type
	if ag.AgentType != model.AgentClassifier {
		t.Errorf("agent type = %q, want %q", ag.AgentType, model.AgentClassifier)
	}

	// Verify agent has worktree path (should be main worktree for read-only classifier)
	if ag.WorktreePath == "" {
		t.Fatal("agent WorktreePath should be set")
	}

	// Step 3: Simulate classifier completion
	// The classifier would have written classification.json to its worktree
	classifierOutput := ClassifierOutput{
		Category:        "quickfix",
		ComplexityScore: 2,
		Title:           "Fix OAuth authentication bypass",
		Description:     "The session validation is broken for OAuth tokens due to incomplete token verification logic. Must add explicit token expiry checks.",
		TargetFiles:     []string{"internal/auth/oauth.go", "internal/auth/session.go"},
		Rationale:       "Analysis of code paths shows OAuth token validation is missing checks for token expiry and signature validation. This is a critical security issue.",
	}

	// Write classification-<taskID>.json to agent's worktree
	writeClassificationJSON(t, ag.WorktreePath, task.ID, classifierOutput)

	// Update agent status to WORKING to simulate it being spawned
	ag.Status = model.AgentWorking
	if err := orch.db.Save(&ag).Error; err != nil {
		t.Fatalf("update agent to WORKING: %v", err)
	}

	// Step 4: Call the completion handler
	if err := orch.onClassifierCompleted(&ag, &afterDispatch); err != nil {
		t.Fatalf("onClassifierCompleted: %v", err)
	}

	// Step 5: Verify task transitioned to BACKLOG with enriched data
	var enriched model.Task
	if err := orch.db.First(&enriched, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load enriched task: %v", err)
	}

	// Verify status transition
	if enriched.Status != model.StatusBacklog {
		t.Errorf("status = %q, want %q", enriched.Status, model.StatusBacklog)
	}

	// Verify category
	if enriched.Category != model.CategoryQuickFix {
		t.Errorf("category = %q, want %q", enriched.Category, model.CategoryQuickFix)
	}

	// Verify complexity score
	if enriched.ComplexityScore != 2 {
		t.Errorf("complexity_score = %d, want 2", enriched.ComplexityScore)
	}

	// Verify title was enriched
	expectedTitle := "Fix OAuth authentication bypass"
	if enriched.Title != expectedTitle {
		t.Errorf("title = %q, want %q", enriched.Title, expectedTitle)
	}

	// Verify description was enriched
	expectedDesc := "The session validation is broken for OAuth tokens due to incomplete token verification logic. Must add explicit token expiry checks."
	if enriched.Description != expectedDesc {
		t.Errorf("description = %q, want %q", enriched.Description, expectedDesc)
	}

	// Verify target files are in context
	if enriched.Context == nil {
		t.Fatal("task context should not be nil")
	}
	targetFilesRaw, ok := enriched.Context["target_files"]
	if !ok {
		t.Fatal("context should contain target_files")
	}
	targetFiles, ok := targetFilesRaw.([]interface{})
	if !ok {
		t.Fatalf("target_files should be []interface{}, got %T", targetFilesRaw)
	}
	if len(targetFiles) != 2 {
		t.Fatalf("expected 2 target files, got %d", len(targetFiles))
	}
	if targetFiles[0] != "internal/auth/oauth.go" || targetFiles[1] != "internal/auth/session.go" {
		t.Errorf("target_files = %v, want [internal/auth/oauth.go internal/auth/session.go]", targetFiles)
	}

	// Verify rationale is in context
	rationaleRaw, ok := enriched.Context["rationale"]
	if !ok {
		t.Fatal("context should contain rationale")
	}
	expectedRationale := "Analysis of code paths shows OAuth token validation is missing checks for token expiry and signature validation. This is a critical security issue."
	if rationaleRaw != expectedRationale {
		t.Errorf("rationale = %q, want %q", rationaleRaw, expectedRationale)
	}

	// Verify agent was marked idle
	var agentAfterCompletion model.Agent
	if err := orch.db.First(&agentAfterCompletion, "id = ?", ag.ID).Error; err != nil {
		t.Fatalf("load agent after completion: %v", err)
	}
	if agentAfterCompletion.Status != model.AgentIdle {
		t.Errorf("agent status = %q, want %q", agentAfterCompletion.Status, model.AgentIdle)
	}
	if agentAfterCompletion.CurrentTaskID != nil {
		t.Fatal("agent should be detached from task after completion")
	}

	// Verify an event was recorded for the state transition
	var event model.TaskEvent
	if err := orch.db.Where("task_id = ?", task.ID).
		First(&event).Error; err != nil {
		t.Fatalf("load task event: %v", err)
	}
	if event.OldValue != string(model.StatusClassifying) {
		t.Errorf("event old_value = %q, want %q", event.OldValue, model.StatusClassifying)
	}
	if event.NewValue != string(model.StatusBacklog) {
		t.Errorf("event new_value = %q, want %q", event.NewValue, model.StatusBacklog)
	}
}
