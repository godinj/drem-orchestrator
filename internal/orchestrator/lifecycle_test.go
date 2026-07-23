package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/clarification"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupLifecycleTest creates an orchestrator with mock dependencies suitable
// for testing state transitions without real tmux/git. The returned
// Orchestrator has a nil runner, so tests that call methods requiring agent
// spawning should skip with testing.Short or use a skip guard.
func setupLifecycleTest(t *testing.T) (*Orchestrator, *gorm.DB) {
	t.Helper()
	db := testutil.NewTestDB(t)
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
		worktree:  &FakeWorktreeManager{BarePath: "/tmp/fake-bare-repo", Default: "main"},
		events:    events,
		logger:    slog.Default().With("component", "lifecycle-test"),
	}
	return o, db
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

func TestHandleClarificationAnswerByAcceptPlanPreservesPlan(t *testing.T) {
	o, db := setupLifecycleTest(t)
	plan := model.JSONField{"subtasks": []any{map[string]any{"title": "bounded implementation"}}}
	session, err := clarification.Evaluate("{}", []clarification.Assumption{{
		Decision: "Use the requested helper", Alternatives: []string{"Add a class"}, WhyChosen: "bounded scope",
	}}, "")
	require.NoError(t, err)
	task := createLifecycleTask(t, db, o.projectID, "accept-plan", model.StatusNeedsClarification, plan)
	task.Context = model.JSONField{
		"clarification_session":          session.SessionData,
		"clarification_current_question": session.Questions[0],
	}
	require.NoError(t, db.Save(&task).Error)

	require.NoError(t, o.HandleClarificationAnswerBy(task.ID, "/accept-plan", "codex:test"))

	var updated model.Task
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPlanReview, updated.Status)
	require.Equal(t, plan, updated.Plan)
	require.Equal(t, "accepted_plan_assumptions", updated.Context["clarification_resolution"])
	require.NotContains(t, updated.Context, "clarification_current_question")
}

