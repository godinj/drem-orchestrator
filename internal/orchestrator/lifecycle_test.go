package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// setupLifecycleTest creates an orchestrator with mock dependencies suitable
// for testing state transitions without real tmux/git. The returned
// Orchestrator has a nil runner, so tests that call methods requiring agent
// spawning should skip with testing.Short or use a skip guard.
func setupLifecycleTest(t *testing.T) (*Orchestrator, *gorm.DB) {
	t.Helper()
	db := lifecycleTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	// Create the project record in DB so FK constraints are satisfied.
	project := model.Project{
		ID:           projectID,
		Name:         "lifecycle-test-" + projectID.String()[:8],
		BareRepoPath: "/tmp/fake-bare-repo",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  &worktree.Manager{BareRepoPath: "/tmp/fake-bare-repo", DefaultBranch: "main"},
		events:    events,
		logger:    slog.Default().With("component", "lifecycle-test"),
	}
	return o, db
}

// lifecycleTestDB creates an isolated in-memory SQLite database for each test.
// Unlike testDB which uses cache=shared, this avoids unique constraint
// collisions between parallel tests.
func lifecycleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a unique file name for each test to ensure isolation.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=private", uuid.New().String())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
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

// createLifecycleTask creates a task with the given status and optional plan.
func createLifecycleTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus, plan model.JSONField) model.Task {
	t.Helper()
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: "lifecycle test task: " + title,
		Status:      status,
		Plan:        plan,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

// makePlan creates a valid plan JSONField with the given number of subtasks.
func makePlan(count int) model.JSONField {
	subtasks := make([]map[string]any, count)
	for i := 0; i < count; i++ {
		subtasks[i] = map[string]any{
			"title":           "subtask-" + string(rune('A'+i)),
			"description":     "Description for subtask " + string(rune('A'+i)),
			"agent_type":      "coder",
			"estimated_files": []string{"file" + string(rune('A'+i)) + ".go"},
		}
	}
	return model.JSONField{"subtasks": subtasks}
}

// ---------------------------------------------------------------------------
// HandlePlanRejected tests
// ---------------------------------------------------------------------------

func TestHandlePlanRejected(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(2)
	task := createLifecycleTask(t, db, o.projectID, "reject-plan-test", model.StatusPlanReview, plan)

	if err := o.HandlePlanRejected(task.ID); err != nil {
		t.Fatalf("HandlePlanRejected: unexpected error: %v", err)
	}

	// Reload task from DB.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Verify status transitioned back to planning.
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status %q, got %q", model.StatusPlanning, updated.Status)
	}

	// Verify the plan was cleared.
	if updated.Plan != nil {
		t.Errorf("expected plan to be nil after rejection, got %v", updated.Plan)
	}

	// Verify assigned agent was cleared.
	if updated.AssignedAgentID != nil {
		t.Errorf("expected assigned agent to be nil, got %v", updated.AssignedAgentID)
	}

	// Verify a status_change event was recorded.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&events)
	if len(events) == 0 {
		t.Error("expected at least one event to be recorded")
	}
	found := false
	for _, e := range events {
		if e.OldValue == string(model.StatusPlanReview) && e.NewValue == string(model.StatusPlanning) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected event with plan_review -> planning transition")
	}
}

func TestHandlePlanRejected_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "wrong-status-reject", model.StatusInProgress, nil)

	err := o.HandlePlanRejected(task.ID)
	if err == nil {
		t.Fatal("HandlePlanRejected: expected error for wrong status, got nil")
	}

	// Verify the task status was not changed.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status to remain %q, got %q", model.StatusInProgress, updated.Status)
	}
}

