package orchestrator

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupSchedulingTest creates an Orchestrator with a test DB, project, and
// event channel suitable for scheduling-focused tests. The runner, merger,
// and worktree manager are nil/minimal, so only DB-level behavior is exercised.
func setupSchedulingTest(t *testing.T) (*Orchestrator, *gorm.DB, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "scheduling-test",
		BareRepoPath:  "/tmp/test.git",
		DefaultBranch: "main",
	}
	db.Create(&project)

	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  &FakeWorktreeManager{BarePath: "/tmp/test.git", Default: "main"},
		events:    events,
		logger:    slog.Default().With("component", "scheduling-test"),
	}
	return orch, db, projectID
}

// createTask is a shorthand for creating a task in the DB and returning it.
func createTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus, parentID *uuid.UUID) model.Task {
	t.Helper()
	task := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: parentID,
		Title:        title,
		Description:  "test task: " + title,
		Status:       status,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

// ---------------------------------------------------------------------------
// processBacklog tests
// ---------------------------------------------------------------------------

// TestProcessBacklog_TransitionsToPlanning is in lifecycle_test.go

func TestProcessBacklog_DetachForReplanning(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	// Create a task with PlanFeedback set (replanning scenario).
	task := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		Title:        "replan-task",
		Description:  "task being replanned",
		Status:       model.StatusBacklog,
		PlanFeedback: "The plan needs more detail on error handling",
	}
	db.Create(&task)

	// Create old subtasks linked to this parent.
	parentID := task.ID
	sub1 := createTask(t, db, projectID, "old-sub-1", model.StatusDone, &parentID)
	sub2 := createTask(t, db, projectID, "old-sub-2", model.StatusFailed, &parentID)

	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	// Verify old subtasks were detached (ParentTaskID set to nil).
	var reloadedSub1 model.Task
	db.First(&reloadedSub1, "id = ?", sub1.ID)
	if reloadedSub1.ParentTaskID != nil {
		t.Errorf("expected sub1 ParentTaskID to be nil (detached), got %v", reloadedSub1.ParentTaskID)
	}

	var reloadedSub2 model.Task
	db.First(&reloadedSub2, "id = ?", sub2.ID)
	if reloadedSub2.ParentTaskID != nil {
		t.Errorf("expected sub2 ParentTaskID to be nil (detached), got %v", reloadedSub2.ParentTaskID)
	}

	// Verify the task itself transitioned to planning.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning after replan, got %s", updated.Status)
	}
}

func TestProcessBacklog_NoPlanFeedback_NoDetach(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	task := createTask(t, db, projectID, "first-time-task", model.StatusBacklog, nil)

	// Create subtasks (should not be detached since PlanFeedback is empty).
	parentID := task.ID
	sub := createTask(t, db, projectID, "sub-1", model.StatusBacklog, &parentID)

	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	// Verify subtask still linked.
	var reloaded model.Task
	db.First(&reloaded, "id = ?", sub.ID)
	if reloaded.ParentTaskID == nil || *reloaded.ParentTaskID != parentID {
		t.Errorf("expected subtask to remain linked to parent, got ParentTaskID=%v", reloaded.ParentTaskID)
	}
}

// ---------------------------------------------------------------------------
// processPlanning tests
// ---------------------------------------------------------------------------

func TestProcessPlanning_SkipsWhenAgentAssigned(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	agentID := uuid.New()
	agent := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentPlanner,
		Name:      "planner-1",
		Status:    model.AgentWorking,
	}
	db.Create(&agent)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		Title:           "planning-task-with-agent",
		Description:     "task already has agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	// processPlanning should return nil (agent is still working, no-op).
	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	// Task should remain in PLANNING.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning, got %s", updated.Status)
	}
}

func TestProcessPlanning_TransitionsToPlanReviewWhenPlanExists(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "planned-task",
		Description: "task with existing plan",
		Status:      model.StatusPlanning,
		Plan:        model.JSONField{"subtasks": []any{}},
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	// Task should transition to PLAN_REVIEW.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanReview {
		t.Errorf("expected status plan_review, got %s", updated.Status)
	}
}

func TestProcessPlanning_ClearsDeadAgent(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	agentID := uuid.New()
	agent := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentPlanner,
		Name:      "dead-planner",
		Status:    model.AgentDead,
	}
	db.Create(&agent)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		Title:           "planning-dead-agent",
		Description:     "task whose planner died",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	// AssignedAgentID should be cleared.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID != nil {
		t.Errorf("expected AssignedAgentID to be nil after dead agent cleanup, got %v", updated.AssignedAgentID)
	}

	// Task should remain in PLANNING (not failed, since retries < max).
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning (retry), got %s", updated.Status)
	}
}

