package orchestrator

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/merge"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/tmux"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// agentResultOrchestrator creates an Orchestrator configured for agent result
// testing. It provides a real runner and memory manager so that code paths
// calling runner.GetAgentOutput and memory.ExtractMemoriesFromOutput do not
// panic. The supervisor is nil (no LLM calls).
func agentResultOrchestrator(t *testing.T, bareRepoPath string) (*Orchestrator, *model.Project) {
	t.Helper()
	db := lifecycleTestDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{
		ID:           projectID,
		Name:         "test-project",
		BareRepoPath: bareRepoPath,
	}
	db.Create(&project)

	// Create a minimal runner backed by a fake tmux session name. The runner
	// is needed to avoid nil-pointer panics in onAgentCompleted; it will
	// return errors from GetAgentOutput which the caller handles gracefully.
	tm := tmux.NewManager("test-agent-result")
	runner := agent.NewRunner(db, tm, wt, "/usr/bin/false", 4)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		memory:    memory.NewManager(db),
		events:    events,
		logger:    slog.Default().With("component", "test-agent-result"),
	}
	return o, &project
}

// ---------------------------------------------------------------------------
// 1. processAgentResult routing
// ---------------------------------------------------------------------------

func TestProcessAgentResult_SuccessRouting(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "success-routing"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create an agent branch with commits that are NOT yet merged into the
	// feature branch. We do this manually (instead of createAgentBranch which
	// also merges) so that BranchHasNewCommits returns true.
	agentBranch := "worktree-agent-success"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-test")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")
	testFile := filepath.Join(agentDir, "agent-work.txt")
	os.WriteFile(testFile, []byte("agent work"), 0o644)
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "agent work commit")

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	// Create parent task with worktree branch.
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-task",
		Description:    "parent for success routing",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	// Create subtask assigned to an agent.
	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   agentDir,
		WorktreeBranch: agentBranch,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "coder-subtask",
		Description:     "do coding",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	// Set up a merge.Orchestrator so onAgentCompleted can merge.
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	o.merger = merge.NewOrchestrator(wt, o.db)

	// Process a success completion.
	err := o.processAgentResult(agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	})
	if err != nil {
		t.Fatalf("processAgentResult success: %v", err)
	}

	// Verify agent is now idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	// Verify task was fast-tracked to done.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusDone {
		t.Errorf("expected task status done, got %s", updatedTask.Status)
	}

	_ = featureDir // used indirectly via branch creation
}

func TestProcessAgentResult_FailureRouting(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "failure-routing"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	// Create parent task.
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-task-fail",
		Description:    "parent for failure routing",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	// Create agent and subtask.
	taskID := uuid.New()
	agentID := uuid.New()
	agentWorktreeDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-fail")
	os.MkdirAll(agentWorktreeDir, 0o755)

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder-fail",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  agentWorktreeDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "failing-subtask",
		Description:     "will fail",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	// onAgentFailed calls o.runner.GetAgentOutput — we need runner to be nil-safe.
	// Since runner is nil, it will panic. Instead, test the routing logic
	// by directly calling onAgentFailed which is the failure path.
	err := o.onAgentFailed(&ag, &task)
	// Without a runner, GetAgentOutput will fail. The function catches this
	// and continues with "unknown error".
	if err != nil {
		t.Fatalf("onAgentFailed: %v", err)
	}

	// Verify agent is dead.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAgent.Status)
	}

	// Verify task is failed (coder failure without supervisor).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected task status failed, got %s", updatedTask.Status)
	}

	// Verify error stored in context.
	if updatedTask.Context == nil {
		t.Fatal("expected task context to be non-nil")
	}
	if _, ok := updatedTask.Context["last_error"]; !ok {
		t.Error("expected last_error in task context")
	}
}

