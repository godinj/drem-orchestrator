package orchestrator

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// findCurrentGroup tests
// ---------------------------------------------------------------------------

func TestFindCurrentGroup_AllComplete_ReturnsNil(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := &model.Task{
		ID:        parentID,
		ProjectID: o.projectID,
		Title:     "parent",
		Status:    model.StatusInProgress,
	}
	db.Create(parent)

	// Create subtasks that are all done.
	sub1ID := uuid.New()
	sub1 := model.Task{
		ID:           sub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "subtask 1",
		Status:       model.StatusDone,
	}
	db.Create(&sub1)

	sub2ID := uuid.New()
	sub2 := model.Task{
		ID:           sub2ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub2",
		Description:  "subtask 2",
		Status:       model.StatusDone,
	}
	db.Create(&sub2)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2ID}},
		},
	}

	group := o.findCurrentGroup(parent, schedule)
	if group != nil {
		t.Errorf("expected nil when all groups complete, got group order %d", group.Order)
	}
}

func TestFindCurrentGroup_FirstGroupIncomplete(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := &model.Task{
		ID:        parentID,
		ProjectID: o.projectID,
		Title:     "parent",
		Status:    model.StatusInProgress,
	}
	db.Create(parent)

	// First group: one subtask still in backlog.
	sub1ID := uuid.New()
	sub1 := model.Task{
		ID:           sub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "not done",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub1)

	// Second group: subtask done.
	sub2ID := uuid.New()
	sub2 := model.Task{
		ID:           sub2ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub2",
		Description:  "done",
		Status:       model.StatusDone,
	}
	db.Create(&sub2)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2ID}},
		},
	}

	group := o.findCurrentGroup(parent, schedule)
	if group == nil {
		t.Fatal("expected non-nil group")
	}
	if group.Order != 0 {
		t.Errorf("expected group order 0, got %d", group.Order)
	}
}

func TestFindCurrentGroup_MissingSubtask_SkipsToNext(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := &model.Task{
		ID:        parentID,
		ProjectID: o.projectID,
		Title:     "parent",
		Status:    model.StatusInProgress,
	}
	db.Create(parent)

	// Group references a subtask ID that doesn't exist in DB.
	missingID := uuid.New()

	sub2ID := uuid.New()
	sub2 := model.Task{
		ID:           sub2ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub2",
		Description:  "in progress",
		Status:       model.StatusInProgress,
	}
	db.Create(&sub2)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{missingID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2ID}},
		},
	}

	// First group has only a missing subtask — treated as terminal.
	// Second group has an in_progress subtask — not terminal.
	group := o.findCurrentGroup(parent, schedule)
	if group == nil {
		t.Fatal("expected non-nil group (second group has active subtask)")
	}
	if group.Order != 1 {
		t.Errorf("expected group order 1, got %d", group.Order)
	}
}

func TestFindCurrentGroup_FailedSubtaskTreatedAsTerminal(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := &model.Task{
		ID:        parentID,
		ProjectID: o.projectID,
		Title:     "parent",
		Status:    model.StatusInProgress,
	}
	db.Create(parent)

	sub1ID := uuid.New()
	sub1 := model.Task{
		ID:           sub1ID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "failed",
		Status:       model.StatusFailed,
	}
	db.Create(&sub1)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1ID}},
		},
	}

	// Failed is terminal, so all groups complete.
	group := o.findCurrentGroup(parent, schedule)
	if group != nil {
		t.Errorf("expected nil when failed subtask in group (terminal), got group order %d", group.Order)
	}
}

// ---------------------------------------------------------------------------
// handlePaused tests
// ---------------------------------------------------------------------------

func TestHandlePaused_NoAgentNoSubtasks(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "paused-no-agent", model.StatusPaused, nil)

	err := o.handlePaused(&task)
	if err != nil {
		t.Fatalf("handlePaused error: %v", err)
	}
	// No panic, no error — baseline success.
}

func TestHandlePaused_ClearsAssignedAgent(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "paused-agent",
		Status:    model.AgentWorking,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		Title:           "paused-with-agent",
		Description:     "task with agent",
		Status:          model.StatusPaused,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	err := orch.handlePaused(&task)
	if err != nil {
		t.Fatalf("handlePaused error: %v", err)
	}

	// Task's assigned agent should be cleared.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID != nil {
		t.Error("expected AssignedAgentID to be nil after handlePaused")
	}
}

func TestHandlePaused_CascadesToSubtasks(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "paused-parent",
		Description: "parent",
		Status:      model.StatusPaused,
	}
	db.Create(&parent)

	// Create subtask with an agent.
	subAgentID := uuid.New()
	subAg := model.Agent{
		ID:        subAgentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "sub-agent-paused",
		Status:    model.AgentWorking,
	}
	db.Create(&subAg)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "sub-with-agent",
		Description:     "subtask",
		Status:          model.StatusInProgress,
		AssignedAgentID: &subAgentID,
	}
	db.Create(&sub)

	err := orch.handlePaused(&parent)
	if err != nil {
		t.Fatalf("handlePaused error: %v", err)
	}

	// Subtask's agent should be cleared.
	var updatedSub model.Task
	db.First(&updatedSub, "id = ?", sub.ID)
	if updatedSub.AssignedAgentID != nil {
		t.Error("expected subtask AssignedAgentID to be nil after parent pause")
	}

	_ = subAg // used for creation
}

// ---------------------------------------------------------------------------
// processPlanning additional branch tests
// ---------------------------------------------------------------------------

func TestProcessPlanning_PlanExists_TransitionsToPlanReview(t *testing.T) {
	o, db := setupLifecycleTest(t)
	wt := o.worktree.(*FakeWorktreeManager)

	plan := makePlan(2)
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "plan-exists",
		Description: "task with plan",
		Status:      model.StatusPlanning,
		Plan:        plan,
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanReview {
		t.Errorf("expected plan_review, got %s", updated.Status)
	}
	expectedBranch := "feature/" + taskFeatureName(&task)
	if updated.WorktreeBranch != expectedBranch {
		t.Fatalf("expected worktree branch %q, got %q", expectedBranch, updated.WorktreeBranch)
	}
	featureName := strings.TrimPrefix(expectedBranch, "feature/")
	if _, ok := wt.Features[featureName]; !ok {
		t.Fatalf("expected feature %q to be created, got %v", featureName, wt.Features)
	}
}

func TestProcessPlanning_AgentDead_ClearsAssignmentAndRetries(t *testing.T) {
	o, db := setupLifecycleTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentPlanner,
		Name:      "dead-planner",
		Status:    model.AgentDead,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "planning-dead-agent",
		Description:     "task with dead agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID != nil {
		t.Error("expected AssignedAgentID to be nil after dead agent cleanup")
	}
	// Status should still be planning (retry).
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected planning (retry), got %s", updated.Status)
	}
}

func TestProcessPlanning_AgentIdle_ClearsAssignment(t *testing.T) {
	o, db := setupLifecycleTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentPlanner,
		Name:      "idle-planner",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "planning-idle-agent",
		Description:     "task with idle agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID != nil {
		t.Error("expected AssignedAgentID to be nil after idle agent cleanup")
	}
}

func TestProcessPlanning_AgentMissing_ClearsAssignment(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// Agent ID that doesn't exist in DB.
	missingAgentID := uuid.New()

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "planning-missing-agent",
		Description:     "task with missing agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &missingAgentID,
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID != nil {
		t.Error("expected AssignedAgentID to be nil after missing agent")
	}
}

func TestProcessPlanning_AgentWorking_NoOp(t *testing.T) {
	o, db := setupLifecycleTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentPlanner,
		Name:      "working-planner",
		Status:    model.AgentWorking,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "planning-working-agent",
		Description:     "task with working agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	// Agent is still working, so nothing should change.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID == nil {
		t.Error("expected AssignedAgentID to remain set while agent is working")
	}
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected planning, got %s", updated.Status)
	}
}

func TestProcessPlanning_AgentDead_MaxRetries_FailsTask(t *testing.T) {
	o, db := setupLifecycleTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentPlanner,
		Name:      "dead-planner-max",
		Status:    model.AgentDead,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "planning-dead-max",
		Description:     "task at max retries",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(MaxPlannerRetries - 1)},
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected failed after max retries, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// onAgentEmptyWork tests
// ---------------------------------------------------------------------------

func TestOnAgentEmptyWork_FirstRetry(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	taskID := uuid.New()
	agentID := uuid.New()

	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "empty-work-agent",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
	}
	o.db.Create(ag)

	task := &model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "empty-work-task",
		Description:     "agent did nothing",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(task)

	err := o.onAgentEmptyWork(ag, task, "no output")
	if err != nil {
		t.Fatalf("onAgentEmptyWork error: %v", err)
	}

	// Agent should be idle.
	var updatedAgent model.Agent
	o.db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}

	// Task should be retried (agent cleared).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.AssignedAgentID != nil {
		t.Error("expected AssignedAgentID to be nil for retry")
	}
	if updatedTask.Context == nil {
		t.Fatal("expected context to be set")
	}
	if v, ok := updatedTask.Context["empty_work"].(bool); !ok || !v {
		t.Error("expected empty_work=true in context")
	}
}

