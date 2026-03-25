package orchestrator

import (
	"encoding/json"
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
		worktree:        &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"},
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

	runner := agent.NewRunner(db, nil, wt, fakeBin, 4, nil)

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
	writeClassificationJSON(t, wtDir, output)

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
	writeClassificationJSON(t, wtDir, output)

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

	writeClassificationJSON(t, wtDir, ClassifierOutput{
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

	writeClassificationJSON(t, wtDir, ClassifierOutput{
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

	// Write classification.json so onClassifierCompleted succeeds.
	writeClassificationJSON(t, wtDir, ClassifierOutput{
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

// writeClassificationJSON writes a ClassifierOutput as classification.json
// in the given directory.
func writeClassificationJSON(t *testing.T, dir string, output ClassifierOutput) {
	t.Helper()
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal classification output: %v", err)
	}
	path := filepath.Join(dir, "classification.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write classification.json: %v", err)
	}
}