func TestProcessAgentResult_UnknownAgent(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	// Call with an agent ID not in DB — should return error, not panic.
	err := o.processAgentResult(agent.Completion{
		AgentID:    uuid.New(),
		ReturnCode: 0,
	})
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
	if !strings.Contains(err.Error(), "load agent") {
		t.Errorf("expected 'load agent' in error, got: %s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// 2. onPlannerCompleted
// ---------------------------------------------------------------------------

func TestOnPlannerCompleted_ValidPlan(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "planner-valid"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	o.worktree = wt

	// Create a planner agent worktree and write plan.json.
	plannerDir := filepath.Join(bareRepoPath, "feature", featureName, "planner-wt")
	os.MkdirAll(plannerDir, 0o755)

	validPlan := map[string]any{
		"subtasks": []map[string]any{
			{"title": "Sub A", "description": "Do A", "agent_type": "coder", "estimated_files": []string{"a.go"}},
			{"title": "Sub B", "description": "Do B", "agent_type": "coder", "estimated_files": []string{"b.go"}},
		},
	}
	planJSON, _ := json.Marshal(validPlan)
	os.WriteFile(filepath.Join(plannerDir, "plan.json"), planJSON, 0o644)

	// Create task in planning status.
	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "test-planner",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  plannerDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "plan-this",
		Description:     "needs planning",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	o.db.Create(&task)

	err := o.onPlannerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onPlannerCompleted: %v", err)
	}

	// Verify task transitioned to plan_review.
	var updated model.Task
	o.db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusPlanReview {
		t.Errorf("expected status plan_review, got %s", updated.Status)
	}

	// Verify Plan field is populated.
	if updated.Plan == nil {
		t.Fatal("expected Plan to be populated")
	}
	subtasksRaw, ok := updated.Plan["subtasks"]
	if !ok {
		t.Fatal("expected subtasks key in plan")
	}
	subtasks, ok := subtasksRaw.([]any)
	if !ok {
		t.Fatalf("expected subtasks to be a slice, got %T", subtasksRaw)
	}
	if len(subtasks) != 2 {
		t.Errorf("expected 2 subtasks in plan, got %d", len(subtasks))
	}

	// Verify a TaskEvent was created.
	var events []model.TaskEvent
	o.db.Where("task_id = ?", taskID).Find(&events)
	found := false
	for _, evt := range events {
		if evt.EventType == "status_change" && evt.NewValue == "plan_review" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a status_change event to plan_review")
	}

	// Verify agent is now idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	_ = featureDir
}

func TestOnPlannerCompleted_InvalidPlan(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "planner-invalid"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	// Create planner worktree with invalid JSON.
	plannerDir := filepath.Join(bareRepoPath, "feature", featureName, "planner-wt")
	os.MkdirAll(plannerDir, 0o755)
	os.WriteFile(filepath.Join(plannerDir, "plan.json"), []byte("{invalid json"), 0o644)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "test-planner-bad",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  plannerDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "bad-plan",
		Description:     "produces bad plan",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	o.db.Create(&task)

	err := o.onPlannerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onPlannerCompleted with invalid plan: %v", err)
	}

	// Task should stay in planning (assigned agent cleared for retry).
	var updated model.Task
	o.db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning (retry), got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared for retry")
	}

	// Verify retry count was incremented.
	if updated.Context != nil {
		if v, ok := updated.Context["retry_count"].(float64); ok {
			if int(v) != 1 {
				t.Errorf("expected retry_count=1, got %d", int(v))
			}
		} else {
			t.Error("expected retry_count in context")
		}
	}
}

func TestOnPlannerCompleted_MissingPlan(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "planner-missing"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	// Create planner worktree with NO plan.json file.
	plannerDir := filepath.Join(bareRepoPath, "feature", featureName, "planner-wt-empty")
	os.MkdirAll(plannerDir, 0o755)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "test-planner-noplan",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  plannerDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "no-plan",
		Description:     "produces no plan",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	o.db.Create(&task)

	err := o.onPlannerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onPlannerCompleted with missing plan: %v", err)
	}

	// Task should stay in planning.
	var updated model.Task
	o.db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning (retry), got %s", updated.Status)
	}
	// Agent should be cleared for retry.
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared for retry")
	}
}

// ---------------------------------------------------------------------------
// 3. onAgentFailed
// ---------------------------------------------------------------------------