func TestHandlePlanRejected_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	err := o.HandlePlanRejected(uuid.New())
	if err == nil {
		t.Fatal("HandlePlanRejected: expected error for nonexistent task, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeletePlanStep tests
// ---------------------------------------------------------------------------

func TestDeletePlanStep(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(3)
	task := createLifecycleTask(t, db, o.projectID, "delete-step-test", model.StatusPlanReview, plan)

	// Delete the middle step (index 1).
	if err := o.DeletePlanStep(task.ID, 1); err != nil {
		t.Fatalf("DeletePlanStep: unexpected error: %v", err)
	}

	// Reload task.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Verify the plan now has 2 steps.
	subtasksRaw, ok := updated.Plan["subtasks"]
	if !ok {
		t.Fatal("expected subtasks key in plan")
	}
	items, ok := subtasksRaw.([]any)
	if !ok {
		// After DB round-trip, the type might differ — re-marshal and check.
		b, _ := json.Marshal(subtasksRaw)
		var arr []any
		if err := json.Unmarshal(b, &arr); err != nil {
			t.Fatalf("unmarshal subtasks after round-trip: %v", err)
		}
		items = arr
	}
	if len(items) != 2 {
		t.Errorf("expected 2 steps after deletion, got %d", len(items))
	}

	// Verify the remaining steps have the right titles (first and third).
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := m["title"].(string)
		switch i {
		case 0:
			if title != "subtask-A" {
				t.Errorf("step 0: expected title %q, got %q", "subtask-A", title)
			}
		case 1:
			if title != "subtask-C" {
				t.Errorf("step 1: expected title %q, got %q", "subtask-C", title)
			}
		}
	}
}

func TestDeletePlanStep_OutOfBounds(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(2)
	task := createLifecycleTask(t, db, o.projectID, "delete-oob", model.StatusPlanReview, plan)

	err := o.DeletePlanStep(task.ID, 5)
	if err == nil {
		t.Fatal("DeletePlanStep: expected error for out-of-bounds index, got nil")
	}

	// Also test negative index.
	err = o.DeletePlanStep(task.ID, -1)
	if err == nil {
		t.Fatal("DeletePlanStep: expected error for negative index, got nil")
	}
}

func TestDeletePlanStep_NoPlan(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "delete-no-plan", model.StatusPlanReview, nil)

	err := o.DeletePlanStep(task.ID, 0)
	if err == nil {
		t.Fatal("DeletePlanStep: expected error for nil plan, got nil")
	}
}

func TestDeletePlanStep_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(3)
	task := createLifecycleTask(t, db, o.projectID, "delete-wrong-status", model.StatusInProgress, plan)

	err := o.DeletePlanStep(task.ID, 0)
	if err == nil {
		t.Fatal("DeletePlanStep: expected error for wrong status, got nil")
	}
}

// ---------------------------------------------------------------------------
// HandlePlanApproved tests
// ---------------------------------------------------------------------------

func TestHandlePlanApproved(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(3)
	task := createLifecycleTask(t, db, o.projectID, "approve-plan-test", model.StatusPlanReview, plan)

	if err := o.HandlePlanApproved(task.ID); err != nil {
		t.Fatalf("HandlePlanApproved: unexpected error: %v", err)
	}

	// Reload task.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Verify status transitioned to in_progress.
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status %q, got %q", model.StatusInProgress, updated.Status)
	}

	// Verify subtasks were created.
	var subtasks []model.Task
	db.Where("parent_task_id = ?", task.ID).Find(&subtasks)
	if len(subtasks) != 3 {
		t.Errorf("expected 3 subtasks, got %d", len(subtasks))
	}

	// Verify each subtask is in backlog.
	for _, sub := range subtasks {
		if sub.Status != model.StatusBacklog {
			t.Errorf("subtask %s: expected status %q, got %q", sub.ID, model.StatusBacklog, sub.Status)
		}
		if sub.ProjectID != o.projectID {
			t.Errorf("subtask %s: expected project_id %s, got %s", sub.ID, o.projectID, sub.ProjectID)
		}
	}

	// Verify assigned agent was cleared.
	if updated.AssignedAgentID != nil {
		t.Errorf("expected assigned agent to be nil after plan approval, got %v", updated.AssignedAgentID)
	}

	// Verify a status_change event was recorded.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&events)
	found := false
	for _, e := range events {
		if e.OldValue == string(model.StatusPlanReview) && e.NewValue == string(model.StatusInProgress) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected event with plan_review -> in_progress transition")
	}
}