func TestOnAgentEmptyWork_AcceptsReadOnlyIntegrationValidation(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	parentID := uuid.New()
	require.NoError(t, o.db.Create(&model.Task{
		ID: parentID, ProjectID: o.projectID, Title: "parent", Description: "assembled feature",
		Status: model.StatusInProgress,
	}).Error)
	taskID := uuid.New()
	agentID := uuid.New()
	ag := &model.Agent{
		ID: agentID, ProjectID: o.projectID, AgentType: model.AgentCoder,
		Name: "integration-validator", Status: model.AgentWorking, CurrentTaskID: &taskID,
	}
	require.NoError(t, o.db.Create(ag).Error)
	task := &model.Task{
		ID: taskID, ProjectID: o.projectID, ParentTaskID: &parentID,
		Title: "validate assembled feature", Description: "run integration checks",
		Status: model.StatusInProgress, Phase: "integration", AssignedAgentID: &agentID,
	}
	require.NoError(t, o.db.Create(task).Error)

	require.NoError(t, o.onAgentEmptyWork(ag, task, "validated; no edits needed"))

	var updatedTask model.Task
	require.NoError(t, o.db.First(&updatedTask, taskID).Error)
	require.Equal(t, model.StatusDone, updatedTask.Status)
	require.Nil(t, updatedTask.AssignedAgentID)
	require.Equal(t, true, updatedTask.Context["read_only_integration_validation"])
	_, hasEmptyWork := updatedTask.Context["empty_work"]
	require.False(t, hasEmptyWork)
	_, hasRetry := updatedTask.Context["retry_count"]
	require.False(t, hasRetry)

	var updatedAgent model.Agent
	require.NoError(t, o.db.First(&updatedAgent, agentID).Error)
	require.Equal(t, model.AgentIdle, updatedAgent.Status)
}

func TestOnAgentEmptyWork_MaxRetries_FailsTask(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	taskID := uuid.New()
	agentID := uuid.New()

	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "empty-work-max",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
	}
	o.db.Create(ag)

	task := &model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "empty-work-max",
		Description:     "agent at max retries",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(MaxEmptyWorkRetries - 1)},
	}
	o.db.Create(task)

	err := o.onAgentEmptyWork(ag, task, "still nothing")
	if err != nil {
		t.Fatalf("onAgentEmptyWork error: %v", err)
	}

	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected failed after max retries, got %s", updatedTask.Status)
	}
}

// ---------------------------------------------------------------------------
// score_bridge tests
// ---------------------------------------------------------------------------

func TestScorePlanGate(t *testing.T) {
	entries := []planEntry{
		{Phase: "implementation", EstimatedFiles: []string{"a.go"}},
		{Phase: "test", TestsFor: []int{0}, EstimatedFiles: []string{"a_test.go"}},
	}
	validation := PlanValidationResult{Valid: true}
	result := scorePlanGate(entries, nil, validation)

	if result["tdd"] == nil {
		t.Error("expected tdd score")
	}
	if result["constitution"] == nil {
		t.Error("expected constitution score")
	}
	if result["documentation"] == nil {
		t.Error("expected documentation score")
	}
	if result["depth"] == nil {
		t.Error("expected depth score")
	}
	if result["formatted"] == nil {
		t.Error("expected formatted score line")
	}
}

func TestScoreImplGate(t *testing.T) {
	result := scoreImplGate(5, 1, []string{"README.md", "main.go"}, "coverage: 80.0% of statements")

	if result["tdd"] == nil {
		t.Error("expected tdd score")
	}
	tdd, ok := result["tdd"].(float64)
	if !ok {
		t.Fatal("expected tdd to be float64")
	}
	if tdd < 0.79 || tdd > 0.81 {
		t.Errorf("expected tdd ~0.8, got %f", tdd)
	}
	doc, ok := result["documentation"].(float64)
	if !ok {
		t.Fatal("expected documentation to be float64")
	}
	if doc != 1.0 {
		t.Errorf("expected documentation 1.0, got %f", doc)
	}
}

// ---------------------------------------------------------------------------
// resolveIntegrationWorktree tests
// ---------------------------------------------------------------------------

func TestResolveIntegrationWorktree_NoBranch(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "no-branch",
		Description: "task without branch",
		Status:      model.StatusInProgress,
	}
	db.Create(&task)

	result := o.resolveIntegrationWorktree(&task)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestResolveIntegrationWorktree_SubtaskLooksUpParent(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-with-branch",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-feat",
	}
	db.Create(&parent)

	subtask := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub-no-branch",
		Description:  "subtask",
		Status:       model.StatusInProgress,
	}
	db.Create(&subtask)

	// The path won't exist on disk, so it returns empty.
	// But this exercises the parent lookup logic.
	result := o.resolveIntegrationWorktree(&subtask)
	// Path doesn't exist on disk, should return empty.
	if result != "" {
		t.Logf("resolveIntegrationWorktree returned %q (path exists on disk)", result)
	}
}

// ---------------------------------------------------------------------------
// SetTestGateConfig test
// ---------------------------------------------------------------------------

func TestSetTestGateConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// Default config.
	if o.testGate.TestTimeout != 0 {
		// testOrchestrator doesn't set testGate
	}

	// Set config with zero timeout — should default to 5m.
	o.SetTestGateConfig(TestGateConfig{
		TestCommand: "go test ./...",
		ScopedTests: true,
	})
	if o.testGate.TestTimeout != 5*time.Minute {
		t.Errorf("expected 5m default timeout, got %v", o.testGate.TestTimeout)
	}

	// Set config with explicit timeout.
	o.SetTestGateConfig(TestGateConfig{
		TestCommand: "npm test",
		TestTimeout: 10 * time.Minute,
	})
	if o.testGate.TestTimeout != 10*time.Minute {
		t.Errorf("expected 10m timeout, got %v", o.testGate.TestTimeout)
	}
	if o.testGate.TestCommand != "npm test" {
		t.Errorf("expected test command 'npm test', got %q", o.testGate.TestCommand)
	}
}

// ---------------------------------------------------------------------------
// HandleTestReviewApproved / HandleTestReviewRejected tests
// ---------------------------------------------------------------------------

func TestHandleTestReviewApproved(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-review-approved", model.StatusTestReview, nil)

	if err := o.HandleTestReviewApproved(task.ID); err != nil {
		t.Fatalf("HandleTestReviewApproved error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected in_progress after test review approved, got %s", updated.Status)
	}
}

func TestHandleTestReviewApproved_InvalidStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-review-wrong", model.StatusInProgress, nil)

	err := o.HandleTestReviewApproved(task.ID)
	if err == nil {
		t.Fatal("expected error for wrong status")
	}
}

func TestHandleTestReviewApproved_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	err := o.HandleTestReviewApproved(uuid.New())
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestHandleTestReviewRejected_FirstRound(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	task := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "test-review-reject-first",
		Description: "parent task",
		Status:      model.StatusTestReview,
	}
	db.Create(&task)

	// Create a done test subtask.
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	err := o.HandleTestReviewRejected(parentID, "tests are wrong")
	if err != nil {
		t.Fatalf("HandleTestReviewRejected error: %v", err)
	}

	// Parent should transition to test_writing.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected test_writing, got %s", updated.Status)
	}

	// Verify rejection count.
	if updated.Context == nil {
		t.Fatal("expected context")
	}
	if v, ok := updated.Context["test_rejection_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected test_rejection_count=1, got %v", updated.Context["test_rejection_count"])
	}

	// Original subtask should be rejected.
	var updatedSub model.Task
	db.First(&updatedSub, "id = ?", sub.ID)
	if updatedSub.Status != model.StatusRejected {
		t.Errorf("expected subtask status rejected, got %s", updatedSub.Status)
	}

	// A replacement subtask should exist.
	var replacements []model.Task
	db.Where("parent_task_id = ? AND status = ?", parentID, model.StatusBacklog).Find(&replacements)
	if len(replacements) != 1 {
		t.Errorf("expected 1 replacement subtask, got %d", len(replacements))
	}
}

func TestHandleTestReviewRejected_InvalidStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "review-reject-wrong", model.StatusInProgress, nil)

	err := o.HandleTestReviewRejected(task.ID, "feedback")
	if err == nil {
		t.Fatal("expected error for wrong status")
	}
}

// ---------------------------------------------------------------------------
// DeleteComment tests
// ---------------------------------------------------------------------------