func authorizeTestDelivery(t *testing.T, o *Orchestrator, taskID uuid.UUID) (*model.DeliveryArtifact, *model.VerificationRecord, *model.IntegrationAuthorization) {
	t.Helper()
	bareRepo := setupTestRepoWithMainBranch(t)
	featureName := "test-" + taskID.String()[:8]
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeFile(t, featureDir, "delivery.txt", "lifecycle delivery")
	runGitCmd(t, featureDir, "add", "delivery.txt")
	runGitCmd(t, featureDir, "commit", "-m", "lifecycle delivery")
	branch := "feature/" + featureName
	commitSHA := runGitCmd(t, featureDir, "rev-parse", "HEAD")
	baseSHA := runGitCmd(t, featureDir, "rev-parse", "main")
	o.worktree = NewHostManager(bareRepo, "main").AsInterface()
	var deliveryTask model.Task
	if err := o.db.First(&deliveryTask, "id = ?", taskID).Error; err != nil {
		t.Fatalf("load delivery task: %v", err)
	}
	deliveryTask.WorktreeBranch = branch
	deliveryTask.WorktreeBaseSHA = baseSHA
	if err := o.db.Save(&deliveryTask).Error; err != nil {
		t.Fatalf("save delivery task refs: %v", err)
	}
	now := time.Now()
	evidence := []CommandEvidence{{Command: "go test ./...", Passed: true, StartedAt: now, FinishedAt: now}}
	artifact, err := o.FreezeDeliveryArtifact(taskID, ArtifactSnapshot{
		Branch: branch, CommitSHA: commitSHA, BaseBranch: "main", BaseSHA: baseSHA,
		GateWorkspaceID: "test-gate-workspace", EnvironmentFingerprint: "test-environment",
		PreliminaryEvidence: evidence, Actor: "orchestrator", Source: "test",
	})
	if err != nil {
		t.Fatalf("freeze delivery: %v", err)
	}
	var verificationReady model.Task
	if err := o.db.First(&verificationReady, "id = ?", taskID).Error; err != nil {
		t.Fatalf("load verification-ready task: %v", err)
	}
	verification, err := o.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: taskID, ObservedStateVersion: verificationReady.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "test:verifier", Source: "test", EnvironmentFingerprint: "test",
		CommandEvidence: evidence, Result: model.VerificationPassed, IdempotencyKey: "verify-" + taskID.String(),
	})
	if err != nil {
		t.Fatalf("verify delivery: %v", err)
	}
	var integrationReady model.Task
	if err := o.db.First(&integrationReady, "id = ?", taskID).Error; err != nil {
		t.Fatalf("load integration-ready task: %v", err)
	}
	auth, err := o.AuthorizeIntegration(IntegrateDeliveryRequest{
		TaskID: taskID, ObservedStateVersion: integrationReady.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		VerificationRecordID: verification.ID, Actor: "test:integrator", Source: "test",
		IdempotencyKey: "integrate-" + taskID.String(),
	})
	if err != nil {
		t.Fatalf("authorize integration: %v", err)
	}
	return artifact, verification, auth
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
	task.Context = model.JSONField{
		"automated_review_state_version": float64(task.StateVersion),
		"automated_review_status":        "attention_required",
		"automated_review_detail":        "revise",
		"review":                         map[string]any{"recommendation": "revise"},
	}
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("seed review context: %v", err)
	}

	if err := o.HandlePlanRejectedBy(task.ID, "test", "Use the existing unit-test harness"); err != nil {
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
	if updated.PlanFeedback != "Use the existing unit-test harness" {
		t.Errorf("expected plan feedback to be persisted, got %q", updated.PlanFeedback)
	}

	// Verify assigned agent was cleared.
	if updated.AssignedAgentID != nil {
		t.Errorf("expected assigned agent to be nil, got %v", updated.AssignedAgentID)
	}
	for _, key := range []string{"automated_review_state_version", "automated_review_status", "automated_review_detail", "review"} {
		if _, ok := updated.Context[key]; ok {
			t.Errorf("expected stale review key %q to be cleared", key)
		}
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
// DeleteTask tests
// ---------------------------------------------------------------------------

func TestDeleteTask_Subtask(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parent := createLifecycleTask(t, db, o.projectID, "parent", model.StatusInProgress, nil)
	parentID := parent.ID
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "child-subtask",
		Description:  "a subtask",
		Status:       model.StatusBacklog,
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create subtask: %v", err)
	}

	// DeleteTask on a subtask should delete just the subtask.
	if err := o.DeleteTask(sub.ID); err != nil {
		t.Fatalf("DeleteTask(subtask): unexpected error: %v", err)
	}

	// Subtask should be gone.
	var count int64
	db.Model(&model.Task{}).Where("id = ?", sub.ID).Count(&count)
	if count != 0 {
		t.Error("expected subtask to be deleted")
	}

	// Parent should still exist.
	db.Model(&model.Task{}).Where("id = ?", parent.ID).Count(&count)
	if count != 1 {
		t.Error("expected parent to still exist")
	}
}

func TestDeleteTask_ParentCascade(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parent := createLifecycleTask(t, db, o.projectID, "cascade-parent", model.StatusInProgress, nil)
	parentID := parent.ID

	// Create several subtasks with comments and events.
	var subtaskIDs []uuid.UUID
	for i := 0; i < 3; i++ {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parentID,
			Title:        fmt.Sprintf("sub-%d", i),
			Description:  "subtask",
			Status:       model.StatusBacklog,
		}
		if err := db.Create(&sub).Error; err != nil {
			t.Fatalf("create subtask %d: %v", i, err)
		}
		subtaskIDs = append(subtaskIDs, sub.ID)

		// Add a comment to each subtask.
		comment := model.TaskComment{
			ID:     uuid.New(),
			TaskID: sub.ID,
			Author: "user",
			Body:   fmt.Sprintf("comment on sub-%d", i),
		}
		db.Create(&comment)
	}

	// Add a comment to the parent too.
	parentComment := model.TaskComment{
		ID:     uuid.New(),
		TaskID: parent.ID,
		Author: "user",
		Body:   "parent comment",
	}
	db.Create(&parentComment)

	// Delete the parent task — should cascade.
	if err := o.DeleteTask(parent.ID); err != nil {
		t.Fatalf("DeleteTask(parent): unexpected error: %v", err)
	}

	// Parent should be gone.
	var count int64
	db.Model(&model.Task{}).Where("id = ?", parent.ID).Count(&count)
	if count != 0 {
		t.Error("expected parent task to be deleted")
	}

	// All subtasks should be gone.
	for _, subID := range subtaskIDs {
		db.Model(&model.Task{}).Where("id = ?", subID).Count(&count)
		if count != 0 {
			t.Errorf("expected subtask %s to be deleted", subID)
		}
	}

	// All comments should be gone (subtask comments + parent comment).
	db.Model(&model.TaskComment{}).Where("task_id IN ?", append(subtaskIDs, parent.ID)).Count(&count)
	if count != 0 {
		t.Errorf("expected all comments to be deleted, got %d remaining", count)
	}
}