func TestHandlePlanApproved_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "approve-wrong-status", model.StatusBacklog, nil)

	err := o.HandlePlanApproved(task.ID)
	if err == nil {
		t.Fatal("HandlePlanApproved: expected error for wrong status, got nil")
	}
}

func TestHandlePlanApproved_NoPlan(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "approve-no-plan", model.StatusPlanReview, nil)

	err := o.HandlePlanApproved(task.ID)
	if err == nil {
		t.Fatal("HandlePlanApproved: expected error when task has no plan, got nil")
	}

	// Verify status did not change.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanReview {
		t.Errorf("expected status to remain %q, got %q", model.StatusPlanReview, updated.Status)
	}
}

// ---------------------------------------------------------------------------
// HandleTestPassed / HandleTestFailed tests
// ---------------------------------------------------------------------------

func TestHandleTestPassed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-passed", model.StatusTestingReady, nil)

	if err := o.HandleTestPassed(task.ID); err != nil {
		t.Fatalf("HandleTestPassed: unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusMerging {
		t.Errorf("expected status %q, got %q", model.StatusMerging, updated.Status)
	}
}

func TestHandleTestPassed_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-passed-wrong", model.StatusInProgress, nil)

	err := o.HandleTestPassed(task.ID)
	if err == nil {
		t.Fatal("HandleTestPassed: expected error for wrong status, got nil")
	}
}

func TestHandleTestFailed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-failed", model.StatusTestingReady, makePlan(1))

	if err := o.HandleTestFailed(task.ID); err != nil {
		t.Fatalf("HandleTestFailed: unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status %q, got %q", model.StatusInProgress, updated.Status)
	}
}

func TestHandleTestFailed_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-failed-wrong", model.StatusBacklog, nil)

	err := o.HandleTestFailed(task.ID)
	if err == nil {
		t.Fatal("HandleTestFailed: expected error for wrong status, got nil")
	}
}

// ---------------------------------------------------------------------------
// PauseTask / ResumeTask lifecycle tests
// ---------------------------------------------------------------------------

func TestPauseResumeLifecycle(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "pause-resume", model.StatusInProgress, nil)

	// Pause the task.
	if err := o.PauseTask(task.ID); err != nil {
		t.Fatalf("PauseTask: unexpected error: %v", err)
	}

	var paused model.Task
	db.First(&paused, "id = ?", task.ID)
	if paused.Status != model.StatusPaused {
		t.Errorf("expected status %q after pause, got %q", model.StatusPaused, paused.Status)
	}

	// Verify the previous status was stored in context.
	if paused.Context == nil {
		t.Fatal("expected context to be set after pause")
	}
	pausedFrom, ok := paused.Context["paused_from"].(string)
	if !ok || pausedFrom != string(model.StatusInProgress) {
		t.Errorf("expected paused_from %q, got %q", model.StatusInProgress, pausedFrom)
	}

	// Resume the task.
	if err := o.ResumeTask(task.ID); err != nil {
		t.Fatalf("ResumeTask: unexpected error: %v", err)
	}

	var resumed model.Task
	db.First(&resumed, "id = ?", task.ID)
	if resumed.Status != model.StatusInProgress {
		t.Errorf("expected status %q after resume, got %q", model.StatusInProgress, resumed.Status)
	}
}

func TestPauseTask_FromPlanning(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "pause-planning", model.StatusPlanning, nil)

	if err := o.PauseTask(task.ID); err != nil {
		t.Fatalf("PauseTask: unexpected error: %v", err)
	}

	var paused model.Task
	db.First(&paused, "id = ?", task.ID)
	if paused.Status != model.StatusPaused {
		t.Errorf("expected status %q, got %q", model.StatusPaused, paused.Status)
	}

	// Resume should go back to planning.
	if err := o.ResumeTask(task.ID); err != nil {
		t.Fatalf("ResumeTask: unexpected error: %v", err)
	}

	var resumed model.Task
	db.First(&resumed, "id = ?", task.ID)
	if resumed.Status != model.StatusPlanning {
		t.Errorf("expected status %q after resume, got %q", model.StatusPlanning, resumed.Status)
	}
}