func TestDeleteComment_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	// Deleting a nonexistent comment should not error in GORM (soft behavior).
	err := o.DeleteComment(uuid.New())
	if err != nil {
		t.Fatalf("DeleteComment error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Emit coverage (internal helpers)
// ---------------------------------------------------------------------------

func TestEmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	events := make(chan Event, 10)
	o := &Orchestrator{
		db:     db,
		events: events,
		logger: slog.Default().With("component", "test"),
	}

	o.emit("test_event", map[string]string{"key": "value"})

	select {
	case evt := <-events:
		if evt.Type != "test_event" {
			t.Errorf("expected event type test_event, got %s", evt.Type)
		}
	default:
		t.Error("expected an event to be emitted")
	}
}

func TestEmit_NilChannel(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := &Orchestrator{
		db:     db,
		events: nil,
		logger: slog.Default().With("component", "test"),
	}

	// Should not panic with nil channel.
	o.emit("test_event", nil)
}

// ---------------------------------------------------------------------------
// defaultTestGateConfig test
// ---------------------------------------------------------------------------

func TestDefaultTestGateConfig_Values(t *testing.T) {
	cfg := defaultTestGateConfig()
	if !cfg.ScopedTests {
		t.Error("expected ScopedTests to be true by default")
	}
	if cfg.TestTimeout != 5*time.Minute {
		t.Errorf("expected 5m timeout, got %v", cfg.TestTimeout)
	}
	if cfg.TestCommand != "" {
		t.Errorf("expected empty test command, got %q", cfg.TestCommand)
	}
}

// ---------------------------------------------------------------------------
// DependenciesMet additional tests
// ---------------------------------------------------------------------------

func TestDependenciesMet_AllDone(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Create done tasks.
	id1 := uuid.New()
	id2 := uuid.New()
	for _, id := range []uuid.UUID{id1, id2} {
		task := model.Task{
			ID:          id,
			Title:       "dep-" + id.String()[:8],
			Description: "dep",
			Status:      model.StatusDone,
		}
		db.Create(&task)
	}

	met, err := DependenciesMet(db, model.JSONArray{id1.String(), id2.String()})
	if err != nil {
		t.Fatalf("DependenciesMet error: %v", err)
	}
	if !met {
		t.Error("expected dependencies to be met")
	}
}

func TestDependenciesMet_SomeNotDone(t *testing.T) {
	db := testutil.NewTestDB(t)

	id1 := uuid.New()
	id2 := uuid.New()
	db.Create(&model.Task{
		ID:          id1,
		Title:       "dep-done",
		Description: "dep",
		Status:      model.StatusDone,
	})
	db.Create(&model.Task{
		ID:          id2,
		Title:       "dep-inprog",
		Description: "dep",
		Status:      model.StatusInProgress,
	})

	met, err := DependenciesMet(db, model.JSONArray{id1.String(), id2.String()})
	if err != nil {
		t.Fatalf("DependenciesMet error: %v", err)
	}
	if met {
		t.Error("expected dependencies to NOT be met")
	}
}

func TestDependenciesMet_EmptyDeps(t *testing.T) {
	db := testutil.NewTestDB(t)

	met, err := DependenciesMet(db, model.JSONArray{})
	if err != nil {
		t.Fatalf("DependenciesMet error: %v", err)
	}
	if !met {
		t.Error("expected empty dependencies to be met")
	}
}

// ---------------------------------------------------------------------------
// isTestSubtask tests
// ---------------------------------------------------------------------------

func TestIsTestSubtask(t *testing.T) {
	tests := []struct {
		name     string
		entry    planEntry
		expected bool
	}{
		{"phase=test", planEntry{Phase: "test"}, true},
		{"phase=implementation", planEntry{Phase: "implementation"}, false},
		{"is_test flag", planEntry{IsTest: true}, true},
		{"title contains test as word", planEntry{Title: "Add tests for auth"}, true},
		{"title starts with test", planEntry{Title: "Test integration flow"}, true},
		{"title ends with test", planEntry{Title: "Unit test"}, true},
		{"title has testing keyword", planEntry{Title: "Unit testing suite"}, true},
		{"title has test in middle", planEntry{Title: "Add test utilities"}, true},
		{"title no test word", planEntry{Title: "Implement auth module"}, false},
		{"title contest not matched", planEntry{Title: "Run the latest contest"}, false},
		{"title attestation not matched", planEntry{Title: "Add attestation support"}, false},
		{"empty entry", planEntry{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isTestSubtask(tc.entry)
			if result != tc.expected {
				t.Errorf("isTestSubtask(%+v) = %v, want %v", tc.entry, result, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// findMissingTestDependencies tests
// ---------------------------------------------------------------------------

func TestFindMissingTestDependencies(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Impl B", Phase: "implementation"},
		{Title: "Test C", Phase: "test", Dependencies: []int{0}},
	}

	// Test subtask at index 2 depends on 0 but not 1.
	missing := findMissingTestDependencies(subtasks, 2)
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing, got %d", len(missing))
	}
	if missing[0] != 1 {
		t.Errorf("expected missing index 1, got %d", missing[0])
	}
}

func TestFindMissingTestDependencies_AllCovered(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Test B", Phase: "test", Dependencies: []int{0}},
	}

	missing := findMissingTestDependencies(subtasks, 1)
	if len(missing) != 0 {
		t.Errorf("expected no missing, got %d", len(missing))
	}
}

// ---------------------------------------------------------------------------
// validateTDDExceptions tests
// ---------------------------------------------------------------------------

func TestValidateTDDExceptions_OutOfBounds(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
	}
	exceptions := []tddException{
		{SubtaskIndex: 5, Reason: "out of range"},
	}

	result := &PlanValidationResult{Valid: true}
	validateTDDExceptions(subtasks, exceptions, result)

	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error for out-of-bounds, got %d", len(result.Errors))
	}
}

func TestValidateTDDExceptions_TestPhase(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Test A", Phase: "test"},
	}
	exceptions := []tddException{
		{SubtaskIndex: 0, Reason: "wrong phase"},
	}

	result := &PlanValidationResult{Valid: true}
	validateTDDExceptions(subtasks, exceptions, result)

	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error for test phase exception, got %d", len(result.Errors))
	}
}

func TestValidateTDDExceptions_TooManyExemptions(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Impl B", Phase: "implementation"},
	}
	// Both implementation subtasks exempted (100% > 50%).
	exceptions := []tddException{
		{SubtaskIndex: 0, Reason: "trivial"},
		{SubtaskIndex: 1, Reason: "config only"},
	}

	result := &PlanValidationResult{Valid: true}
	validateTDDExceptions(subtasks, exceptions, result)

	if len(result.Warnings) != 1 {
		t.Errorf("expected 1 warning for excessive exemptions, got %d", len(result.Warnings))
	}
}

func TestValidateTDDExceptions_ValidExceptions(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Impl B", Phase: "implementation"},
		{Title: "Impl C", Phase: "implementation"},
		{Title: "Test D", Phase: "test"},
	}
	// Only one of three impl subtasks exempted (33% < 50%).
	exceptions := []tddException{
		{SubtaskIndex: 0, Reason: "config only"},
	}

	result := &PlanValidationResult{Valid: true}
	validateTDDExceptions(subtasks, exceptions, result)

	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// validateLegacyTestOrdering tests
// ---------------------------------------------------------------------------

func TestValidateLegacyTestOrdering_MissingDeps(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Implement feature A"},
		{Title: "Add tests", IsTest: true, Dependencies: nil}, // doesn't depend on impl
	}

	result := &PlanValidationResult{Valid: true}
	validateLegacyTestOrdering(subtasks, result)

	if len(result.Warnings) != 1 {
		t.Errorf("expected 1 warning for missing test dependency, got %d", len(result.Warnings))
	}
}

func TestValidateLegacyTestOrdering_AllDeps(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Implement feature A"},
		{Title: "Add tests", IsTest: true, Dependencies: []int{0}},
	}

	result := &PlanValidationResult{Valid: true}
	validateLegacyTestOrdering(subtasks, result)

	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// validatePhaseOrdering tests
// ---------------------------------------------------------------------------

func TestValidatePhaseOrdering_TestDependsOnImpl(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Test B", Phase: "test", Dependencies: []int{0}},
	}

	result := &PlanValidationResult{Valid: true}
	validatePhaseOrdering(subtasks, result)

	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error for test depending on impl, got %d", len(result.Errors))
	}
}

func TestValidatePhaseOrdering_NoViolation(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Test A", Phase: "test"},
		{Title: "Impl B", Phase: "implementation", Dependencies: []int{0}},
	}

	result := &PlanValidationResult{Valid: true}
	validatePhaseOrdering(subtasks, result)

	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

// ---------------------------------------------------------------------------
// hasFileOverlap tests
// ---------------------------------------------------------------------------