func TestDeleteTask_NotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)

	err := o.DeleteTask(uuid.New())
	if err == nil {
		t.Fatal("DeleteTask: expected error for nonexistent task, got nil")
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

func TestHandlePlanApproved_ReusesDoneSubtasksOnRetry(t *testing.T) {
	o, db := setupLifecycleTest(t)

	plan := makePlan(2)
	parent := createLifecycleTask(t, db, o.projectID, "retry-approve-plan-test", model.StatusPlanReview, plan)
	existingIDs := make(map[uuid.UUID]bool)
	for _, title := range []string{"subtask-A", "subtask-B"} {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parent.ID,
			Title:        title,
			Description:  "already completed before parent retry",
			Status:       model.StatusDone,
		}
		if err := db.Create(&sub).Error; err != nil {
			t.Fatalf("create existing subtask %q: %v", title, err)
		}
		existingIDs[sub.ID] = true
	}
	staleDuplicate := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parent.ID,
		Title:        "subtask-A",
		Description:  "duplicate from a failed previous approval",
		Status:       model.StatusFailed,
	}
	if err := db.Create(&staleDuplicate).Error; err != nil {
		t.Fatalf("create stale duplicate subtask: %v", err)
	}

	if err := o.HandlePlanApproved(parent.ID); err != nil {
		t.Fatalf("HandlePlanApproved: unexpected error: %v", err)
	}

	var subtasks []model.Task
	if err := db.Where("parent_task_id = ?", parent.ID).Find(&subtasks).Error; err != nil {
		t.Fatalf("load subtasks: %v", err)
	}
	if len(subtasks) != 2 {
		t.Fatalf("expected existing 2 subtasks to be reused without duplicates, got %d", len(subtasks))
	}
	for _, sub := range subtasks {
		if !existingIDs[sub.ID] {
			t.Fatalf("unexpected new subtask created: %s", sub.ID)
		}
		if sub.Status != model.StatusDone {
			t.Fatalf("expected reused subtask %s to remain done, got %s", sub.ID, sub.Status)
		}
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

	if err := o.HandleTestPassed(task.ID); err == nil {
		t.Fatal("HandleTestPassed: expected deprecated transition error")
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected status %q, got %q", model.StatusTestingReady, updated.Status)
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

	if err := o.HandleTestFailed(task.ID); err == nil {
		t.Fatal("HandleTestFailed: expected deprecated transition error")
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected status %q, got %q", model.StatusTestingReady, updated.Status)
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

	authorizeTestDelivery(t, o, task.ID)

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusMerging {
		t.Errorf("expected merging, got %s", updated.Status)
	}
}

func TestTaskLifecycle_HappyPathToDone(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "happy-path", model.StatusBacklog, nil)
	task.WorktreeBranch = "feature/happy-path"
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task worktree branch: %v", err)
	}

	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	var afterPlanning model.Task
	if err := db.First(&afterPlanning, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload after backlog: %v", err)
	}
	if afterPlanning.Status != model.StatusPlanning {
		t.Fatalf("expected planning, got %s", afterPlanning.Status)
	}

	afterPlanning.Plan = makePlan(1)
	if err := db.Save(&afterPlanning).Error; err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := o.processPlanning(&afterPlanning); err != nil {
		t.Fatalf("processPlanning: %v", err)
	}

	var afterReview model.Task
	if err := db.First(&afterReview, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload after planning: %v", err)
	}
	if afterReview.Status != model.StatusPlanReview {
		t.Fatalf("expected plan_review, got %s", afterReview.Status)
	}

	if err := o.HandlePlanApproved(task.ID); err != nil {
		t.Fatalf("HandlePlanApproved: %v", err)
	}

	var afterApprove model.Task
	if err := db.First(&afterApprove, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload after approval: %v", err)
	}
	if afterApprove.Status != model.StatusInProgress {
		t.Fatalf("expected in_progress, got %s", afterApprove.Status)
	}

	var subtask model.Task
	if err := db.Where("parent_task_id = ?", task.ID).First(&subtask).Error; err != nil {
		t.Fatalf("load subtask: %v", err)
	}
	subtask.Status = model.StatusDone
	if err := db.Save(&subtask).Error; err != nil {
		t.Fatalf("mark subtask done: %v", err)
	}

	if err := o.checkFeatureCompletion(&afterApprove); err != nil {
		t.Fatalf("checkFeatureCompletion: %v", err)
	}

	var afterTestingReady model.Task
	if err := db.First(&afterTestingReady, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload after completion check: %v", err)
	}
	if afterTestingReady.Status != model.StatusTestingReady {
		t.Fatalf("expected testing_ready, got %s", afterTestingReady.Status)
	}

	_, _, _ = authorizeTestDelivery(t, o, task.ID)

	var afterMerging model.Task
	if err := db.First(&afterMerging, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload after test pass: %v", err)
	}
	if afterMerging.Status != model.StatusMerging {
		t.Fatalf("expected merging, got %s", afterMerging.Status)
	}

	var intent model.MergeIntent
	if err := db.Where("task_id = ?", task.ID).Order("created_at DESC").First(&intent).Error; err != nil {
		t.Fatalf("load merge intent: %v", err)
	}
	if _, err := o.completeAuthorizedMerge(task.ID, intent.ID, strings.Repeat("c", 40), "test"); err != nil {
		t.Fatalf("completeAuthorizedMerge: %v", err)
	}

	var final model.Task
	if err := db.First(&final, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload final task: %v", err)
	}
	if final.Status != model.StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
}