func TestProcessPlanning_FailsAfterMaxRetries(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	agentID := uuid.New()
	agent := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentPlanner,
		Name:      "dead-planner-max",
		Status:    model.AgentDead,
	}
	db.Create(&agent)

	// Pre-set retry count to MaxPlannerRetries - 1 so next failure triggers max.
	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		Title:           "planning-max-retries",
		Description:     "task exceeding max retries",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(MaxPlannerRetries - 1)},
	}
	db.Create(&task)

	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected status failed after max retries, got %s", updated.Status)
	}
}

func TestProcessPlanning_ReplanningWithFeedback(t *testing.T) {
	// A task in PLANNING with PlanFeedback set (replanning after rejection).
	// Verify the feedback is preserved through the planning cycle.
	o, db, projectID := setupSchedulingTest(t)

	agentID := uuid.New()
	agent := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentPlanner,
		Name:      "working-planner",
		Status:    model.AgentWorking,
	}
	db.Create(&agent)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		Title:           "replan-with-feedback",
		Description:     "task replanned after rejection",
		Status:          model.StatusPlanning,
		PlanFeedback:    "Need better error handling in subtask 2",
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	// Agent is still working, so processPlanning should no-op.
	err := o.processPlanning(&task)
	if err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	// PlanFeedback should be preserved.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.PlanFeedback != "Need better error handling in subtask 2" {
		t.Errorf("expected PlanFeedback to be preserved, got %q", updated.PlanFeedback)
	}
}

// ---------------------------------------------------------------------------
// checkFeatureCompletion tests
// ---------------------------------------------------------------------------

func TestCheckFeatureCompletion_AllSubtasksDone(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-all-done",
		Description: "parent with all done subtasks",
		Status:      model.StatusInProgress,
		// No WorktreeBranch — skip the file change check.
	}
	db.Create(&parent)

	for _, title := range []string{"sub-1", "sub-2", "sub-3"} {
		createTask(t, db, projectID, title, model.StatusDone, &parentID)
	}

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updated.Status)
	}

	// Verify event was created.
	var events []model.TaskEvent
	db.Where("task_id = ? AND new_value = ?", parentID, "testing_ready").Find(&events)
	if len(events) == 0 {
		t.Error("expected a testing_ready event")
	}
}

func TestCheckFeatureCompletion_CancelledSubtasksDoNotBlockDoneWork(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-with-superseded-subtasks",
		Description: "parent with done work and cancelled superseded subtasks",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	createTask(t, db, projectID, "done-sub-1", model.StatusDone, &parentID)
	createTask(t, db, projectID, "done-sub-2", model.StatusDone, &parentID)
	cancelled := createTask(t, db, projectID, "cancelled-superseded-sub", model.StatusCancelled, &parentID)
	cancelled.Context = model.JSONField{cancellationKindContextKey: "superseded"}
	require.NoError(t, db.Save(&cancelled).Error)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_DependencyFailureCancellationBlocksDelivery(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID: parentID, ProjectID: projectID, Title: "parent-with-cancelled-required-work",
		Description: "a required child was cancelled when its dependency failed",
		Status:      model.StatusInProgress,
	}
	require.NoError(t, db.Create(&parent).Error)
	createTask(t, db, projectID, "repaired-prerequisite", model.StatusDone, &parentID)
	cancelled := createTask(t, db, projectID, "cancelled-required-integration", model.StatusCancelled, &parentID)
	cancelled.Context = model.JSONField{cancellationKindContextKey: "dependency_failure"}
	require.NoError(t, db.Save(&cancelled).Error)

	require.NoError(t, o.checkFeatureCompletion(&parent))

	var updated model.Task
	require.NoError(t, db.First(&updated, "id = ?", parentID).Error)
	require.Equal(t, model.StatusFailed, updated.Status,
		"a repaired prerequisite must not make a dependency-cancelled required child look complete")
	require.NotEqual(t, model.StatusTestingReady, updated.Status)
}