func TestHasFileOverlap(t *testing.T) {
	tests := []struct {
		name     string
		filesA   []string
		filesB   []string
		expected bool
	}{
		{"no overlap", []string{"a.go"}, []string{"b.go"}, false},
		{"has overlap", []string{"a.go", "b.go"}, []string{"b.go", "c.go"}, true},
		{"empty A", nil, []string{"b.go"}, false},
		{"empty B", []string{"a.go"}, nil, false},
		{"both empty", nil, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := hasFileOverlap(tc.filesA, tc.filesB)
			if result != tc.expected {
				t.Errorf("hasFileOverlap() = %v, want %v", result, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hasLinesException tests
// ---------------------------------------------------------------------------

func TestHasLinesException(t *testing.T) {
	// nil exceptions.
	result := hasLinesException(nil, "foo.go")
	if result {
		t.Error("expected false for nil exceptions")
	}

	// With matching exception.
	exceptions := []constraints.LinesException{
		{Path: "foo.go", BaselineLines: 100},
	}
	result = hasLinesException(exceptions, "foo.go")
	if !result {
		t.Error("expected true for matching exception")
	}

	// No match.
	result = hasLinesException(exceptions, "bar.go")
	if result {
		t.Error("expected false for non-matching path")
	}
}

// ---------------------------------------------------------------------------
// matchesGlob tests
// ---------------------------------------------------------------------------

func TestMatchesGlob(t *testing.T) {
	tests := []struct {
		pattern  string
		path     string
		expected bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "internal/foo/bar.go", true},
		{"internal/*.go", "internal/foo.go", true},
		{"*.txt", "main.go", false},
		{"*.go", "README.md", false},
	}

	for _, tc := range tests {
		t.Run(tc.pattern+"_"+tc.path, func(t *testing.T) {
			result := matchesGlob(tc.pattern, tc.path)
			if result != tc.expected {
				t.Errorf("matchesGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, result, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// goPackages tests
// ---------------------------------------------------------------------------

func TestGoPackages(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		expected int
	}{
		{"no files", nil, 0},
		{"no go files", []string{"README.md"}, 0},
		{"root go file", []string{"main.go"}, 0},
		{"one package", []string{"internal/foo/bar.go"}, 1},
		{"two packages", []string{"internal/foo/a.go", "internal/bar/b.go"}, 2},
		{"dedup same package", []string{"internal/foo/a.go", "internal/foo/b.go"}, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := goPackages(tc.files)
			if len(result) != tc.expected {
				t.Errorf("goPackages(%v) returned %d packages, want %d: %v", tc.files, len(result), tc.expected, result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// planTouchesDocFiles tests
// ---------------------------------------------------------------------------

func TestPlanTouchesDocFiles(t *testing.T) {
	tests := []struct {
		name     string
		entries  []planEntry
		expected bool
	}{
		{"no entries", nil, false},
		{"no doc files", []planEntry{{EstimatedFiles: []string{"main.go"}}}, false},
		{"has readme", []planEntry{{EstimatedFiles: []string{"readme.md"}}}, true},
		{"has docs dir", []planEntry{{EstimatedFiles: []string{"docs/guide.md"}}}, true},
		{"has doc dir", []planEntry{{EstimatedFiles: []string{"doc/api.md"}}}, true},
		{"has Files field", []planEntry{{Files: []string{"docs/guide.md"}}}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := planTouchesDocFiles(tc.entries)
			if result != tc.expected {
				t.Errorf("planTouchesDocFiles() = %v, want %v", result, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MergeTDDDependencies tests
// ---------------------------------------------------------------------------

func TestMergeTDDDependencies_AddsImplDepsOnTest(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Test A", Phase: "test", TestsFor: []int{0}},
	}

	result := MergeTDDDependencies(subtasks)

	// Impl A (index 0) should now depend on Test A (index 1).
	if len(result[0].Dependencies) != 1 || result[0].Dependencies[0] != 1 {
		t.Errorf("expected Impl A to depend on Test A, got deps %v", result[0].Dependencies)
	}
}

func TestMergeTDDDependencies_IntegrationDependsOnImpl(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation"},
		{Title: "Impl B", Phase: "implementation"},
		{Title: "Integration", Phase: "integration"},
	}

	result := MergeTDDDependencies(subtasks)

	// Integration subtask (index 2) should depend on both impl subtasks.
	if len(result[2].Dependencies) != 2 {
		t.Errorf("expected Integration to have 2 dependencies, got %v", result[2].Dependencies)
	}
}

func TestMergeTDDDependencies_PreservesExistingDeps(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl A", Phase: "implementation", Dependencies: []int{2}},
		{Title: "Test A", Phase: "test", TestsFor: []int{0}},
		{Title: "Setup", Phase: "implementation"},
	}

	result := MergeTDDDependencies(subtasks)

	// Impl A should keep its existing dep (2) and gain test dep (1).
	if len(result[0].Dependencies) != 2 {
		t.Errorf("expected Impl A to have 2 dependencies, got %v", result[0].Dependencies)
	}
}

// ---------------------------------------------------------------------------
// computeFileOverlaps tests
// ---------------------------------------------------------------------------

func TestComputeFileOverlaps(t *testing.T) {
	subtasks := []planEntry{
		{Title: "A", EstimatedFiles: []string{"shared.go", "a.go"}},
		{Title: "B", EstimatedFiles: []string{"shared.go", "b.go"}},
		{Title: "C", EstimatedFiles: []string{"c.go"}},
	}

	overlaps := computeFileOverlaps(subtasks)
	if len(overlaps) != 1 {
		t.Errorf("expected 1 overlap pair, got %d", len(overlaps))
	}
	if len(overlaps) > 0 && len(overlaps[0].Files) != 1 {
		t.Errorf("expected 1 shared file, got %d", len(overlaps[0].Files))
	}
}

func TestComputeFileOverlaps_NoOverlap(t *testing.T) {
	subtasks := []planEntry{
		{Title: "A", EstimatedFiles: []string{"a.go"}},
		{Title: "B", EstimatedFiles: []string{"b.go"}},
	}

	overlaps := computeFileOverlaps(subtasks)
	if len(overlaps) != 0 {
		t.Errorf("expected 0 overlaps, got %d", len(overlaps))
	}
}

// ---------------------------------------------------------------------------
// ValidatePlan integration test
// ---------------------------------------------------------------------------

func TestValidatePlan_ValidTDDPlan(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Test auth", Phase: "test", TestsFor: []int{1}, EstimatedFiles: []string{"auth_test.go"}},
		{Title: "Impl auth", Phase: "implementation", Dependencies: []int{0}, EstimatedFiles: []string{"auth.go"}},
	}

	result := ValidatePlan(subtasks, nil)
	if !result.Valid {
		t.Errorf("expected valid plan, got errors: %v", result.Errors)
	}
}

func TestValidatePlan_MissingTestCoverage(t *testing.T) {
	subtasks := []planEntry{
		{Title: "Impl auth", Phase: "implementation", EstimatedFiles: []string{"auth.go"}},
		{Title: "Impl db", Phase: "implementation", EstimatedFiles: []string{"db.go"}},
	}

	result := ValidatePlan(subtasks, nil)
	if result.Valid {
		t.Error("expected invalid plan due to missing test coverage")
	}
	if len(result.Errors) < 1 {
		t.Error("expected at least 1 error for uncovered impl subtasks")
	}
}

// ---------------------------------------------------------------------------
// hasCycle tests
// ---------------------------------------------------------------------------

func TestHasCycle_NoCycle(t *testing.T) {
	subtasks := []planEntry{
		{Dependencies: nil},
		{Dependencies: []int{0}},
		{Dependencies: []int{1}},
	}

	if hasCycle(subtasks) {
		t.Error("expected no cycle")
	}
}

func TestHasCycle_WithCycle(t *testing.T) {
	subtasks := []planEntry{
		{Dependencies: []int{2}},
		{Dependencies: []int{0}},
		{Dependencies: []int{1}},
	}

	if !hasCycle(subtasks) {
		t.Error("expected cycle to be detected")
	}
}

// ---------------------------------------------------------------------------
// hasDependency tests
// ---------------------------------------------------------------------------

func TestHasDependency(t *testing.T) {
	subtasks := []planEntry{
		{},
		{Dependencies: []int{0}},
		{},
	}

	// hasDependency checks bidirectionally.
	if !hasDependency(subtasks, 0, 1) {
		t.Error("expected dependency between 0 and 1")
	}
	if !hasDependency(subtasks, 1, 0) {
		t.Error("expected dependency between 1 and 0 (bidirectional)")
	}
	if hasDependency(subtasks, 0, 2) {
		t.Error("expected no dependency between 0 and 2")
	}
}

// ---------------------------------------------------------------------------
// allFiles tests
// ---------------------------------------------------------------------------

func TestAllFiles_PrefersFiles(t *testing.T) {
	entry := planEntry{
		Files:          []string{"a.go"},
		EstimatedFiles: []string{"b.go", "c.go"},
	}

	result := allFiles(entry)
	// allFiles returns Files if non-empty, ignoring EstimatedFiles.
	if len(result) != 1 || result[0] != "a.go" {
		t.Errorf("expected [a.go] from Files field, got %v", result)
	}
}

func TestAllFiles_FallsBackToEstimated(t *testing.T) {
	entry := planEntry{
		EstimatedFiles: []string{"b.go", "c.go"},
	}

	result := allFiles(entry)
	if len(result) != 2 {
		t.Errorf("expected 2 files from EstimatedFiles, got %d: %v", len(result), result)
	}
}

func TestAllFiles_EmptyEntry(t *testing.T) {
	entry := planEntry{}
	result := allFiles(entry)
	if len(result) != 0 {
		t.Errorf("expected 0 files, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// inferTestCommand / inferCompileCommand tests
// ---------------------------------------------------------------------------

func TestInferTestCommand(t *testing.T) {
	dir := t.TempDir()

	// No files -> empty.
	result := inferTestCommand(dir)
	if result != "" {
		t.Errorf("expected empty for unknown project, got %q", result)
	}

	// go.mod -> go test ./...
	writeFile(t, dir, "go.mod", "module test")
	result = inferTestCommand(dir)
	if result != "go test ./..." {
		t.Errorf("expected 'go test ./...', got %q", result)
	}
}

func TestInferTestCommand_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", "{}")
	result := inferTestCommand(dir)
	if result != "npm test" {
		t.Errorf("expected 'npm test', got %q", result)
	}
}

func TestInferTestCommand_PyProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.pytest]")
	result := inferTestCommand(dir)
	if result != "pytest" {
		t.Errorf("expected 'pytest', got %q", result)
	}
}

func TestInferTestCommand_Cargo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]")
	result := inferTestCommand(dir)
	if result != "cargo test" {
		t.Errorf("expected 'cargo test', got %q", result)
	}
}

func TestInferCompileCommand(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		content  string
		expected string
	}{
		{"go.mod", "go.mod", "module test", "go vet ./..."},
		{"tsconfig", "tsconfig.json", "{}", "npx tsc --noEmit"},
		{"Cargo", "Cargo.toml", "[package]", "cargo check"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tc.file, tc.content)
			result := inferCompileCommand(dir)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestInferCompileCommand_NoProjectFile(t *testing.T) {
	dir := t.TempDir()
	result := inferCompileCommand(dir)
	if result != "" {
		t.Errorf("expected empty for unknown project, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// getTestCommand tests
// ---------------------------------------------------------------------------

func TestGetTestCommand_ConfiguredCommand(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate.TestCommand = "make test"

	task := &model.Task{
		ID:     uuid.New(),
		Title:  "test-cmd",
		Status: model.StatusInProgress,
	}

	result := o.getTestCommand(task)
	if result != "make test" {
		t.Errorf("expected 'make test', got %q", result)
	}
}

func TestGetTestCommand_NoCommandNoAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	task := &model.Task{
		ID:     uuid.New(),
		Title:  "no-cmd",
		Status: model.StatusInProgress,
	}

	result := o.getTestCommand(task)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestGetTestCommand_InfersFromAgentWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module test")

	agentID := uuid.New()
	ag := model.Agent{
		ID:           agentID,
		AgentType:    model.AgentCoder,
		Name:         "infer-agent",
		Status:       model.AgentWorking,
		WorktreePath: dir,
	}
	db.Create(&ag)

	task := &model.Task{
		ID:              uuid.New(),
		Title:           "infer-test",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}

	result := o.getTestCommand(task)
	if result != "go test ./..." {
		t.Errorf("expected 'go test ./...', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// processTestWriting tests
// ---------------------------------------------------------------------------

func TestProcessTestWriting_NoTestSubtasks(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "tw-no-subs",
		Description:    "parent with no test subtasks",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/tw-no-subs",
	}
	db.Create(&parent)

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	// No test subtasks -> recovery policy sends task back to planning.
	var updated model.Task
	db.First(&updated, "id = ?", parent.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected planning (recovery replan on empty subtasks), got %s", updated.Status)
	}
}

func TestProcessTestWriting_AllTestSubtasksDone(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "tw-all-done",
		Description: "parent with done test subtasks",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-done",
		Description:  "done test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestReview {
		t.Errorf("expected test_review when all test subs done, got %s", updated.Status)
	}
}

func TestProcessTestWriting_AllTerminalSomeFailed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "tw-some-failed",
		Description: "parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-done",
		Description:  "done test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	})
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-failed",
		Description:  "failed test subtask",
		Status:       model.StatusFailed,
		Phase:        "test",
	})

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected failed when all terminal and some failed, got %s", updated.Status)
	}
}

func TestProcessTestWriting_CancelledSubtasksDontBlockCompletion(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "tw-cancelled-mixed",
		Description: "parent with done + cancelled test subtasks",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	// One done, one cancelled — should still transition to test_review.
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-done",
		Description:  "done test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	})
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-cancelled",
		Description:  "cancelled test subtask",
		Status:       model.StatusCancelled,
		Phase:        "test",
	})

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestReview {
		t.Errorf("expected test_review when done+cancelled (no in-progress), got %s", updated.Status)
	}
}

func TestProcessTestWriting_StillInProgress(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "tw-in-progress",
		Description: "parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "test-wip",
		Description:  "in progress test subtask",
		Status:       model.StatusInProgress,
		Phase:        "test",
	})

	err := orch.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected test_writing (test sub still in progress), got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// warnSamePackageTestOverlap tests
// ---------------------------------------------------------------------------

func TestWarnSamePackageTestOverlap_NoOverlap(t *testing.T) {
	subtasks := []planEntry{
		{Phase: "test", EstimatedFiles: []string{"internal/foo/a_test.go"}},
		{Phase: "test", EstimatedFiles: []string{"internal/bar/b_test.go"}},
	}

	result := &PlanValidationResult{Valid: true}
	warnSamePackageTestOverlap(subtasks, result)

	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings for different packages, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

func TestWarnSamePackageTestOverlap_SamePackage(t *testing.T) {
	subtasks := []planEntry{
		{Phase: "test", EstimatedFiles: []string{"internal/foo/a_test.go"}},
		{Phase: "test", EstimatedFiles: []string{"internal/foo/b_test.go"}},
	}

	result := &PlanValidationResult{Valid: true}
	warnSamePackageTestOverlap(subtasks, result)

	if len(result.Warnings) != 1 {
		t.Errorf("expected 1 warning for same package, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

func TestWarnSamePackageTestOverlap_WithDependency(t *testing.T) {
	subtasks := []planEntry{
		{Phase: "test", EstimatedFiles: []string{"internal/foo/a_test.go"}, Dependencies: []int{1}},
		{Phase: "test", EstimatedFiles: []string{"internal/foo/b_test.go"}},
	}

	result := &PlanValidationResult{Valid: true}
	warnSamePackageTestOverlap(subtasks, result)

	// Pairs with dependencies are skipped.
	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings for dependent tests, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// countFileLines tests
// ---------------------------------------------------------------------------

func TestCountFileLines(t *testing.T) {
	dir := t.TempDir()

	// Write a file with known lines.
	path := dir + "/test.txt"
	writeFile(t, dir, "test.txt", "line1\nline2\nline3\n")

	count, err := countFileLines(path)
	if err != nil {
		t.Fatalf("countFileLines error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 lines, got %d", count)
	}
}

func TestCountFileLines_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/empty.txt"
	writeFile(t, dir, "empty.txt", "")

	count, err := countFileLines(path)
	if err != nil {
		t.Fatalf("countFileLines error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 lines, got %d", count)
	}
}

func TestCountFileLines_Nonexistent(t *testing.T) {
	_, err := countFileLines("/tmp/nonexistent-file-12345")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// ValidatePlanConstraints tests
// ---------------------------------------------------------------------------

func TestValidatePlanConstraints_NilConfig(t *testing.T) {
	subtasks := []planEntry{
		{Title: "test", EstimatedFiles: []string{"a.go"}},
	}

	result := ValidatePlanConstraints(subtasks, nil, "")
	if !result.Valid {
		t.Error("expected valid for nil config")
	}
}

// ---------------------------------------------------------------------------
// Helper for test file creation
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// onAgentFailed additional tests — already merged path
// ---------------------------------------------------------------------------

func TestOnAgentFailed_CoderNoAgentBranch(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "fail-no-branch"
	createFeatureWorktree(t, bareRepoPath, featureName)

	o, _ := agentResultOrchestrator(t, bareRepoPath)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent-fail-no-branch",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	o.db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "fail-no-branch-agent",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		// No WorktreeBranch set — agent has no branch to clean up.
	}
	o.db.Create(ag)

	task := &model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "fail-no-branch-sub",
		Description:     "subtask",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	o.db.Create(task)

	err := o.onAgentFailed(ag, task)
	if err != nil {
		t.Fatalf("onAgentFailed error: %v", err)
	}

	// Task should be failed (no work to salvage).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected task failed, got %s", updatedTask.Status)
	}

	// Error context should be set.
	if updatedTask.Context == nil || updatedTask.Context["last_error"] == nil {
		t.Error("expected last_error in context")
	}
}

// ---------------------------------------------------------------------------
// processTestingReady tests
// ---------------------------------------------------------------------------

func TestProcessTestingReady_BusyAgent(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "testing-ready-busy",
		Description: "parent with busy reviewer",
		Status:      model.StatusTestingReady,
	}
	db.Create(&parent)

	// Create a working reviewer for this task.
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentReviewer,
		Name:          "busy-reviewer",
		Status:        model.AgentWorking,
		CurrentTaskID: &parentID,
	}
	db.Create(&ag)

	err := o.processTestingReady(&parent)
	if err != nil {
		t.Fatalf("processTestingReady error: %v", err)
	}

	// Should be a no-op since reviewer is already running.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (busy agent), got %s", updated.Status)
	}
}

func TestProcessTestingReady_AlreadyPassed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "testing-ready-passed",
		Description: "parent that already passed",
		Status:      model.StatusTestingReady,
		Context:     model.JSONField{"automated_gate_passed": true},
	}
	db.Create(&parent)

	err := o.processTestingReady(&parent)
	if err == nil || !strings.Contains(err.Error(), "canonical feature branch") {
		t.Fatalf("processTestingReady should fail closed without accepted branch: %v", err)
	}

	// Should be a no-op since already passed.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (already passed), got %s", updated.Status)
	}
}

func TestProcessTestingReady_NoAcceptedBranchFailsClosed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "testing-ready-no-wt",
		Description: "parent without worktree",
		Status:      model.StatusTestingReady,
	}
	db.Create(&parent)

	err := o.processTestingReady(&parent)
	if err == nil || !strings.Contains(err.Error(), "canonical feature branch") {
		t.Fatalf("processTestingReady should fail closed: %v", err)
	}

	// No worktree -> no-op.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (no worktree), got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// processBacklog with PlanFeedback and no old subtasks
// ---------------------------------------------------------------------------

func TestProcessBacklog_WithPlanFeedback_NoOldSubtasks(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		Title:        "feedback-no-subs",
		Description:  "task with feedback but no old subtasks",
		Status:       model.StatusBacklog,
		PlanFeedback: "need better plan",
	}
	db.Create(&task)

	err := o.processBacklog(&task)
	if err != nil {
		t.Fatalf("processBacklog error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected planning, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// Truncate utility test
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello"},
		{"empty", "", 5, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := truncate(tc.input, tc.maxLen)
			if len(result) > tc.maxLen {
				t.Errorf("expected max len %d, got %d", tc.maxLen, len(result))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper for test file creation
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// isTestFile tests (many branches to cover)
// ---------------------------------------------------------------------------

func TestIsTestFile_Variants(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		expected bool
	}{
		{"go test file", "foo_test.go", true},
		{"go regular file", "foo.go", false},
		{"python test prefix", "test_foo.py", true},
		{"python test suffix", "foo_test.py", true},
		{"python regular", "foo.py", false},
		{"js test", "foo.test.js", true},
		{"ts test", "foo.test.ts", true},
		{"js spec", "foo.spec.js", true},
		{"ts spec", "foo.spec.ts", true},
		{"tsx test", "Component.test.tsx", true},
		{"jsx spec", "Component.spec.jsx", true},
		{"cpp test suffix", "FooTest.cpp", true},
		{"cpp tests suffix", "FooTests.cpp", true},
		{"cpp underscore test", "foo_test.cpp", true},
		{"cpp test header", "FooTest.h", true},
		{"cpp regular", "foo.cpp", false},
		{"cpp in test dir", "tests/helper.cpp", true},
		{"cpp in test subdir", "src/tests/helper.h", true},
		{"header not in test dir", "src/foo.h", false},
		{"random file", "readme.txt", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isTestFile(tc.file)
			if result != tc.expected {
				t.Errorf("isTestFile(%q) = %v, want %v", tc.file, result, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// runCommand tests
// ---------------------------------------------------------------------------

func TestRunCommand_Success(t *testing.T) {
	dir := t.TempDir()
	result, err := runCommand(dir, "echo hello")
	if err != nil {
		t.Fatalf("runCommand error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Output == "" {
		t.Error("expected non-empty output")
	}
}

func TestRunCommand_Failure(t *testing.T) {
	dir := t.TempDir()
	result, err := runCommand(dir, "exit 1")
	if err != nil {
		t.Fatalf("runCommand error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
}

func TestRunCommand_Output(t *testing.T) {
	dir := t.TempDir()
	result, err := runCommand(dir, "echo hello && echo world >&2")
	if err != nil {
		t.Fatalf("runCommand error: %v", err)
	}
	// Both stdout and stderr should be captured.
	if result.Output == "" {
		t.Error("expected output to capture stdout and stderr")
	}
}

// ---------------------------------------------------------------------------
// fileExistsAt tests
// ---------------------------------------------------------------------------

func TestFileExistsAt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "exists.txt", "content")

	if !fileExistsAt(dir + "/exists.txt") {
		t.Error("expected true for existing file")
	}
	if fileExistsAt(dir + "/nonexistent.txt") {
		t.Error("expected false for nonexistent file")
	}
	if fileExistsAt(dir) {
		t.Error("expected false for directory")
	}
}

// ---------------------------------------------------------------------------
// taskPhase utility test
// ---------------------------------------------------------------------------

func TestTaskPhase_Variants(t *testing.T) {
	task := &model.Task{
		ID:      uuid.New(),
		Title:   "phase-test",
		Context: model.JSONField{"phase": "test"},
	}
	result := taskPhase(task)
	if result != "test" {
		t.Errorf("expected 'test', got %q", result)
	}

	// No context.
	task2 := &model.Task{ID: uuid.New(), Title: "no-ctx"}
	result = taskPhase(task2)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}

	// Context without phase.
	task3 := &model.Task{
		ID:      uuid.New(),
		Title:   "no-phase",
		Context: model.JSONField{"other": "value"},
	}
	result = taskPhase(task3)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// checkForCompilableTests test (basic coverage)
// ---------------------------------------------------------------------------

func TestCheckForCompilableTests_NonexistentDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	result := o.checkForCompilableTests("/tmp/nonexistent-path-xyz")
	if result {
		t.Error("expected false for nonexistent directory")
	}
}

// ---------------------------------------------------------------------------
// processTestingReady with actual test suite run
// ---------------------------------------------------------------------------

func TestProcessTestingReady_CodeFailureReturnsToImplementation(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "testing-ready-fixer"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	// Add a change to the feature branch.
	writeFile(t, featureDir, "feature.txt", "feature content")
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add feature")

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "testing-ready-fixer-test",
		Description:    "parent",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
		Context:        model.JSONField{"testing_ready_fixer_attempted": true},
	}
	recordBranchAcceptanceForTest(t, &parent, featureDir, "main")
	db.Create(&parent)
	persistBranchAcceptanceForTest(t, db, &parent)

	// The isolated command fails because the fixture is not a Go project.
	// Legacy fixer flags must not change deterministic failure routing.
	err := orch.processTestingReady(&parent)
	if err != nil {
		t.Fatalf("processTestingReady error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Fatalf("expected code failure to return to in_progress, got %s", updated.Status)
	}
	var gateRun model.PreliminaryGateRun
	if err := db.Where("task_id = ?", parent.ID).First(&gateRun).Error; err != nil {
		t.Fatalf("load preliminary gate run: %v", err)
	}
	if gateRun.Outcome != model.PreliminaryGateCodeFailure {
		t.Fatalf("expected code outcome, got %s", gateRun.Outcome)
	}
}

// ---------------------------------------------------------------------------
// storeTestResult tests
// ---------------------------------------------------------------------------

func TestStoreTestResult(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		AgentType: model.AgentCoder,
		Name:      "store-result-agent",
		Status:    model.AgentWorking,
	}
	db.Create(&ag)

	result := &TestResult{
		Passed:   true,
		Output:   "all tests passed",
		ExitCode: 0,
		RunAt:    time.Now(),
		Duration: 1.5,
		Command:  "go test ./...",
	}

	o.storeTestResult(&ag, result)

	var updated model.Agent
	db.First(&updated, "id = ?", agentID)
	if updated.Config == nil {
		t.Fatal("expected config to be set")
	}
	if updated.Config["last_test_result"] == nil {
		t.Error("expected last_test_result in config")
	}
}

// ---------------------------------------------------------------------------
// extractTestFiles test (with real git repo)
// ---------------------------------------------------------------------------

func TestExtractTestFiles(t *testing.T) {
	bareRepo := setupTestRepoWithMainBranch(t)
	featureName := "extract-tests"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	// Add test and non-test files.
	writeFile(t, featureDir, "foo.go", "package foo")
	writeFile(t, featureDir, "foo_test.go", "package foo")
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add files")

	db := testutil.NewTestDB(t)
	wt := NewHostWorktreeManager(bareRepo, "main")
	o := testOrchestrator(t, db, wt)

	testFiles := o.extractTestFiles(featureDir, "main")
	foundTest := false
	for _, f := range testFiles {
		if f == "foo_test.go" {
			foundTest = true
		}
	}
	if !foundTest {
		t.Errorf("expected foo_test.go in extracted test files, got %v", testFiles)
	}
}

// ---------------------------------------------------------------------------
// New / constructor test
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	db := testutil.NewTestDB(t)
	events := make(chan Event, 100)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	projectID := uuid.New()

	o := New(db, "/tmp/test.db", nil, wt, nil, nil, projectID, "drem-test", events,
		time.Second, 30*time.Minute, 75, 90, nil, "")
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	if o.contextFixerPct != defaultContextFixerPct {
		t.Errorf("expected default fixer pct %d, got %d", defaultContextFixerPct, o.contextFixerPct)
	}

	// With custom fixer pct.
	o2 := New(db, "/tmp/test.db", nil, wt, nil, nil, projectID, "drem-test", events,
		time.Second, 30*time.Minute, 75, 90, nil, "", 95)
	if o2.contextFixerPct != 95 {
		t.Errorf("expected fixer pct 95, got %d", o2.contextFixerPct)
	}
}

// ---------------------------------------------------------------------------
// IntegrationWorktreePath test
// ---------------------------------------------------------------------------

func TestIntegrationWorktreePath_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	result := o.IntegrationWorktreePath(uuid.New())
	if result != "" {
		t.Errorf("expected empty for nonexistent task, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// runCommandWithTimeout tests
// ---------------------------------------------------------------------------

func TestRunCommandWithTimeout_Success(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	dir := t.TempDir()
	result := o.runCommandWithTimeout(dir, "echo ok", 5*time.Second)
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Output == "" {
		t.Error("expected non-empty output")
	}
}

func TestRunCommandWithTimeout_Failure(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	dir := t.TempDir()
	result := o.runCommandWithTimeout(dir, "exit 42", 5*time.Second)
	if result.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", result.ExitCode)
	}
}

func TestRunCommandWithTimeout_Timeout(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	dir := t.TempDir()
	result := o.runCommandWithTimeout(dir, "sleep 30", 100*time.Millisecond)
	if result.ExitCode != -1 {
		t.Errorf("expected exit code -1 (timeout), got %d", result.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// runTestSuite tests
// ---------------------------------------------------------------------------

func TestRunTestSuite_NoGoProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	dir := t.TempDir()
	// No go.mod — go test will fail.
	passed, output := o.runTestSuite(dir)
	if passed {
		t.Error("expected tests to fail in empty directory")
	}
	_ = output
}

// ---------------------------------------------------------------------------
// parsePlan tests
// ---------------------------------------------------------------------------

func TestParsePlan_ValidPlan(t *testing.T) {
	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":           "Do A",
				"description":     "First task",
				"agent_type":      "coder",
				"estimated_files": []any{"a.go"},
			},
		},
	}

	result, err := parsePlan(plan)
	if err != nil {
		t.Fatalf("parsePlan error: %v", err)
	}
	if len(result.Subtasks) != 1 {
		t.Errorf("expected 1 subtask, got %d", len(result.Subtasks))
	}
	if result.Subtasks[0].Title != "Do A" {
		t.Errorf("expected title 'Do A', got %q", result.Subtasks[0].Title)
	}
}

func TestParsePlan_NilPlan(t *testing.T) {
	_, err := parsePlan(nil)
	if err == nil {
		t.Error("expected error for nil plan")
	}
}

func TestParsePlan_NoSubtasksKey(t *testing.T) {
	plan := model.JSONField{"other": "data"}
	_, err := parsePlan(plan)
	if err == nil {
		t.Error("expected error for missing subtasks key")
	}
}

func TestParsePlan_WithTDDExceptions(t *testing.T) {
	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":       "Do A",
				"description": "First task",
				"agent_type":  "coder",
			},
		},
		"tdd_exceptions": []any{
			map[string]any{
				"subtask_index": float64(0),
				"reason":        "config only",
			},
		},
	}

	result, err := parsePlan(plan)
	if err != nil {
		t.Fatalf("parsePlan error: %v", err)
	}
	if len(result.TDDExceptions) != 1 {
		t.Errorf("expected 1 exception, got %d", len(result.TDDExceptions))
	}
}

// ---------------------------------------------------------------------------
// DependenciesMet with missing task (not in DB)
// ---------------------------------------------------------------------------

func TestDependenciesMet_MissingTask(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Dependency ID that doesn't exist in DB.
	met, err := DependenciesMet(db, model.JSONArray{uuid.New().String()})
	if err != nil {
		t.Fatalf("DependenciesMet error: %v", err)
	}
	if met {
		t.Error("expected not met for missing dependency")
	}
}

// ---------------------------------------------------------------------------
// topologicalSortByDegree coverage
// ---------------------------------------------------------------------------

func TestBuildSchedule_EmptySubtasks(t *testing.T) {
	schedule := BuildSchedule(nil)
	if len(schedule.Groups) != 0 {
		t.Errorf("expected 0 groups for nil subtasks, got %d", len(schedule.Groups))
	}
}

func TestBuildSchedule_SingleSubtask(t *testing.T) {
	db := testutil.NewTestDB(t)

	subtasks := []model.Task{
		{
			ID:          uuid.New(),
			Title:       "single",
			Description: "only subtask",
			Status:      model.StatusBacklog,
		},
	}
	for i := range subtasks {
		db.Create(&subtasks[i])
	}

	schedule := BuildSchedule(subtasks)
	if len(schedule.Groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(schedule.Groups))
	}
}

func TestBuildSchedule_WithOverlap(t *testing.T) {
	// Create subtasks with file overlap that should be in different groups.
	sub1 := model.Task{
		ID:          uuid.New(),
		Title:       "sub1",
		Description: "first",
		Status:      model.StatusBacklog,
		Context:     model.JSONField{"estimated_files": []any{"shared.go", "a.go"}},
	}
	sub2 := model.Task{
		ID:          uuid.New(),
		Title:       "sub2",
		Description: "second",
		Status:      model.StatusBacklog,
		Context:     model.JSONField{"estimated_files": []any{"shared.go", "b.go"}},
	}
	sub3 := model.Task{
		ID:          uuid.New(),
		Title:       "sub3",
		Description: "third",
		Status:      model.StatusBacklog,
		Context:     model.JSONField{"estimated_files": []any{"c.go"}},
	}

	schedule := BuildSchedule([]model.Task{sub1, sub2, sub3})
	// sub1 and sub2 overlap on shared.go -> different groups.
	// sub3 has no overlap -> can be in same group as either.
	if len(schedule.Groups) < 2 {
		t.Errorf("expected at least 2 groups due to file overlap, got %d", len(schedule.Groups))
	}
}

// ---------------------------------------------------------------------------
// processTestWriting with baseline test failure
// ---------------------------------------------------------------------------

func TestProcessTestWriting_BaselineTestsFailed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "tw-baseline-failed",
		Description: "parent with failed baseline",
		Status:      model.StatusTestWriting,
		Context:     model.JSONField{"baseline_tests_failed": true, "baseline_tests_checked": true},
	}
	db.Create(&parent)

	// Create a done test subtask.
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub-done-baseline",
		Description:  "done test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	})

	// Despite baseline failures, the completion check should still run
	// and transition since all test subtasks are done.
	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestReview {
		t.Errorf("expected test_review (all done despite baseline failure), got %s", updated.Status)
	}
}

func TestProcessTestWriting_RejectedSubtasksTreatedAsFailed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "tw-rejected",
		Description: "parent",
		Status:      model.StatusTestWriting,
	}
	db.Create(&parent)

	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-rejected",
		Description:  "rejected test subtask",
		Status:       model.StatusRejected,
		Phase:        "test",
	})

	err := o.processTestWriting(&parent)
	if err != nil {
		t.Fatalf("processTestWriting error: %v", err)
	}

	// Rejected is terminal and counts as failed.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected failed (rejected test subtask), got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// HandleTestReviewRejected — 3 rejections triggers pause
// ---------------------------------------------------------------------------

func TestHandleTestReviewRejected_ThirdRound_PausesTask(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parentID := uuid.New()
	task := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "review-reject-3",
		Description: "parent",
		Status:      model.StatusTestReview,
		Context: model.JSONField{
			"test_rejection_count": float64(2),
		},
	}
	db.Create(&task)

	err := o.HandleTestReviewRejected(parentID, "third rejection")
	if err != nil {
		t.Fatalf("HandleTestReviewRejected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusPaused {
		t.Errorf("expected paused after 3 rejections, got %s", updated.Status)
	}
	if updated.Context == nil {
		t.Fatal("expected context")
	}
	if v, ok := updated.Context["diagnostic_required"].(bool); !ok || !v {
		t.Error("expected diagnostic_required=true")
	}
}

// ---------------------------------------------------------------------------
// HandlePlanApproved with TDD plan (test phase subtasks)
// ---------------------------------------------------------------------------

func TestHandlePlanApproved_TDDPlan(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":           "Test auth",
				"description":     "Write tests",
				"agent_type":      "coder",
				"phase":           "test",
				"estimated_files": []any{"auth_test.go"},
				"tests_for":       []any{float64(1)},
			},
			map[string]any{
				"title":           "Impl auth",
				"description":     "Implement",
				"agent_type":      "coder",
				"phase":           "implementation",
				"estimated_files": []any{"auth.go"},
				"dependencies":    []any{float64(0)},
			},
		},
	}
	task := createLifecycleTask(t, db, o.projectID, "tdd-plan", model.StatusPlanReview, plan)

	if err := o.HandlePlanApproved(task.ID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	// TDD plan with test phase -> goes to test_writing, not in_progress.
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected test_writing for TDD plan, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// HandlePlanApproved with dependencies
// ---------------------------------------------------------------------------

func TestHandlePlanApproved_WithDependencies(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := model.JSONField{
		"subtasks": []any{
			map[string]any{
				"title":           "Setup DB",
				"description":     "Database setup",
				"agent_type":      "coder",
				"estimated_files": []any{"db.go"},
			},
			map[string]any{
				"title":           "Add API",
				"description":     "API endpoints",
				"agent_type":      "coder",
				"estimated_files": []any{"api.go"},
				"dependencies":    []any{float64(0)},
			},
			map[string]any{
				"title":           "Tests",
				"description":     "Add tests",
				"agent_type":      "coder",
				"estimated_files": []any{"api_test.go"},
				"dependencies":    []any{float64(0), float64(1)},
			},
		},
	}
	task := createLifecycleTask(t, db, o.projectID, "deps-plan", model.StatusPlanReview, plan)

	if err := o.HandlePlanApproved(task.ID); err != nil {
		t.Fatalf("HandlePlanApproved error: %v", err)
	}

	// Verify subtasks have correct dependencies.
	var subtasks []model.Task
	db.Where("parent_task_id = ?", task.ID).Order("priority desc").Find(&subtasks)
	if len(subtasks) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(subtasks))
	}

	// Third subtask should have dependency_ids set.
	if len(subtasks[2].DependencyIDs) == 0 {
		t.Error("expected third subtask to have dependency IDs")
	}
}