func TestTaskLifecycle_TestFailedBackToPlanning(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "test-fail-replan", model.StatusTestingReady, makePlan(1))

	now := time.Now()
	artifact, err := o.FreezeDeliveryArtifact(task.ID, ArtifactSnapshot{
		Branch: "feature/test-fail", CommitSHA: strings.Repeat("a", 40), BaseBranch: "main", BaseSHA: strings.Repeat("b", 40),
		GateWorkspaceID: "test-gate-workspace", EnvironmentFingerprint: "test-environment",
		PreliminaryEvidence: []CommandEvidence{{Command: "go test ./...", Passed: true, StartedAt: now, FinishedAt: now}},
		Actor:               "orchestrator", Source: "test",
	})
	if err != nil {
		t.Fatalf("freeze delivery: %v", err)
	}
	var verificationReady model.Task
	db.First(&verificationReady, "id = ?", task.ID)
	verifiedAt := time.Now()
	_, err = o.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: verificationReady.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "test:verifier", Source: "test", EnvironmentFingerprint: "test",
		CommandEvidence: []CommandEvidence{{Command: "native verify", Passed: false, ExitCode: 1, StartedAt: verifiedAt, FinishedAt: verifiedAt}},
		Result:          model.VerificationFailed, IdempotencyKey: "failed-" + task.ID.String(),
	})
	if err != nil {
		t.Fatalf("failed verification: %v", err)
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

	if task.Status != model.StatusClassifying {
		t.Errorf("expected status %q, got %q", model.StatusClassifying, task.Status)
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

func TestRetryTask_RetriesPausedPreliminaryGateWithoutDiscardingBranch(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "retry-paused-gate", model.StatusPaused, nil)
	task.WorktreeBranch = "feature/retry-paused-gate"
	task.WorktreeBaseSHA = strings.Repeat("a", 40)
	require.NoError(t, db.Save(&task).Error)
	gate := model.PreliminaryGateRun{
		ID: uuid.New(), TaskID: task.ID, Branch: task.WorktreeBranch,
		CommitSHA: strings.Repeat("b", 40), BaseBranch: "main", BaseSHA: task.WorktreeBaseSHA,
		WorkspaceID: "gate-workspace", EnvironmentFingerprint: "linux/arm64",
		CommandEvidence: model.JSONField{"commands": []any{}}, Outcome: model.PreliminaryGateConfiguration,
		Actor: "orchestrator", Source: "testing_ready", StartedAt: time.Now(), FinishedAt: time.Now(),
	}
	require.NoError(t, db.Create(&gate).Error)

	require.NoError(t, o.RetryTask(task.ID))
	var updated model.Task
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, updated.Status)
	require.Equal(t, task.WorktreeBranch, updated.WorktreeBranch)
	require.Equal(t, task.WorktreeBaseSHA, updated.WorktreeBaseSHA)
	require.Empty(t, o.worktree.(*FakeWorktreeManager).RemovedFeatures)
}

func TestRetryTask_RefusesPausedTaskWithoutRetryableGate(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "operator-paused", model.StatusPaused, nil)

	err := o.RetryTask(task.ID)
	require.ErrorIs(t, err, ErrRetryPausedGateMissing)
	var updated model.Task
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPaused, updated.Status)
}