func TestCheckFeatureCompletion_SomeSubtasksPending(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-partial",
		Description: "parent with some pending subtasks",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	createTask(t, db, projectID, "done-sub-1", model.StatusDone, &parentID)
	createTask(t, db, projectID, "done-sub-2", model.StatusDone, &parentID)
	createTask(t, db, projectID, "running-sub", model.StatusInProgress, &parentID)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_NoSubtasks_StaysInProgress(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-no-subs",
		Description: "parent with no subtasks",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_MixedPhases(t *testing.T) {
	// Parent with subtasks that might have different "phases" (via context
	// agent_type). All must be done for parent to transition.
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-mixed-phases",
		Description: "parent with mixed phase subtasks",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// "Test" subtask done.
	testSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "write tests",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Context:      model.JSONField{"agent_type": "coder"},
	}
	db.Create(&testSub)

	// "Impl" subtask still in progress.
	implSub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "implement feature",
		Description:  "implementation subtask",
		Status:       model.StatusInProgress,
		Context:      model.JSONField{"agent_type": "coder"},
	}
	db.Create(&implSub)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress (impl subtask pending), got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_AllTerminalWithFailures(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-some-failed",
		Description: "parent with failed subtasks",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	createTask(t, db, projectID, "done-sub", model.StatusDone, &parentID)
	createTask(t, db, projectID, "failed-sub", model.StatusFailed, &parentID)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected parent to be failed when all terminal and some failed, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// handlePaused tests
// ---------------------------------------------------------------------------

func TestHandlePaused_NoAgent(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       projectID,
		Title:           "paused-no-agent",
		Description:     "paused task with no agent",
		Status:          model.StatusPaused,
		AssignedAgentID: nil,
	}
	db.Create(&task)

	// handlePaused calls runner.StopAgent which would panic with nil runner
	// if AssignedAgentID is set. With nil agent, it should be a no-op.
	err := o.handlePaused(&task)
	if err != nil {
		t.Fatalf("handlePaused: %v", err)
	}

	// Task should remain unchanged.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.AssignedAgentID != nil {
		t.Errorf("expected AssignedAgentID to remain nil, got %v", updated.AssignedAgentID)
	}
}

func TestHandlePaused_NoSubtaskAgents(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "paused-parent",
		Description: "paused parent with subtasks without agents",
		Status:      model.StatusPaused,
	}
	db.Create(&parent)

	// Subtasks with no assigned agents.
	createTask(t, db, projectID, "sub-1", model.StatusBacklog, &parentID)
	createTask(t, db, projectID, "sub-2", model.StatusInProgress, &parentID)

	err := o.handlePaused(&parent)
	if err != nil {
		t.Fatalf("handlePaused: %v", err)
	}

	// The query filters on assigned_agent_id IS NOT NULL, so these should
	// not cause any issues. Verify subtasks are untouched.
	var subs []model.Task
	db.Where("parent_task_id = ?", parentID).Find(&subs)
	for _, sub := range subs {
		if sub.AssignedAgentID != nil {
			t.Errorf("subtask %s: expected no assigned agent, got %v", sub.Title, sub.AssignedAgentID)
		}
	}
}

// ---------------------------------------------------------------------------
// findCurrentGroup tests
// ---------------------------------------------------------------------------