// ---------------------------------------------------------------------------
// processPlanning — no agent, no capacity (runner needed)
// ---------------------------------------------------------------------------

func TestProcessPlanning_NoCapacity(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// Create project.
	project := model.Project{ID: orch.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   orch.projectID,
		Title:       "planning-no-capacity",
		Description: "task needing agent",
		Status:      model.StatusPlanning,
	}
	db.Create(&task)

	// The runner was created with max 0, so CanSpawn() returns false.
	// processPlanning should do nothing when no capacity.
	err := orch.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	// Task should remain in planning.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected planning (no capacity), got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// handlePaused — multiple subtasks with and without agents
// ---------------------------------------------------------------------------

func TestHandlePaused_MixedSubtasks(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   orch.projectID,
		Title:       "paused-mixed",
		Description: "parent with mixed subtasks",
		Status:      model.StatusPaused,
	}
	db.Create(&parent)

	// Subtask with agent.
	agentID := uuid.New()
	db.Create(&model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "mixed-agent",
		Status:    model.AgentWorking,
	})
	sub1 := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "sub-with-agent",
		Description:     "has agent",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub1)

	// Subtask without agent.
	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "sub-no-agent",
		Description:  "no agent",
		Status:       model.StatusBacklog,
	}
	db.Create(&sub2)

	err := orch.handlePaused(&parent)
	if err != nil {
		t.Fatalf("handlePaused error: %v", err)
	}

	// Sub1's agent should be cleared.
	var updated1 model.Task
	db.First(&updated1, "id = ?", sub1.ID)
	if updated1.AssignedAgentID != nil {
		t.Error("expected sub1 AssignedAgentID to be nil")
	}

	// Sub2 should be unchanged.
	var updated2 model.Task
	db.First(&updated2, "id = ?", sub2.ID)
	if updated2.Status != model.StatusBacklog {
		t.Errorf("expected sub2 to remain backlog, got %s", updated2.Status)
	}
}