func TestRetryTask_RefusesTopLevelParentWithChildren(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parent := createLifecycleTask(t, db, o.projectID, "retry-parent", model.StatusFailed, nil)
	parent.WorktreeBranch = "feature/stale-retry-parent"
	db.Save(&parent)
	doneChild := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parent.ID,
		Title:        "done child",
		Description:  "stale done child",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	failedChild := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parent.ID,
		Title:        "failed child",
		Description:  "stale failed child",
		Status:       model.StatusFailed,
		Phase:        "test",
	}
	db.Create(&doneChild)
	db.Create(&failedChild)

	if err := o.RetryTask(parent.ID); err == nil {
		t.Fatal("RetryTask: expected error for parent with child history, got nil")
	} else if !errors.Is(err, ErrRetryParentHasChildren) {
		t.Fatalf("RetryTask: expected ErrRetryParentHasChildren, got %v", err)
	}

	var reloadedParent model.Task
	db.First(&reloadedParent, "id = ?", parent.ID)
	if reloadedParent.Status != model.StatusFailed {
		t.Fatalf("expected parent to remain failed, got %s", reloadedParent.Status)
	}
	if reloadedParent.WorktreeBranch != "feature/stale-retry-parent" {
		t.Fatalf("expected stale parent worktree branch preserved, got %q", reloadedParent.WorktreeBranch)
	}
	if len(o.worktree.(*FakeWorktreeManager).RemovedFeatures) != 0 {
		t.Fatalf("expected no stale feature removal, got %v", o.worktree.(*FakeWorktreeManager).RemovedFeatures)
	}

	var attachedCount int64
	db.Model(&model.Task{}).Where("parent_task_id = ?", parent.ID).Count(&attachedCount)
	if attachedCount != 2 {
		t.Fatalf("expected stale children to remain attached, got %d", attachedCount)
	}

	var reloadedDone model.Task
	db.First(&reloadedDone, "id = ?", doneChild.ID)
	if reloadedDone.ParentTaskID == nil || *reloadedDone.ParentTaskID != parent.ID {
		t.Fatalf("expected done child parent preserved, got %v", reloadedDone.ParentTaskID)
	}
	if reloadedDone.Status != model.StatusDone {
		t.Fatalf("expected done child status preserved, got %s", reloadedDone.Status)
	}

	var reloadedFailed model.Task
	db.First(&reloadedFailed, "id = ?", failedChild.ID)
	if reloadedFailed.ParentTaskID == nil || *reloadedFailed.ParentTaskID != parent.ID {
		t.Fatalf("expected failed child parent preserved, got %v", reloadedFailed.ParentTaskID)
	}
	if reloadedFailed.Status != model.StatusFailed {
		t.Fatalf("expected failed child status preserved, got %s", reloadedFailed.Status)
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
// DeleteTask tests
// ---------------------------------------------------------------------------

func TestDeleteTask_RootNoSubtasks(t *testing.T) {
	o, db := setupLifecycleTest(t)

	task := createLifecycleTask(t, db, o.projectID, "delete-root-no-subs", model.StatusBacklog, nil)

	// Add comments to the task.
	comment := model.TaskComment{
		ID:     uuid.New(),
		TaskID: task.ID,
		Author: "user",
		Body:   "this task should be deleted",
	}
	if err := db.Create(&comment).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}

	// Add events to the task.
	event := model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "status_change",
		OldValue:  "",
		NewValue:  string(model.StatusBacklog),
		Actor:     "system",
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}

	// Delete the task.
	if err := o.DeleteTask(task.ID); err != nil {
		t.Fatalf("DeleteTask: unexpected error: %v", err)
	}

	// Verify the task is gone.
	var count int64
	db.Model(&model.Task{}).Where("id = ?", task.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected task to be deleted, but found %d", count)
	}

	// Verify comments are gone.
	db.Model(&model.TaskComment{}).Where("task_id = ?", task.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected comments to be deleted, but found %d", count)
	}

	// Verify events are gone.
	db.Model(&model.TaskEvent{}).Where("task_id = ?", task.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected events to be deleted, but found %d", count)
	}
}

