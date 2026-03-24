package orchestrator

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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

	runner := agent.NewRunner(db, nil, wt, fakeBin, 4)

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