// ---------------------------------------------------------------------------
// HandleTestReviewApproved (happy path)
// ---------------------------------------------------------------------------

func TestHandleTestReviewApproved_TransitionsToInProgress(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "tr-approved-happy", model.StatusTestReview, nil)

	if err := o.HandleTestReviewApproved(task.ID); err != nil {
		t.Fatalf("error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected in_progress, got %s", updated.Status)
	}

	// Verify event recorded.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&events)
	found := false
	for _, e := range events {
		if e.OldValue == string(model.StatusTestReview) && e.NewValue == string(model.StatusInProgress) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected test_review -> in_progress event")
	}
}

// ---------------------------------------------------------------------------
// HandleTestPassed / HandleTestFailed not found tests
// ---------------------------------------------------------------------------

func TestHandleTestPassed_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)
	err := o.HandleTestPassed(uuid.New())
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestHandleTestFailed_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)
	err := o.HandleTestFailed(uuid.New())
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

// ---------------------------------------------------------------------------
// AddComment additional tests
// ---------------------------------------------------------------------------

func TestAddComment_TestingReadyStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "comment-testing-ready", model.StatusTestingReady, nil)

	err := o.AddComment(task.ID, "user", "Looks good")
	if err != nil {
		t.Fatalf("AddComment error: %v", err)
	}

	comments, err := o.GetComments(task.ID)
	if err != nil {
		t.Fatalf("GetComments error: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("expected 1 comment, got %d", len(comments))
	}
}