func TestDeleteTask_RootWithSubtasks(t *testing.T) {
	o, db := setupLifecycleTest(t)

	// Create a root task with a plan.
	plan := makePlan(2)
	parent := createLifecycleTask(t, db, o.projectID, "delete-root-with-subs", model.StatusInProgress, plan)

	// Create subtasks attached to the parent.
	parentID := parent.ID
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "subtask-1",
		Description:  "first subtask",
		Status:       model.StatusInProgress,
	}
	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "subtask-2",
		Description:  "second subtask",
		Status:       model.StatusDone,
	}
	if err := db.Create(&sub1).Error; err != nil {
		t.Fatalf("create sub1: %v", err)
	}
	if err := db.Create(&sub2).Error; err != nil {
		t.Fatalf("create sub2: %v", err)
	}

	// Add comments and events to subtasks.
	for _, subID := range []uuid.UUID{sub1.ID, sub2.ID} {
		db.Create(&model.TaskComment{
			ID:     uuid.New(),
			TaskID: subID,
			Author: "system",
			Body:   "subtask comment",
		})
		db.Create(&model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    subID,
			EventType: "status_change",
			OldValue:  string(model.StatusBacklog),
			NewValue:  string(model.StatusInProgress),
			Actor:     "system",
		})
	}

	// Add comments and events to the parent task.
	db.Create(&model.TaskComment{
		ID:     uuid.New(),
		TaskID: parent.ID,
		Author: "user",
		Body:   "parent comment",
	})
	db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    parent.ID,
		EventType: "status_change",
		OldValue:  string(model.StatusBacklog),
		NewValue:  string(model.StatusInProgress),
		Actor:     "system",
	})

	// Delete the root task — should cascade to subtasks.
	if err := o.DeleteTask(parent.ID); err != nil {
		t.Fatalf("DeleteTask: unexpected error: %v", err)
	}

	// Verify parent task is gone.
	var count int64
	db.Model(&model.Task{}).Where("id = ?", parent.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected parent task to be deleted, but found %d", count)
	}

	// Verify subtasks are gone.
	db.Model(&model.Task{}).Where("parent_task_id = ?", parent.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected subtasks to be deleted, but found %d", count)
	}

	// Verify subtask comments are gone.
	db.Model(&model.TaskComment{}).Where("task_id IN ?", []uuid.UUID{sub1.ID, sub2.ID}).Count(&count)
	if count != 0 {
		t.Errorf("expected subtask comments to be deleted, but found %d", count)
	}

	// Verify subtask events are gone.
	db.Model(&model.TaskEvent{}).Where("task_id IN ?", []uuid.UUID{sub1.ID, sub2.ID}).Count(&count)
	if count != 0 {
		t.Errorf("expected subtask events to be deleted, but found %d", count)
	}

	// Verify parent comments are gone.
	db.Model(&model.TaskComment{}).Where("task_id = ?", parent.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected parent comments to be deleted, but found %d", count)
	}

	// Verify parent events are gone.
	db.Model(&model.TaskEvent{}).Where("task_id = ?", parent.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected parent events to be deleted, but found %d", count)
	}
}

func TestDeleteTask_RootWithSubtasksAndAgents(t *testing.T) {
	o, db := setupLifecycleTest(t)

	parent := createLifecycleTask(t, db, o.projectID, "delete-root-agents", model.StatusInProgress, makePlan(1))
	parentID := parent.ID

	// Create an agent assigned to a subtask.
	agent := model.Agent{
		ID:        uuid.New(),
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "test-agent",
		Status:    model.AgentWorking,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	agentID := agent.ID
	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask-with-agent",
		Description:     "subtask with assigned agent",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create subtask: %v", err)
	}

	// Delete the root task.
	if err := o.DeleteTask(parent.ID); err != nil {
		t.Fatalf("DeleteTask: unexpected error: %v", err)
	}

	// Verify agent was marked as dead.
	var updatedAgent model.Agent
	if err := db.First(&updatedAgent, "id = ?", agentID).Error; err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status %q, got %q", model.AgentDead, updatedAgent.Status)
	}

	// Verify subtask and parent are deleted.
	var count int64
	db.Model(&model.Task{}).Where("id = ?", sub.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected subtask to be deleted, but found %d", count)
	}
	db.Model(&model.Task{}).Where("id = ?", parent.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected parent to be deleted, but found %d", count)
	}
}

func TestRetryTask_UnlinksStaleAgents(t *testing.T) {
	o, db := setupLifecycleTest(t)

	taskID := uuid.New()
	agentID := uuid.New()

	task := model.Task{
		ID:              taskID,
		ProjectID:       o.projectID,
		Title:           "retry-unlink-task",
		Description:     "task with stale agent",
		Status:          model.StatusFailed,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(2), "last_error": "some error"},
	}
	db.Create(&task)

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "stale-retry-agent",
		Status:        model.AgentDead,
		CurrentTaskID: &taskID,
	}
	db.Create(&ag)

	err := o.RetryTask(taskID)
	if err != nil {
		t.Fatalf("RetryTask error: %v", err)
	}

	// Task should be in backlog with cleared context.
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected status backlog, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected AssignedAgentID to be nil")
	}

	// Stale agent should have CurrentTaskID cleared and be idle.
	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.CurrentTaskID != nil {
		t.Error("expected agent CurrentTaskID to be nil")
	}
	if updatedAgent.Status != model.AgentIdle {
		t.Errorf("expected agent status idle, got %s", updatedAgent.Status)
	}
}