func TestPauseTask_FromBacklog(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "pause-backlog", model.StatusBacklog, nil)

	if err := o.PauseTask(task.ID); err != nil {
		t.Fatalf("PauseTask: unexpected error: %v", err)
	}

	var paused model.Task
	db.First(&paused, "id = ?", task.ID)
	if paused.Status != model.StatusPaused {
		t.Errorf("expected status %q, got %q", model.StatusPaused, paused.Status)
	}

	// Resume should go back to backlog.
	if err := o.ResumeTask(task.ID); err != nil {
		t.Fatalf("ResumeTask: unexpected error: %v", err)
	}

	var resumed model.Task
	db.First(&resumed, "id = ?", task.ID)
	if resumed.Status != model.StatusBacklog {
		t.Errorf("expected status %q after resume, got %q", model.StatusBacklog, resumed.Status)
	}
}

func TestPauseTask_InvalidStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// DONE is terminal — cannot be paused.
	task := createLifecycleTask(t, db, o.projectID, "pause-done", model.StatusDone, nil)

	err := o.PauseTask(task.ID)
	if err == nil {
		t.Fatal("PauseTask: expected error for done status, got nil")
	}
}

func TestResumeTask_NotPaused(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "resume-not-paused", model.StatusInProgress, nil)

	err := o.ResumeTask(task.ID)
	if err == nil {
		t.Fatal("ResumeTask: expected error for non-paused task, got nil")
	}
}

func TestResumeTask_FallbackToBacklog(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// Create a task that was paused from plan_review. plan_review is not a valid
	// target from PAUSED (PAUSED can go to backlog, planning, in_progress).
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "resume-fallback",
		Description: "test task",
		Status:      model.StatusPaused,
		Context:     model.JSONField{"paused_from": string(model.StatusPlanReview)},
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Resume should fall back to backlog since plan_review is not reachable from PAUSED.
	if err := o.ResumeTask(task.ID); err != nil {
		t.Fatalf("ResumeTask: unexpected error: %v", err)
	}

	var resumed model.Task
	db.First(&resumed, "id = ?", task.ID)
	if resumed.Status != model.StatusBacklog {
		t.Errorf("expected status %q (fallback), got %q", model.StatusBacklog, resumed.Status)
	}
}

// ---------------------------------------------------------------------------
// processBacklog tests
// ---------------------------------------------------------------------------

func TestProcessBacklog_TransitionsToPlanning(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "backlog-to-planning", model.StatusBacklog, nil)

	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: unexpected error: %v", err)
	}

	// Verify task transitioned to planning.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status %q, got %q", model.StatusPlanning, updated.Status)
	}

	// Verify event was recorded.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&events)
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}

func TestProcessBacklog_ReplanDetachesOldSubtasks(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "replan-test", model.StatusBacklog, nil)
	task.PlanFeedback = "Please improve the plan"
	db.Save(&task)

	// Create old subtasks attached to this parent.
	parentID := task.ID
	for _, title := range []string{"old-sub-1", "old-sub-2"} {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parentID,
			Title:        title,
			Description:  "old subtask",
			Status:       model.StatusDone,
		}
		db.Create(&sub)
	}

	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: unexpected error: %v", err)
	}

	// Verify old subtasks are detached (parent_task_id = nil).
	var subs []model.Task
	db.Where("parent_task_id = ?", parentID).Find(&subs)
	if len(subs) != 0 {
		t.Errorf("expected 0 attached subtasks after replan, got %d", len(subs))
	}

	// Verify the subtasks still exist (just detached).
	var allSubs []model.Task
	db.Where("title LIKE 'old-sub-%'").Find(&allSubs)
	if len(allSubs) != 2 {
		t.Errorf("expected 2 detached subtasks to still exist, got %d", len(allSubs))
	}
}

// ---------------------------------------------------------------------------
// SpawnReviewerSession tests
// ---------------------------------------------------------------------------