func TestAddComment_TestReviewStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "comment-test-review", model.StatusTestReview, nil)

	err := o.AddComment(task.ID, "user", "Review comment")
	if err != nil {
		t.Fatalf("AddComment error: %v", err)
	}
}

func TestAddComment_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)
	err := o.AddComment(uuid.New(), "user", "should fail")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestGetComments_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)
	comments, err := o.GetComments(uuid.New())
	if err != nil {
		t.Fatalf("GetComments error: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}

// ---------------------------------------------------------------------------
// CreateTask additional tests
// ---------------------------------------------------------------------------

func TestCreateTask_DefaultPriority(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task, err := o.CreateTask("zero-priority", "A task with 0 priority", 0)
	if err != nil {
		t.Fatalf("CreateTask error: %v", err)
	}
	if task.Priority != 0 {
		t.Errorf("expected priority 0, got %d", task.Priority)
	}

	var loaded model.Task
	db.First(&loaded, "id = ?", task.ID)
	if loaded.Title != "zero-priority" {
		t.Errorf("expected title 'zero-priority', got %q", loaded.Title)
	}
}

// ---------------------------------------------------------------------------
// SpawnReviewerSession — existing working reviewer returns session
// ---------------------------------------------------------------------------

func TestSpawnReviewerSession_ExistingReviewer(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "reviewer-existing"
	createFeatureWorktree(t, bareRepo, featureName)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "reviewer-existing-task",
		Description:    "task with existing reviewer",
		Status:         model.StatusPlanReview,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	// Create an existing working reviewer.
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentReviewer,
		Name:          "existing-reviewer",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		TmuxSession:   "test-tmux-session",
	}
	db.Create(&ag)

	session, err := orch.SpawnReviewerSession(taskID)
	if err != nil {
		t.Fatalf("SpawnReviewerSession error: %v", err)
	}
	if session != "test-tmux-session" {
		t.Errorf("expected existing tmux session, got %q", session)
	}
}

// ---------------------------------------------------------------------------
// SpawnFixerSession — worktree occupied
// ---------------------------------------------------------------------------

func TestSpawnFixerSession_WorktreeOccupied(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "fixer-occupied"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "fixer-occupied-task",
		Description:    "task with busy integration worktree",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	// Create a busy reviewer in the integration worktree.
	agentID := uuid.New()
	ag := model.Agent{
		ID:           agentID,
		ProjectID:    orch.projectID,
		AgentType:    model.AgentReviewer,
		Name:         "busy-reviewer",
		Status:       model.AgentWorking,
		WorktreePath: featureDir,
	}
	db.Create(&ag)

	_, err := orch.SpawnFixerSession(taskID)
	if err == nil {
		t.Fatal("expected error for occupied worktree")
	}
}

// ---------------------------------------------------------------------------
// checkFeatureCompletion with worktree but empty feature branch
// ---------------------------------------------------------------------------

func TestCheckFeatureCompletion_AllDoneButNoChanges(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "empty-changes"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "empty-changes-parent",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Create done subtasks.
	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub-empty",
		Description:  "done",
		Status:       model.StatusDone,
	})

	err := orch.checkFeatureCompletion(&parent)
	if err != nil {
		t.Fatalf("checkFeatureCompletion error: %v", err)
	}

	// All subtasks done but feature branch has no changes -> parent should fail.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected failed (no changes on feature branch), got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_AllDoneAlreadyMergedToDefault(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	mainDir := filepath.Join(bareRepo, "main")
	runGitCmd(t, bareRepo, "worktree", "add", mainDir, "main")
	baseSHA := runGitCmd(t, mainDir, "rev-parse", "HEAD")

	featureName := "already-merged-parent"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeFile(t, featureDir, "merged.txt", "merged work")
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "merged work")
	runGitCmd(t, mainDir, "merge", "feature/"+featureName, "--no-edit")

	parentID := uuid.New()
	parent := model.Task{
		ID:              parentID,
		ProjectID:       orch.projectID,
		Title:           "already-merged-parent",
		Description:     "parent",
		Status:          model.StatusInProgress,
		WorktreeBranch:  "feature/" + featureName,
		WorktreeBaseSHA: baseSHA,
	}
	db.Create(&parent)

	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub-merged",
		Description:  "done",
		Status:       model.StatusDone,
	})

	if err := orch.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Fatalf("expected testing_ready so already-merged work still receives delivery verification, got %s", updated.Status)
	}

	var failedEvents int64
	db.Model(&model.TaskEvent{}).Where(
		"task_id = ? AND new_value = ?", parentID, string(model.StatusFailed),
	).Count(&failedEvents)
	if failedEvents != 0 {
		t.Fatalf("expected no transient failed event, got %d", failedEvents)
	}
	var testingReadyEvents int64
	db.Model(&model.TaskEvent{}).Where(
		"task_id = ? AND new_value = ?", parentID, string(model.StatusTestingReady),
	).Count(&testingReadyEvents)
	if testingReadyEvents != 1 {
		t.Fatalf("expected one testing_ready event, got %d", testingReadyEvents)
	}
}