func TestRetryTask_ClearsFailureContext(t *testing.T) {
	o, db := setupLifecycleTest(t)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "retry-clear-ctx",
		Description: "task with failure context",
		Status:      model.StatusFailed,
		Context: model.JSONField{
			"retry_count":               float64(1),
			"last_error":                "error msg",
			"failure_diagnosis":         "some diagnosis",
			"failure_category":          "code_error",
			"prompt_adjustment":         "try harder",
			"empty_work":                true,
			"constraint_violations":     "violated stuff",
			"test_rejection_count":      float64(2),
			"test_rejection_feedback_1": "first rejection",
			"test_rejection_feedback_2": "second rejection",
			"other_key":                 "should survive",
		},
	}
	db.Create(&task)

	err := o.RetryTask(taskID)
	if err != nil {
		t.Fatalf("RetryTask error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)

	// These keys should be deleted.
	for _, key := range []string{"retry_count", "last_error", "failure_diagnosis", "failure_category", "prompt_adjustment", "empty_work", "constraint_violations", "test_rejection_count", "test_rejection_feedback_1", "test_rejection_feedback_2"} {
		if _, ok := updated.Context[key]; ok {
			t.Errorf("expected %q to be cleared from context", key)
		}
	}

	// This key should survive.
	if _, ok := updated.Context["other_key"]; !ok {
		t.Error("expected other_key to survive retry cleanup")
	}
}

// ---------------------------------------------------------------------------
// AddComment tests
// ---------------------------------------------------------------------------

// TestAddComment_BacklogStatus verifies AddComment works on tasks in backlog status.
func TestAddComment_BacklogStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "backlog-comment", model.StatusBacklog, nil)

	err := o.AddComment(task.ID, "test-author", "This is a test comment on backlog")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	// Verify comment was stored in DB.
	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "test-author" {
		t.Errorf("expected author %q, got %q", "test-author", comments[0].Author)
	}
	if comments[0].Body != "This is a test comment on backlog" {
		t.Errorf("expected body %q, got %q", "This is a test comment on backlog", comments[0].Body)
	}
}

// TestAddComment_PlanningStatus verifies AddComment works on tasks in planning status.
func TestAddComment_PlanningStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "planning-comment", model.StatusPlanning, nil)

	err := o.AddComment(task.ID, "agent-planner", "Planning in progress")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_InProgressStatus verifies AddComment works on tasks in in_progress status.
func TestAddComment_InProgressStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "in-progress-comment", model.StatusInProgress, nil)

	err := o.AddComment(task.ID, "agent-coder", "Implementation underway")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_ClassifyingStatus verifies AddComment works on tasks in classifying status.
func TestAddComment_ClassifyingStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "classifying-comment", model.StatusClassifying, nil)

	err := o.AddComment(task.ID, "agent-classifier", "Classification in progress")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_PausedStatus verifies AddComment works on tasks in paused status.
func TestAddComment_PausedStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "paused-comment", model.StatusPaused, nil)

	err := o.AddComment(task.ID, "operator", "Task paused for review")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_DoneStatus verifies AddComment works on tasks in done status.
func TestAddComment_DoneStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "done-comment", model.StatusDone, nil)

	err := o.AddComment(task.ID, "reviewer", "Excellent work!")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_TestWritingStatus verifies AddComment works on tasks in test_writing status.
func TestAddComment_TestWritingStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "test-writing-comment", model.StatusTestWriting, nil)

	err := o.AddComment(task.ID, "agent-tester", "Writing comprehensive tests")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_MergingStatus verifies AddComment works on tasks in merging status.
func TestAddComment_MergingStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "merging-comment", model.StatusMerging, nil)

	err := o.AddComment(task.ID, "orchestrator", "Merging to main branch")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_FailedStatus verifies AddComment works on tasks in failed status.
func TestAddComment_FailedStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "failed-comment", model.StatusFailed, nil)

	err := o.AddComment(task.ID, "supervisor", "Task failed due to constraint violation")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_RejectedStatus verifies AddComment works on tasks in rejected status.