func TestSpawnReviewerSession_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "reviewer-wrong-status", model.StatusBacklog, nil)

	_, err := o.SpawnReviewerSession(task.ID)
	if err == nil {
		t.Fatal("SpawnReviewerSession: expected error for wrong status, got nil")
	}
}

func TestSpawnReviewerSession_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	_, err := o.SpawnReviewerSession(uuid.New())
	if err == nil {
		t.Fatal("SpawnReviewerSession: expected error for nonexistent task, got nil")
	}
}

// ---------------------------------------------------------------------------
// SpawnFixerSession tests
// ---------------------------------------------------------------------------

func TestSpawnFixerSession_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "fixer-wrong-status", model.StatusBacklog, nil)

	_, err := o.SpawnFixerSession(task.ID)
	if err == nil {
		t.Fatal("SpawnFixerSession: expected error for wrong status, got nil")
	}
}

func TestSpawnFixerSession_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	_, err := o.SpawnFixerSession(uuid.New())
	if err == nil {
		t.Fatal("SpawnFixerSession: expected error for nonexistent task, got nil")
	}
}

func TestSpawnFixerSession_ValidStatuses(t *testing.T) {
	// Verify that in_progress, failed, and testing_ready are accepted
	// (the method will fail later when resolving worktree, but status
	// validation should pass).
	validStatuses := []model.TaskStatus{
		model.StatusInProgress,
		model.StatusFailed,
		model.StatusTestingReady,
	}

	for _, status := range validStatuses {
		t.Run(string(status), func(t *testing.T) {
			o, db := setupLifecycleTest(t)
			task := createLifecycleTask(t, db, o.projectID, "fixer-"+string(status), status, nil)

			_, err := o.SpawnFixerSession(task.ID)
			// Should fail on worktree resolution, not status validation.
			if err == nil {
				t.Fatal("expected error (missing worktree), got nil")
			}
			// The error should NOT be about status.
			if contains(err.Error(), "must be in") {
				t.Errorf("unexpected status validation error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle transition tests
// ---------------------------------------------------------------------------

func TestTaskLifecycle_BacklogToPlanningToReview(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// Create a task in BACKLOG.
	task := createLifecycleTask(t, db, o.projectID, "full-lifecycle", model.StatusBacklog, nil)

	// Step 1: BACKLOG -> PLANNING via processBacklog.
	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	var afterPlanning model.Task
	db.First(&afterPlanning, "id = ?", task.ID)
	if afterPlanning.Status != model.StatusPlanning {
		t.Fatalf("expected planning, got %s", afterPlanning.Status)
	}

	// Step 2: Simulate plan arrival (set plan directly, then call processPlanning
	// which checks for plan and transitions to PLAN_REVIEW). We skip the runner-
	// dependent spawn path.
	plan := makePlan(2)
	afterPlanning.Plan = plan
	db.Save(&afterPlanning)

	if err := o.processPlanning(&afterPlanning); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var afterReview model.Task
	db.First(&afterReview, "id = ?", task.ID)
	if afterReview.Status != model.StatusPlanReview {
		t.Fatalf("expected plan_review, got %s", afterReview.Status)
	}
}

func TestTaskLifecycle_PlanApprovalToInProgress(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(2)
	task := createLifecycleTask(t, db, o.projectID, "approve-lifecycle", model.StatusPlanReview, plan)

	// Approve the plan.
	if err := o.HandlePlanApproved(task.ID); err != nil {
		t.Fatalf("HandlePlanApproved: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected in_progress, got %s", updated.Status)
	}

	// Verify subtasks were created with proper hierarchy.
	var subtasks []model.Task
	db.Where("parent_task_id = ?", task.ID).Order("priority desc").Find(&subtasks)
	if len(subtasks) != 2 {
		t.Fatalf("expected 2 subtasks, got %d", len(subtasks))
	}

	// Higher priority should be for earlier items.
	if subtasks[0].Priority <= subtasks[1].Priority {
		t.Errorf("expected first subtask to have higher priority, got %d <= %d",
			subtasks[0].Priority, subtasks[1].Priority)
	}
}

func TestTaskLifecycle_TestPassedToMerging(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-to-merge", model.StatusTestingReady, nil)

	if err := o.HandleTestPassed(task.ID); err != nil {
		t.Fatalf("HandleTestPassed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusMerging {
		t.Errorf("expected merging, got %s", updated.Status)
	}
}

func TestTaskLifecycle_TestFailedBackToPlanning(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-fail-replan", model.StatusTestingReady, makePlan(1))

	if err := o.HandleTestFailed(task.ID); err != nil {
		t.Fatalf("HandleTestFailed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected in_progress, got %s", updated.Status)
	}
}

func TestTaskLifecycle_PlanRejectReplanApprove(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// Start with a plan in review.
	plan := makePlan(2)
	task := createLifecycleTask(t, db, o.projectID, "reject-replan", model.StatusPlanReview, plan)

	// Reject the plan.
	if err := o.HandlePlanRejected(task.ID); err != nil {
		t.Fatalf("HandlePlanRejected: %v", err)
	}

	var afterReject model.Task
	db.First(&afterReject, "id = ?", task.ID)
	if afterReject.Status != model.StatusPlanning {
		t.Fatalf("expected planning after reject, got %s", afterReject.Status)
	}

	// Simulate a new plan arriving.
	newPlan := makePlan(3)
	afterReject.Plan = newPlan
	db.Save(&afterReject)

	// processPlanning should detect the plan and transition to plan_review.
	if err := o.processPlanning(&afterReject); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var afterReview model.Task
	db.First(&afterReview, "id = ?", task.ID)
	if afterReview.Status != model.StatusPlanReview {
		t.Fatalf("expected plan_review, got %s", afterReview.Status)
	}

	// Approve the new plan.
	if err := o.HandlePlanApproved(task.ID); err != nil {
		t.Fatalf("HandlePlanApproved: %v", err)
	}

	var afterApprove model.Task
	db.First(&afterApprove, "id = ?", task.ID)
	if afterApprove.Status != model.StatusInProgress {
		t.Errorf("expected in_progress, got %s", afterApprove.Status)
	}

	// Should have 3 subtasks from the new plan.
	var subtasks []model.Task
	db.Where("parent_task_id = ?", task.ID).Find(&subtasks)
	if len(subtasks) != 3 {
		t.Errorf("expected 3 subtasks from new plan, got %d", len(subtasks))
	}
}

func TestTaskLifecycle_PauseWhileInProgress_ResumeAndComplete(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "pause-in-progress", model.StatusInProgress, nil)

	// Pause.
	if err := o.PauseTask(task.ID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	var paused model.Task
	db.First(&paused, "id = ?", task.ID)
	if paused.Status != model.StatusPaused {
		t.Fatalf("expected paused, got %s", paused.Status)
	}

	// Resume.
	if err := o.ResumeTask(task.ID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	var resumed model.Task
	db.First(&resumed, "id = ?", task.ID)
	if resumed.Status != model.StatusInProgress {
		t.Fatalf("expected in_progress, got %s", resumed.Status)
	}

	// Verify event trail: should have pause and resume events.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Order("created_at asc").Find(&events)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	// First event: in_progress -> paused.
	if events[0].OldValue != string(model.StatusInProgress) || events[0].NewValue != string(model.StatusPaused) {
		t.Errorf("event 0: expected in_progress->paused, got %s->%s", events[0].OldValue, events[0].NewValue)
	}

	// Second event: paused -> in_progress.
	if events[1].OldValue != string(model.StatusPaused) || events[1].NewValue != string(model.StatusInProgress) {
		t.Errorf("event 1: expected paused->in_progress, got %s->%s", events[1].OldValue, events[1].NewValue)
	}
}

// ---------------------------------------------------------------------------
// CreateTask / RetryTask lifecycle tests
// ---------------------------------------------------------------------------

func TestCreateTask(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task, err := o.CreateTask("new-task", "A brand new task", 5)
	if err != nil {
		t.Fatalf("CreateTask: unexpected error: %v", err)
	}

	if task.Status != model.StatusBacklog {
		t.Errorf("expected status %q, got %q", model.StatusBacklog, task.Status)
	}
	if task.Title != "new-task" {
		t.Errorf("expected title %q, got %q", "new-task", task.Title)
	}
	if task.Priority != 5 {
		t.Errorf("expected priority 5, got %d", task.Priority)
	}
	if task.ProjectID != o.projectID {
		t.Errorf("expected project_id %s, got %s", o.projectID, task.ProjectID)
	}

	// Verify persisted in DB.
	var loaded model.Task
	if err := db.First(&loaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("task not found in DB: %v", err)
	}
}

func TestRetryTask(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "retry-task", model.StatusFailed, nil)
	// Add retry context that should be cleared.
	task.Context = model.JSONField{
		"retry_count": float64(2),
		"last_error":  "something went wrong",
	}
	db.Save(&task)

	if err := o.RetryTask(task.ID); err != nil {
		t.Fatalf("RetryTask: unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected status %q, got %q", model.StatusBacklog, updated.Status)
	}

	// Verify retry context was cleared.
	if updated.Context != nil {
		if _, hasRetry := updated.Context["retry_count"]; hasRetry {
			t.Error("expected retry_count to be cleared")
		}
		if _, hasError := updated.Context["last_error"]; hasError {
			t.Error("expected last_error to be cleared")
		}
	}
}

func TestRetryTask_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "retry-wrong-status", model.StatusInProgress, nil)

	err := o.RetryTask(task.ID)
	if err == nil {
		t.Fatal("RetryTask: expected error for non-failed task, got nil")
	}
}

// ---------------------------------------------------------------------------
// AddComment / DeleteComment / GetComments tests
// ---------------------------------------------------------------------------

func TestAddDeleteComments(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// AddComment is only allowed in human-gate statuses (plan_review, testing_ready).
	task := createLifecycleTask(t, db, o.projectID, "comment-test", model.StatusPlanReview, nil)

	// Add a comment.
	if err := o.AddComment(task.ID, "user", "This plan looks good"); err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	// Retrieve comments.
	comments, err := o.GetComments(task.ID)
	if err != nil {
		t.Fatalf("GetComments: unexpected error: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Body != "This plan looks good" {
		t.Errorf("expected comment body %q, got %q", "This plan looks good", comments[0].Body)
	}

	// Delete the comment.
	if err := o.DeleteComment(comments[0].ID); err != nil {
		t.Fatalf("DeleteComment: unexpected error: %v", err)
	}

	// Verify it's gone.
	comments, _ = o.GetComments(task.ID)
	if len(comments) != 0 {
		t.Errorf("expected 0 comments after deletion, got %d", len(comments))
	}
}

func TestAddComment_WrongStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// Comments only allowed in human-gate statuses.
	task := createLifecycleTask(t, db, o.projectID, "comment-wrong", model.StatusInProgress, nil)

	err := o.AddComment(task.ID, "user", "should fail")
	if err == nil {
		t.Fatal("AddComment: expected error for non-human-gate status, got nil")
	}
}

// ---------------------------------------------------------------------------
// failTask tests
// ---------------------------------------------------------------------------

func TestFailTask(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "fail-task", model.StatusInProgress, nil)

	if err := o.failTask(&task, "test failure reason"); err != nil {
		t.Fatalf("failTask: unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected status %q, got %q", model.StatusFailed, updated.Status)
	}

	// Verify the failure reason is in context.
	if updated.Context == nil {
		t.Fatal("expected context to be set")
	}
	reason, _ := updated.Context["failure_reason"].(string)
	if reason != "test failure reason" {
		t.Errorf("expected failure_reason %q, got %q", "test failure reason", reason)
	}
}

func TestFailTask_AlreadyFailed(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// FAILED -> FAILED is not a valid transition.
	task := createLifecycleTask(t, db, o.projectID, "fail-again", model.StatusFailed, nil)

	err := o.failTask(&task, "second failure")
	if err == nil {
		t.Fatal("failTask: expected error for already-failed task, got nil")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