// ---------------------------------------------------------------------------
// checkFeatureCompletion with real changes on feature branch
// ---------------------------------------------------------------------------

func TestCheckFeatureCompletion_AllDoneWithChanges(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "real-changes"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	// Add a real change to the feature branch.
	writeFile(t, featureDir, "real.go", "package main")
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "real change")

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "real-changes-parent",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	db.Create(&model.Task{
		ID:           uuid.New(),
		ProjectID:    orch.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub-real",
		Description:  "done",
		Status:       model.StatusDone,
	})

	err := orch.checkFeatureCompletion(&parent)
	if err != nil {
		t.Fatalf("checkFeatureCompletion error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected testing_ready, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// SpawnReviewerSession — testing_ready status (feature review mode)
// ---------------------------------------------------------------------------

func TestSpawnReviewerSession_TestingReadyExistingReviewer(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "reviewer-testing"
	createFeatureWorktree(t, bareRepo, featureName)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "reviewer-testing-task",
		Description:    "testing_ready task",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	// Existing reviewer.
	ag := model.Agent{
		ID:            uuid.New(),
		ProjectID:     orch.projectID,
		AgentType:     model.AgentReviewer,
		Name:          "testing-reviewer",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		TmuxSession:   "review-session",
	}
	db.Create(&ag)

	session, err := orch.SpawnReviewerSession(taskID)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if session != "review-session" {
		t.Errorf("expected 'review-session', got %q", session)
	}
}

// ---------------------------------------------------------------------------
// HandleTestReviewRejected_NotFound
// ---------------------------------------------------------------------------

func TestHandleTestReviewRejected_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)
	err := o.HandleTestReviewRejected(uuid.New(), "feedback")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

// ---------------------------------------------------------------------------
// processPlanning — agent missing from DB (cleared assignment)
// ---------------------------------------------------------------------------

func TestProcessPlanning_AgentMissing_MaxRetries(t *testing.T) {
	o, db := setupLifecycleTest(t)

	missingAgentID := uuid.New()
	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "planning-missing-max",
		Description:     "task at max retries with missing agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &missingAgentID,
		Context:         model.JSONField{"retry_count": float64(MaxPlannerRetries - 1)},
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected failed after max retries with missing agent, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// onAgentEmptyWork — retry with context already set
// ---------------------------------------------------------------------------

func TestOnAgentEmptyWork_SecondRetry(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	taskID := uuid.New()
	agentID := uuid.New()

	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "empty-work-2nd",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
	}
	o.db.Create(ag)

	task := &model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "empty-work-2nd",
		Description:     "agent did nothing again",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(0)},
	}
	o.db.Create(task)

	err := o.onAgentEmptyWork(ag, task, "no output")
	if err != nil {
		t.Fatalf("onAgentEmptyWork error: %v", err)
	}

	// Should still retry (retry_count 0 -> 1, below MaxEmptyWorkRetries=2).
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.AssignedAgentID != nil {
		t.Error("expected agent cleared for retry")
	}
	if v, ok := updatedTask.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1, got %v", updatedTask.Context["retry_count"])
	}
}

// ---------------------------------------------------------------------------
// onAgentFailed — planner retry path
// ---------------------------------------------------------------------------

func TestOnAgentFailed_PlannerRetry(t *testing.T) {
	o, _ := agentResultOrchestrator(t, "/tmp/fake")

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent-planner-retry",
		Description: "parent",
		Status:      model.StatusInProgress,
	}
	o.db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := &model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "planner-retry-agent",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
		WorktreePath:  "/tmp/fake-planner",
	}
	o.db.Create(ag)

	task := &model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "planner-retry-task",
		Description:     "will retry",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(0)},
	}
	o.db.Create(task)

	err := o.onAgentFailed(ag, task)
	if err != nil {
		t.Fatalf("onAgentFailed error: %v", err)
	}

	// Planner should stay in planning for retry.
	var updatedTask model.Task
	o.db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusPlanning {
		t.Errorf("expected planning (retry), got %s", updatedTask.Status)
	}
	if updatedTask.AssignedAgentID != nil {
		t.Error("expected agent cleared for retry")
	}
	if v, ok := updatedTask.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1, got %v", updatedTask.Context["retry_count"])
	}
}

// ---------------------------------------------------------------------------
// SpawnFixerSession — with context diagnosis data
// ---------------------------------------------------------------------------

func TestSpawnFixerSession_WithDiagnosisContext(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "fixer-diagnosis"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "fixer-diagnosis-task",
		Description:    "task with diagnosis",
		Status:         model.StatusFailed,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"failure_diagnosis": "compilation error",
			"failure_reason":    "build failed",
			"suggested_fix":     "fix import",
			"affected_files":    []any{"main.go"},
		},
	}
	db.Create(&task)

	// Should fail because runner can't spawn (max 0), but exercises
	// the context extraction logic.
	_, err := orch.SpawnFixerSession(taskID)
	if err == nil {
		// Could succeed if runner can spawn, but likely fails.
		t.Log("SpawnFixerSession succeeded (unexpected but ok)")
	}

	_ = featureDir
}

// ---------------------------------------------------------------------------
// reconcileStaleSubtasks — multiple done subtasks with no changes
// ---------------------------------------------------------------------------

func TestReconcileStaleSubtasks_MultipleDone(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stale-multi"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stale-multi-parent",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Three done subtasks — all should be reset.
	for i := 0; i < 3; i++ {
		db.Create(&model.Task{
			ID:           uuid.New(),
			ProjectID:    orch.projectID,
			ParentTaskID: &parentID,
			Title:        "stale-multi-sub",
			Description:  "done subtask",
			Status:       model.StatusDone,
		})
	}

	fixes, err := orch.reconcileStaleSubtasks()
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if fixes != 3 {
		t.Errorf("expected 3 fixes, got %d", fixes)
	}
}

// ---------------------------------------------------------------------------
// Helper for test file creation
// ---------------------------------------------------------------------------

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
}