func TestOnAgentFailed_FirstFailure(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "fail-first"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-first-fail",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder-first-fail",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  "/tmp/fake-agent-path",
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "first-fail-subtask",
		Description:     "will fail first time",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.onAgentFailed(&ag, &task)
	if err != nil {
		t.Fatalf("onAgentFailed first failure: %v", err)
	}

	// Verify agent is dead.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAgent.Status)
	}

	// Verify task context has error info.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Context == nil {
		t.Fatal("expected task context to have error info")
	}
	if _, ok := updatedTask.Context["last_error"]; !ok {
		t.Error("expected last_error in task context")
	}

	// Task should be failed (coder, no supervisor, no retries).
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected task status failed, got %s", updatedTask.Status)
	}
}

func TestOnAgentFailed_MaxRetries(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "fail-max-retries"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-max-retry",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	// Set retry_count to MaxPlannerRetries - 1 so the next increment hits max.
	taskID := uuid.New()
	agentID := uuid.New()

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "test-planner-max",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  "/tmp/fake-planner-path",
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "max-retry-subtask",
		Description:     "will exhaust retries",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(MaxPlannerRetries - 1)},
	}
	o.db.Create(&task)

	err := o.onAgentFailed(&ag, &task)
	if err != nil {
		t.Fatalf("onAgentFailed max retries: %v", err)
	}

	// Verify task is failed (max retries exceeded).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected task status failed after max retries, got %s", updatedTask.Status)
	}
}

func TestOnAgentFailed_WithSupervisorNil(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "fail-no-supervisor"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)
	// Supervisor is already nil by default in agentResultOrchestrator.
	if o.supervisor != nil {
		t.Fatal("expected supervisor to be nil")
	}

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-no-sup",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	// Test planner failure without supervisor — should retry.
	taskID := uuid.New()
	agentID := uuid.New()

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "test-planner-nosup",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  "/tmp/fake-planner",
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "no-sup-subtask",
		Description:     "test fallback",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.onAgentFailed(&ag, &task)
	if err != nil {
		t.Fatalf("onAgentFailed without supervisor: %v", err)
	}

	// Verify planner stays in planning (retry behavior).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusPlanning {
		t.Errorf("expected task to stay in planning for retry, got %s", updatedTask.Status)
	}

	// Verify assignment cleared.
	if updatedTask.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared for retry")
	}

	// Verify retry_count = 1.
	if updatedTask.Context != nil {
		if v, ok := updatedTask.Context["retry_count"].(float64); ok {
			if int(v) != 1 {
				t.Errorf("expected retry_count=1, got %d", int(v))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 4. onReviewerCompleted and onFixerCompleted
// ---------------------------------------------------------------------------

func TestOnReviewerCompleted(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "reviewer-complete"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	// Create reviewer agent with a worktree containing review.json.
	reviewerDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	review := map[string]any{
		"verdict":  "approved",
		"comments": []string{"looks good", "tests pass"},
	}
	reviewJSON, _ := json.Marshal(review)
	os.WriteFile(filepath.Join(reviewerDir, "review.json"), reviewJSON, 0o644)

	taskID := uuid.New()
	agentID := uuid.New()

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-review",
		Description:    "parent for review",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentReviewer,
		Name:          "test-reviewer",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  reviewerDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "reviewer-subtask",
		Description:     "review this",
		Status:          model.StatusTestingReady,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.onReviewerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onReviewerCompleted: %v", err)
	}

	// Verify agent is idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	// Verify review is stored in task context.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Context == nil {
		t.Fatal("expected task context to have review data")
	}
	reviewData, ok := updatedTask.Context["review"]
	if !ok {
		t.Fatal("expected review key in task context")
	}
	reviewMap, ok := reviewData.(map[string]any)
	if !ok {
		t.Fatalf("expected review to be a map, got %T", reviewData)
	}
	if reviewMap["verdict"] != "approved" {
		t.Errorf("expected verdict=approved, got %v", reviewMap["verdict"])
	}

	// Verify review.json was cleaned up.
	if _, err := os.Stat(filepath.Join(reviewerDir, "review.json")); !os.IsNotExist(err) {
		t.Error("expected review.json to be removed after processing")
	}
}

func TestOnFixerCompleted(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentFixer,
		Name:          "test-fixer",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  "/tmp/fake-fixer",
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "fixer-task",
		Description:     "fix something",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(&task)

	err := o.onFixerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onFixerCompleted: %v", err)
	}

	// Verify agent is idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	// Verify agent's current task is cleared.
	if updatedAgent.CurrentTaskID != nil {
		t.Error("expected agent's current task to be nil")
	}

	// Task status should remain unchanged (fixer doesn't transition task).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusInProgress {
		t.Errorf("expected task to remain in_progress, got %s", updatedTask.Status)
	}
}