func TestFindCurrentGroup_AllGroupsComplete(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-all-groups-done",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// Create subtasks, all DONE.
	sub1 := createTask(t, db, projectID, "sub-1", model.StatusDone, &parentID)
	sub2 := createTask(t, db, projectID, "sub-2", model.StatusDone, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group != nil {
		t.Errorf("expected nil (all groups complete), got group with order %d", group.Order)
	}
}

func TestFindCurrentGroup_FirstGroupPending(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-first-pending",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub1 := createTask(t, db, projectID, "sub-1", model.StatusInProgress, &parentID)
	sub2 := createTask(t, db, projectID, "sub-2", model.StatusBacklog, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group == nil {
		t.Fatal("expected a non-nil group")
	}
	if group.Order != 0 {
		t.Errorf("expected group 0, got group %d", group.Order)
	}
}

func TestFindCurrentGroup_SecondGroupActive(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-second-active",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub1 := createTask(t, db, projectID, "sub-1", model.StatusDone, &parentID)
	sub2 := createTask(t, db, projectID, "sub-2", model.StatusInProgress, &parentID)
	sub3 := createTask(t, db, projectID, "sub-3", model.StatusBacklog, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2.ID}},
			{Order: 2, TaskIDs: []uuid.UUID{sub3.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group == nil {
		t.Fatal("expected a non-nil group")
	}
	if group.Order != 1 {
		t.Errorf("expected group 1, got group %d", group.Order)
	}
}

func TestFindCurrentGroup_MissingSubtaskTreatedAsTerminal(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-missing-sub",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// Group 0 has a missing subtask ID (simulating deleted/replanned task).
	missingID := uuid.New() // Not in DB.
	sub2 := createTask(t, db, projectID, "sub-2", model.StatusBacklog, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{missingID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group == nil {
		t.Fatal("expected a non-nil group (group 1 still pending)")
	}
	// Group 0 should be treated as complete (missing tasks are terminal).
	if group.Order != 1 {
		t.Errorf("expected group 1 (after skipping missing), got group %d", group.Order)
	}
}

func TestFindCurrentGroup_EmptySchedule(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parent := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "parent-empty-schedule",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	schedule := Schedule{Groups: nil}
	group := o.findCurrentGroup(&parent, schedule)
	if group != nil {
		t.Errorf("expected nil for empty schedule, got group %d", group.Order)
	}
}

func TestFindCurrentGroup_FailedSubtasksTreatedAsTerminal(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-failed-sub",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub1 := createTask(t, db, projectID, "sub-1-failed", model.StatusFailed, &parentID)
	sub2 := createTask(t, db, projectID, "sub-2-backlog", model.StatusBacklog, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group == nil {
		t.Fatal("expected a non-nil group")
	}
	// Failed subtask in group 0 is terminal, so group 0 is complete.
	if group.Order != 1 {
		t.Errorf("expected group 1, got group %d", group.Order)
	}
}

func TestFindCurrentGroup_CancelledAndRejectedSubtasksTreatedAsTerminal(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-cancelled-rejected-subtasks",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub1 := createTask(t, db, projectID, "sub-1-cancelled", model.StatusCancelled, &parentID)
	sub2 := createTask(t, db, projectID, "sub-2-rejected", model.StatusRejected, &parentID)
	sub3 := createTask(t, db, projectID, "sub-3-backlog", model.StatusBacklog, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID, sub2.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub3.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group == nil {
		t.Fatal("expected a non-nil group")
	}
	if group.Order != 1 {
		t.Errorf("expected group 1, got group %d", group.Order)
	}
}

func TestFindCurrentGroup_MultipleSubtasksInGroup(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-multi-subs",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub1 := createTask(t, db, projectID, "sub-1-done", model.StatusDone, &parentID)
	sub2 := createTask(t, db, projectID, "sub-2-running", model.StatusInProgress, &parentID)
	sub3 := createTask(t, db, projectID, "sub-3-backlog", model.StatusBacklog, &parentID)

	// All three subtasks in a single group.
	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID, sub2.ID, sub3.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group == nil {
		t.Fatal("expected a non-nil group")
	}
	if group.Order != 0 {
		t.Errorf("expected group 0, got group %d", group.Order)
	}
}

func TestFindCurrentGroup_AllSamePhase(t *testing.T) {
	// All subtasks in one phase, single group.
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "parent-same-phase",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	sub1 := createTask(t, db, projectID, "impl-1", model.StatusDone, &parentID)
	sub2 := createTask(t, db, projectID, "impl-2", model.StatusDone, &parentID)
	sub3 := createTask(t, db, projectID, "impl-3", model.StatusDone, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID, sub2.ID, sub3.ID}},
		},
	}

	group := o.findCurrentGroup(&parent, schedule)
	if group != nil {
		t.Errorf("expected nil (all done), got group %d", group.Order)
	}
}

// ---------------------------------------------------------------------------
// scheduleSubtasks (wave scheduling) tests — DB-level only
// ---------------------------------------------------------------------------

func TestScheduleSubtasks_WaveScheduleFiltersToCurrentGroup(t *testing.T) {
	// This test verifies that when a schedule is stored in parent.Context,
	// scheduleSubtasks correctly parses it and findCurrentGroup selects the
	// right group. Since we can't call scheduleSubtasks without a runner
	// (it calls runner.CanSpawn), we test the schedule parsing and
	// findCurrentGroup integration.
	o, db, projectID := setupSchedulingTest(t)

	parentID := uuid.New()

	sub1 := createTask(t, db, projectID, "wave-sub-1", model.StatusDone, &parentID)
	sub2 := createTask(t, db, projectID, "wave-sub-2", model.StatusBacklog, &parentID)

	schedule := Schedule{
		Groups: []SubtaskGroup{
			{Order: 0, TaskIDs: []uuid.UUID{sub1.ID}},
			{Order: 1, TaskIDs: []uuid.UUID{sub2.ID}},
		},
	}

	// Simulate what HandlePlanApproved does: marshal schedule and store in context.
	scheduleJSON, err := json.Marshal(schedule)
	if err != nil {
		t.Fatalf("marshal schedule: %v", err)
	}
	var scheduleField any
	json.Unmarshal(scheduleJSON, &scheduleField)

	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "wave-parent",
		Description:    "parent with wave schedule",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/wave-test",
		Context:        model.JSONField{"schedule": scheduleField},
	}
	db.Create(&parent)

	// Parse the schedule from context the same way scheduleSubtasks does.
	scheduleRaw := parent.Context["schedule"]
	reJSON, err := json.Marshal(scheduleRaw)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var parsed Schedule
	if err := json.Unmarshal(reJSON, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	group := o.findCurrentGroup(&parent, parsed)
	if group == nil {
		t.Fatal("expected a group, got nil")
	}
	// Group 0 (sub1) is done, so current group should be group 1 (sub2).
	if group.Order != 1 {
		t.Errorf("expected group 1, got group %d", group.Order)
	}
	if len(group.TaskIDs) != 1 || group.TaskIDs[0] != sub2.ID {
		t.Errorf("expected group to contain sub2 ID %s, got %v", sub2.ID, group.TaskIDs)
	}
}

// ---------------------------------------------------------------------------
// doTick integration (lightweight) — nil-safety
// ---------------------------------------------------------------------------

// TestDoTick_NilRunnerPanicsExpected documents that doTick requires a non-nil
// runner because it calls runner.DrainCompletions and runner.CleanupStaleAgents
// in every tick. We verify the backlog processing works before the runner call.
func TestDoTick_BacklogProcessingWorks(t *testing.T) {
	o, db, projectID := setupSchedulingTest(t)

	// Create a backlog task with no dependencies.
	task := createTask(t, db, projectID, "tick-backlog", model.StatusBacklog, nil)

	// We can't call doTick directly because it calls runner.DrainCompletions
	// which would panic on nil runner. Instead, test the individual
	// processBacklog path which doTick invokes.
	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog (doTick path): %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected task to transition to planning, got %s", updated.Status)
	}
}

func TestDoTick_ProcessesMultipleStatuses(t *testing.T) {
	// Verify each phase handler works independently without crashing.
	// Since doTick requires a runner, we call each handler directly.
	o, db, projectID := setupSchedulingTest(t)

	// 1. BACKLOG -> PLANNING
	backlog := createTask(t, db, projectID, "tick-backlog", model.StatusBacklog, nil)
	if err := o.processBacklog(&backlog); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	// 2. PLANNING with plan -> PLAN_REVIEW
	planning := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "tick-planning",
		Description: "test",
		Status:      model.StatusPlanning,
		Plan:        model.JSONField{"subtasks": []any{}},
	}
	db.Create(&planning)
	if err := o.processPlanning(&planning); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	// 3. IN_PROGRESS parent with all done subtasks -> checkFeatureCompletion
	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   projectID,
		Title:       "tick-in-progress",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)
	createTask(t, db, projectID, "tick-sub-done", model.StatusDone, &parentID)
	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	// 4. PAUSED with no agent -> no-op
	paused := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "tick-paused",
		Description: "test",
		Status:      model.StatusPaused,
	}
	db.Create(&paused)
	if err := o.handlePaused(&paused); err != nil {
		t.Fatalf("handlePaused: %v", err)
	}

	// Verify final states.
	var updatedBacklog model.Task
	db.First(&updatedBacklog, "id = ?", backlog.ID)
	if updatedBacklog.Status != model.StatusPlanning {
		t.Errorf("backlog: expected planning, got %s", updatedBacklog.Status)
	}

	var updatedPlanning model.Task
	db.First(&updatedPlanning, "id = ?", planning.ID)
	if updatedPlanning.Status != model.StatusPlanReview {
		t.Errorf("planning: expected plan_review, got %s", updatedPlanning.Status)
	}

	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Status != model.StatusTestingReady {
		t.Errorf("in_progress: expected testing_ready, got %s", updatedParent.Status)
	}
}

// TestIncrementRetryCount, TestTaskFeatureName, TestTaskFeatureName_LongTitle
// are in agent_result_test.go