func TestAddComment_RejectedStatus(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "rejected-comment", model.StatusRejected, nil)

	err := o.AddComment(task.ID, "human-reviewer", "Plan was rejected due to unclear requirements")
	if err != nil {
		t.Fatalf("AddComment: unexpected error: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_GateStatus_PlanReview verifies AddComment still works on gate status plan_review.
func TestAddComment_GateStatus_PlanReview(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "gate-plan-review", model.StatusPlanReview, makePlan(1))

	err := o.AddComment(task.ID, "human-approver", "Plan looks good")
	if err != nil {
		t.Fatalf("AddComment: unexpected error on gate status: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_GateStatus_TestReview verifies AddComment still works on gate status test_review.
func TestAddComment_GateStatus_TestReview(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "gate-test-review", model.StatusTestReview, makePlan(1))

	err := o.AddComment(task.ID, "test-reviewer", "Tests look comprehensive")
	if err != nil {
		t.Fatalf("AddComment: unexpected error on gate status: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_GateStatus_TestingReady verifies AddComment still works on gate status testing_ready.
func TestAddComment_GateStatus_TestingReady(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "gate-testing-ready", model.StatusTestingReady, makePlan(1))

	err := o.AddComment(task.ID, "final-reviewer", "Ready to merge")
	if err != nil {
		t.Fatalf("AddComment: unexpected error on gate status: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_GateStatus_NeedsClarification verifies AddComment still works on gate status needs_clarification.
func TestAddComment_GateStatus_NeedsClarification(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "gate-needs-clarification", model.StatusNeedsClarification, nil)

	err := o.AddComment(task.ID, "clarifier", "Need more details on requirements")
	if err != nil {
		t.Fatalf("AddComment: unexpected error on gate status: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

// TestAddComment_TaskNotFound verifies AddComment returns error when task doesn't exist.
func TestAddComment_TaskNotFound(t *testing.T) {
	o, _ := setupLifecycleTest(t)
	nonexistentID := uuid.New()

	err := o.AddComment(nonexistentID, "author", "comment on non-existent task")
	if err == nil {
		t.Fatal("AddComment: expected error for non-existent task, got nil")
	}
}

// TestAddComment_VerifyGetComments tests retrieving comments for a task.
func TestAddComment_VerifyGetComments(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "get-comments-test", model.StatusBacklog, nil)

	// Add multiple comments.
	if err := o.AddComment(task.ID, "author1", "First comment"); err != nil {
		t.Fatalf("AddComment 1: %v", err)
	}
	if err := o.AddComment(task.ID, "author2", "Second comment"); err != nil {
		t.Fatalf("AddComment 2: %v", err)
	}
	if err := o.AddComment(task.ID, "author3", "Third comment"); err != nil {
		t.Fatalf("AddComment 3: %v", err)
	}

	// Retrieve comments using GetComments.
	comments, err := o.GetComments(task.ID)
	if err != nil {
		t.Fatalf("GetComments: %v", err)
	}

	if len(comments) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(comments))
	}

	// Verify order and content (should be ordered by created_at asc).
	if comments[0].Author != "author1" || comments[0].Body != "First comment" {
		t.Error("first comment mismatch")
	}
	if comments[1].Author != "author2" || comments[1].Body != "Second comment" {
		t.Error("second comment mismatch")
	}
	if comments[2].Author != "author3" || comments[2].Body != "Third comment" {
		t.Error("third comment mismatch")
	}
}

// TestAddComment_EmptyAuthor verifies AddComment works even with empty author string.
func TestAddComment_EmptyAuthor(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "empty-author", model.StatusBacklog, nil)

	err := o.AddComment(task.ID, "", "Comment with empty author")
	if err != nil {
		t.Fatalf("AddComment: unexpected error with empty author: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "" {
		t.Errorf("expected empty author, got %q", comments[0].Author)
	}
}

// TestAddComment_EmptyBody verifies AddComment works even with empty body string.
func TestAddComment_EmptyBody(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "empty-body", model.StatusBacklog, nil)

	err := o.AddComment(task.ID, "author", "")
	if err != nil {
		t.Fatalf("AddComment: unexpected error with empty body: %v", err)
	}

	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Body != "" {
		t.Errorf("expected empty body, got %q", comments[0].Body)
	}
}

// TestAddComment_MultipleCommentsOnTask verifies multiple comments can be added to the same task.
func TestAddComment_MultipleCommentsOnTask(t *testing.T) {
	o, db := setupLifecycleTest(t)
	task := createLifecycleTask(t, db, o.projectID, "multiple-comments", model.StatusBacklog, nil)

	// Add 5 comments to the same task.
	for i := 0; i < 5; i++ {
		author := fmt.Sprintf("author-%d", i)
		body := fmt.Sprintf("Comment %d", i)
		if err := o.AddComment(task.ID, author, body); err != nil {
			t.Fatalf("AddComment %d: %v", i, err)
		}
	}

	// Verify all comments were stored.
	var comments []model.TaskComment
	db.Where("task_id = ?", task.ID).Find(&comments)
	if len(comments) != 5 {
		t.Fatalf("expected 5 comments, got %d", len(comments))
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