// ---------------------------------------------------------------------------
// 5. executeMerge
// ---------------------------------------------------------------------------

func TestExecuteMerge_Success(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	// Create a worktree for 'main' so MergeFeatureIntoMain can find it.
	mainDir := filepath.Join(bareRepoPath, "main-worktree")
	runGitCmd(t, bareRepoPath, "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")

	featureName := "merge-success"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create a commit on the feature branch so there's something to merge.
	featureFile := filepath.Join(featureDir, "feature-work.txt")
	os.WriteFile(featureFile, []byte("feature work"), 0o644)
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "feature work")

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	o.worktree = wt
	o.merger = merge.NewOrchestrator(wt, o.db)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      o.projectID,
		Title:          "merge-test-task",
		Description:    "task for merge test",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&task)

	err := o.executeMerge(&task)
	if err != nil {
		t.Fatalf("executeMerge success: %v", err)
	}

	// Verify task transitioned to done.
	var updated model.Task
	o.db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected task status done after merge, got %s", updated.Status)
	}

	// Verify a status_change event was created.
	var events []model.TaskEvent
	o.db.Where("task_id = ?", taskID).Find(&events)
	found := false
	for _, evt := range events {
		if evt.EventType == "status_change" && evt.NewValue == "done" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a status_change event to done")
	}
}

func TestExecuteMerge_NoMerger(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")
	// merger is nil by default.

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      o.projectID,
		Title:          "merge-nil-merger",
		Description:    "will fail",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/test",
	}
	o.db.Create(&task)

	// This should panic or return error because merger is nil.
	// We test that it panics by recovering.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic when merger is nil, got none")
			}
		}()
		_ = o.executeMerge(&task)
	}()
}

// ---------------------------------------------------------------------------
// 6. Helper utilities
// ---------------------------------------------------------------------------

func TestIncrementRetryCount(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	// Test with nil context.
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "retry-test",
		Description: "test",
		Status:      model.StatusPlanning,
	}

	count := o.incrementRetryCount(task)
	if count != 1 {
		t.Errorf("first increment: expected 1, got %d", count)
	}

	// Verify context was created.
	if task.Context == nil {
		t.Fatal("expected context to be created")
	}
	if v, ok := task.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1 in context, got %v", task.Context["retry_count"])
	}

	// Increment again.
	count = o.incrementRetryCount(task)
	if count != 2 {
		t.Errorf("second increment: expected 2, got %d", count)
	}

	// Increment a third time.
	count = o.incrementRetryCount(task)
	if count != 3 {
		t.Errorf("third increment: expected 3, got %d", count)
	}
}

func TestTaskFeatureName(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		contains string
	}{
		{
			name:     "simple title",
			title:    "Add Auth Module",
			contains: "add-auth-module",
		},
		{
			name:     "title with special chars",
			title:    "Fix: Bug #123 (urgent)",
			contains: "fix-bug-123-urgent",
		},
		{
			name:     "title with spaces only",
			title:    "Simple Task",
			contains: "simple-task",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := &model.Task{
				ID:    uuid.New(),
				Title: tc.title,
			}
			result := taskFeatureName(task)

			// Should contain the slugified title.
			if !strings.Contains(result, tc.contains) {
				t.Errorf("expected feature name to contain %q, got %q", tc.contains, result)
			}

			// Should start with the task ID prefix (first 8 chars).
			prefix := task.ID.String()[:8]
			if !strings.HasPrefix(result, prefix) {
				t.Errorf("expected feature name to start with %q, got %q", prefix, result)
			}
		})
	}
}

func TestTaskFeatureName_LongTitle(t *testing.T) {
	task := &model.Task{
		ID:    uuid.New(),
		Title: "This Is A Very Long Task Title That Exceeds The Maximum Length Allowed For Feature Names In Git Branches",
	}
	result := taskFeatureName(task)

	// The slug part (after the UUID prefix + "-") should be at most 40 chars.
	prefix := task.ID.String()[:8]
	slug := strings.TrimPrefix(result, prefix+"-")
	if len(slug) > 40 {
		t.Errorf("expected slug to be at most 40 chars, got %d: %q", len(slug), slug)
	}
}

// ---------------------------------------------------------------------------
// Supplementary tests
// ---------------------------------------------------------------------------

func TestProcessAgentResult_AgentWithNoTask(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	// Create an agent with no current task.
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "orphaned-agent",
		Status:        model.AgentWorking,
		CurrentTaskID: nil,
	}
	o.db.Create(&ag)

	// Should handle gracefully (log warning, return nil).
	err := o.processAgentResult(agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	})
	if err != nil {
		t.Fatalf("expected nil error for agent with no task, got: %v", err)
	}
}

func TestOnPlannerCompleted_EmptySubtasks(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "planner-empty"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	plannerDir := filepath.Join(bareRepoPath, "feature", featureName, "planner-wt")
	os.MkdirAll(plannerDir, 0o755)

	// Write plan.json with empty subtasks array.
	emptyPlan := map[string]any{
		"subtasks": []map[string]any{},
	}
	planJSON, _ := json.Marshal(emptyPlan)
	os.WriteFile(filepath.Join(plannerDir, "plan.json"), planJSON, 0o644)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "test-planner-empty",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  plannerDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "empty-plan",
		Description:     "produces empty plan",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
	}
	o.db.Create(&task)

	err := o.onPlannerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onPlannerCompleted with empty subtasks: %v", err)
	}

	// Task should stay in planning (retry on empty plan).
	var updated model.Task
	o.db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning (retry for empty plan), got %s", updated.Status)
	}
}

func TestOnAgentFailed_PlannerRetryThenMax(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "planner-retry-cycle"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-retry",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	// Start at retry_count=0. Each call to onAgentFailed for a planner
	// increments and retries until MaxPlannerRetries is hit.
	taskID := uuid.New()

	task := model.Task{
		ID:           taskID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "planner-retry-task",
		Description:  "retry cycle",
		Status:       model.StatusPlanning,
	}
	o.db.Create(&task)

	for i := 0; i < MaxPlannerRetries; i++ {
		agentID := uuid.New()
		ag := model.Agent{
			ID:            agentID,
			ProjectID:     o.projectID,
			AgentType:     model.AgentPlanner,
			Name:          "planner-iter",
			Status:        model.AgentWorking,
			CurrentTaskID: &taskID,
			WorktreePath:  "/tmp/fake-planner-iter",
		}
		o.db.Create(&ag)

		// Reload task to get current state.
		o.db.First(&task, "id = ?", taskID)
		task.AssignedAgentID = &agentID
		o.db.Save(&task)

		err := o.onAgentFailed(&ag, &task)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}

		o.db.First(&task, "id = ?", taskID)
	}

	// After MaxPlannerRetries iterations, task should be failed.
	var final model.Task
	o.db.First(&final, "id = ?", taskID)
	if final.Status != model.StatusFailed {
		t.Errorf("expected task to be failed after max retries, got %s", final.Status)
	}
}

func TestOnReviewerCompleted_NoReviewJSON(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	// Reviewer with worktree that has no review.json.
	reviewerDir := t.TempDir()

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentReviewer,
		Name:          "test-reviewer-noreview",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  reviewerDir,
	}
	o.db.Create(&ag)

	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "no-review",
		Description: "reviewer produces nothing",
		Status:      model.StatusTestingReady,
	}
	o.db.Create(&task)

	err := o.onReviewerCompleted(&ag, &task)
	if err != nil {
		t.Fatalf("onReviewerCompleted with no review.json: %v", err)
	}

	// Agent should still be idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	// Task context should NOT have review key.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Context != nil {
		if _, ok := updatedTask.Context["review"]; ok {
			t.Error("expected no review in context when review.json missing")
		}
	}
}

// TestFailTask is in lifecycle_test.go
