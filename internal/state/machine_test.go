package state

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestValidateTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    model.TaskStatus
		to      model.TaskStatus
		wantErr bool
	}{
		// PLAN_REVIEW now goes to TEST_WRITING, not IN_PROGRESS
		{"plan_review to test_writing is valid", model.StatusPlanReview, model.StatusTestWriting, false},
		{"plan_review to in_progress (backward compat)", model.StatusPlanReview, model.StatusInProgress, false},
		{"plan_review to planning is valid", model.StatusPlanReview, model.StatusPlanning, false},

		// TEST_WRITING transitions
		{"test_writing to test_review is valid", model.StatusTestWriting, model.StatusTestReview, false},
		{"test_writing to failed is valid", model.StatusTestWriting, model.StatusFailed, false},
		{"test_writing to paused is valid", model.StatusTestWriting, model.StatusPaused, false},
		{"test_writing to in_progress is INVALID", model.StatusTestWriting, model.StatusInProgress, true},
		{"test_writing to done is INVALID", model.StatusTestWriting, model.StatusDone, true},

		// TEST_REVIEW transitions
		{"test_review to in_progress is valid (approve)", model.StatusTestReview, model.StatusInProgress, false},
		{"test_review to test_writing is valid (reject)", model.StatusTestReview, model.StatusTestWriting, false},
		{"test_review to merging is INVALID", model.StatusTestReview, model.StatusMerging, true},
		{"test_review to done is INVALID", model.StatusTestReview, model.StatusDone, true},

		// PAUSED gains TEST_WRITING as a valid target
		{"paused to test_writing is valid", model.StatusPaused, model.StatusTestWriting, false},
		{"paused to backlog is valid", model.StatusPaused, model.StatusBacklog, false},
		{"paused to planning is valid", model.StatusPaused, model.StatusPlanning, false},
		{"paused to in_progress is valid", model.StatusPaused, model.StatusInProgress, false},
		{"paused to testing_ready is valid", model.StatusPaused, model.StatusTestingReady, false},

		// REJECTED work can only be archived as cancelled.
		{"rejected to backlog is INVALID", model.StatusRejected, model.StatusBacklog, true},
		{"rejected to planning is INVALID", model.StatusRejected, model.StatusPlanning, true},
		{"rejected to in_progress is INVALID", model.StatusRejected, model.StatusInProgress, true},
		{"rejected to test_writing is INVALID", model.StatusRejected, model.StatusTestWriting, true},
		{"rejected to cancelled archive", model.StatusRejected, model.StatusCancelled, false},

		// NEEDS_CLARIFICATION transitions
		{"planning to needs_clarification is valid", model.StatusPlanning, model.StatusNeedsClarification, false},
		{"needs_clarification to planning is valid (replan)", model.StatusNeedsClarification, model.StatusPlanning, false},
		{"needs_clarification to plan_review is valid (skip/done)", model.StatusNeedsClarification, model.StatusPlanReview, false},
		{"needs_clarification to in_progress is INVALID", model.StatusNeedsClarification, model.StatusInProgress, true},
		{"needs_clarification to failed is INVALID", model.StatusNeedsClarification, model.StatusFailed, true},
		{"paused to needs_clarification is valid", model.StatusPaused, model.StatusNeedsClarification, false},

		// Existing transitions that should still work
		{"backlog to planning", model.StatusBacklog, model.StatusPlanning, false},
		{"backlog to paused", model.StatusBacklog, model.StatusPaused, false},
		{"planning to plan_review", model.StatusPlanning, model.StatusPlanReview, false},
		{"planning to failed", model.StatusPlanning, model.StatusFailed, false},
		{"planning to paused", model.StatusPlanning, model.StatusPaused, false},
		{"in_progress to testing_ready", model.StatusInProgress, model.StatusTestingReady, false},
		{"in_progress to backlog recovery", model.StatusInProgress, model.StatusBacklog, false},
		{"in_progress to failed", model.StatusInProgress, model.StatusFailed, false},
		{"in_progress to paused", model.StatusInProgress, model.StatusPaused, false},
		{"testing_ready to merging is invalid", model.StatusTestingReady, model.StatusMerging, true},
		{"testing_ready to verification_ready", model.StatusTestingReady, model.StatusVerificationReady, false},
		{"testing_ready to in_progress", model.StatusTestingReady, model.StatusInProgress, false},
		{"testing_ready to planning", model.StatusTestingReady, model.StatusPlanning, false},
		{"testing_ready to failed gate", model.StatusTestingReady, model.StatusFailed, false},
		{"testing_ready to paused runner failure", model.StatusTestingReady, model.StatusPaused, false},
		{"verification_ready to integration_ready", model.StatusVerificationReady, model.StatusIntegrationReady, false},
		{"verification_ready to host_rework", model.StatusVerificationReady, model.StatusHostRework, false},
		{"verification_ready to in_progress", model.StatusVerificationReady, model.StatusInProgress, false},
		{"host_rework to testing_ready", model.StatusHostRework, model.StatusTestingReady, false},
		{"host_rework to in_progress", model.StatusHostRework, model.StatusInProgress, false},
		{"host_rework to cancelled", model.StatusHostRework, model.StatusCancelled, false},
		{"host_rework to integration_ready is invalid", model.StatusHostRework, model.StatusIntegrationReady, true},
		{"integration_ready to merging", model.StatusIntegrationReady, model.StatusMerging, false},
		{"integration_ready to host_rework", model.StatusIntegrationReady, model.StatusHostRework, false},
		{"integration_ready to in_progress", model.StatusIntegrationReady, model.StatusInProgress, false},
		{"merging to done", model.StatusMerging, model.StatusDone, false},
		{"merging to failed", model.StatusMerging, model.StatusFailed, false},
		{"done is terminal", model.StatusDone, model.StatusBacklog, true},
		{"failed to backlog", model.StatusFailed, model.StatusBacklog, false},
		{"failed to in_progress", model.StatusFailed, model.StatusInProgress, false},
		{"failed to cancelled archive", model.StatusFailed, model.StatusCancelled, false},
		{"failed to done is INVALID", model.StatusFailed, model.StatusDone, true},
		{"cancelled is terminal", model.StatusCancelled, model.StatusBacklog, true},
		{"active task may be cancelled", model.StatusPlanning, model.StatusCancelled, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTransition(tc.from, tc.to)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateTransition(%q, %q) = nil, want error", tc.from, tc.to)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateTransition(%q, %q) = %v, want nil", tc.from, tc.to, err)
			}
		})
	}
}

func TestValidTransitionsModelsEveryKnownTaskStatus(t *testing.T) {
	statuses := []model.TaskStatus{
		model.StatusClassifying,
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusNeedsClarification,
		model.StatusPlanReview,
		model.StatusTestWriting,
		model.StatusTestReview,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusVerificationReady,
		model.StatusHostRework,
		model.StatusIntegrationReady,
		model.StatusMerging,
		model.StatusPaused,
		model.StatusDone,
		model.StatusFailed,
		model.StatusRejected,
		model.StatusCancelled,
	}
	for _, status := range statuses {
		if _, ok := ValidTransitions[status]; !ok {
			t.Errorf("ValidTransitions missing status %q", status)
		}
	}
	if got := ValidTransitions[model.StatusCancelled]; len(got) != 0 {
		t.Errorf("cancelled transitions = %v, want terminal state", got)
	}
}

func TestTransitionTask_TestWritingToTestReview(t *testing.T) {
	taskID := uuid.New()
	projectID := uuid.New()
	now := time.Now()
	task := &model.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Write tests",
		Status:    model.StatusTestWriting,
		CreatedAt: now,
		UpdatedAt: now,
	}

	details := map[string]any{"reason": "tests complete"}
	event, err := TransitionTask(task, model.StatusTestReview, "agent:test-writer", details)
	if err != nil {
		t.Fatalf("TransitionTask returned error: %v", err)
	}

	if task.Status != model.StatusTestReview {
		t.Errorf("task.Status = %q, want %q", task.Status, model.StatusTestReview)
	}
	if event.TaskID != taskID {
		t.Errorf("event.TaskID = %v, want %v", event.TaskID, taskID)
	}
	if event.EventType != "status_change" {
		t.Errorf("event.EventType = %q, want %q", event.EventType, "status_change")
	}
	if event.OldValue != string(model.StatusTestWriting) {
		t.Errorf("event.OldValue = %q, want %q", event.OldValue, model.StatusTestWriting)
	}
	if event.NewValue != string(model.StatusTestReview) {
		t.Errorf("event.NewValue = %q, want %q", event.NewValue, model.StatusTestReview)
	}
	if event.Actor != "agent:test-writer" {
		t.Errorf("event.Actor = %q, want %q", event.Actor, "agent:test-writer")
	}
	if event.ID == uuid.Nil {
		t.Error("event.ID should not be nil")
	}
}

func TestGuardedCompleteSubtaskCannotBypassTopLevelDelivery(t *testing.T) {
	task := &model.Task{ID: uuid.New(), Status: model.StatusInProgress}
	request := TransitionRequest{
		Target: model.StatusDone, ExpectedStatus: model.StatusInProgress,
		Evidence: Evidence{TaskID: task.ID, Actor: "orchestrator", Source: "test", Reason: "complete"},
	}
	if _, err := GuardedCompleteSubtask(task, request); err == nil {
		t.Fatal("top-level task must not complete without delivery states")
	}
	parentID := uuid.New()
	task.ParentTaskID = &parentID
	if _, err := GuardedCompleteSubtask(task, request); err != nil {
		t.Fatalf("subtask completion failed: %v", err)
	}
	if task.Status != model.StatusDone {
		t.Fatalf("subtask status = %q, want done", task.Status)
	}
}

func TestGuardedAdoptFailedSubtaskRequiresDedicatedEvidence(t *testing.T) {
	parentID := uuid.New()
	task := &model.Task{ID: uuid.New(), ParentTaskID: &parentID, Status: model.StatusFailed}
	req := TransitionRequest{
		Target: model.StatusDone, Actor: "codex:pilot", ExpectedStatus: model.StatusFailed,
		Evidence: Evidence{TaskID: task.ID, Actor: "codex:pilot", Source: "worker_completion", Reason: "repair", Timestamp: time.Now()},
	}
	if _, err := GuardedAdoptFailedSubtask(task, req); err == nil {
		t.Fatal("expected non-adoption evidence to be rejected")
	}
	req.Evidence.Source = "codex_adapter_adoption"
	if _, err := GuardedAdoptFailedSubtask(task, req); err != nil {
		t.Fatalf("expected dedicated adoption evidence to pass: %v", err)
	}
	if task.Status != model.StatusDone {
		t.Fatalf("got %s, want done", task.Status)
	}
}

func TestGuardedAcceptExistingSubtaskRequiresExplicitDedupEvidence(t *testing.T) {
	parentID := uuid.New()
	task := &model.Task{ID: uuid.New(), ParentTaskID: &parentID, Status: model.StatusBacklog}
	request := TransitionRequest{
		Target: model.StatusDone, ExpectedStatus: model.StatusBacklog,
		Evidence: Evidence{TaskID: task.ID, Actor: "orchestrator", Source: "worker_completion", Reason: "already present"},
	}
	if _, err := GuardedAcceptExistingSubtask(task, request); !errors.Is(err, ErrMissingEvidence) {
		t.Fatalf("wrong evidence source error = %v, want ErrMissingEvidence", err)
	}
	if task.Status != model.StatusBacklog {
		t.Fatalf("rejected transition mutated task to %q", task.Status)
	}

	request.Evidence.Source = "dedup_existing_work"
	event, err := GuardedAcceptExistingSubtask(task, request)
	if err != nil {
		t.Fatalf("accept existing subtask: %v", err)
	}
	if task.Status != model.StatusDone || event.OldValue != string(model.StatusBacklog) || event.NewValue != string(model.StatusDone) {
		t.Fatalf("unexpected accepted transition: status=%q event=%#v", task.Status, event)
	}

	topLevel := &model.Task{ID: uuid.New(), Status: model.StatusBacklog}
	request.Evidence.TaskID = topLevel.ID
	if _, err := GuardedAcceptExistingSubtask(topLevel, request); err == nil {
		t.Fatal("top-level task must not use existing-subtask acceptance")
	}
}

func TestGuardedSupersedeCompletedTestSubtaskIsNarrow(t *testing.T) {
	parentID := uuid.New()
	task := &model.Task{ID: uuid.New(), ParentTaskID: &parentID, Phase: "test", Status: model.StatusDone}
	request := TransitionRequest{
		Target: model.StatusRejected, ExpectedStatus: model.StatusDone,
		Evidence: Evidence{TaskID: task.ID, Actor: "codex:reviewer", Source: "review_gate", Reason: "tests need revision"},
	}
	event, err := GuardedSupersedeCompletedTestSubtask(task, request)
	if err != nil {
		t.Fatalf("supersede completed test subtask: %v", err)
	}
	if task.Status != model.StatusRejected || event.OldValue != string(model.StatusDone) || event.NewValue != string(model.StatusRejected) {
		t.Fatalf("unexpected supersession: status=%q event=%#v", task.Status, event)
	}

	implementation := &model.Task{ID: uuid.New(), ParentTaskID: &parentID, Phase: "implementation", Status: model.StatusDone}
	request.Evidence.TaskID = implementation.ID
	if _, err := GuardedSupersedeCompletedTestSubtask(implementation, request); err == nil {
		t.Fatal("completed implementation subtask must remain terminal")
	}
	topLevel := &model.Task{ID: uuid.New(), Phase: "test", Status: model.StatusDone}
	request.Evidence.TaskID = topLevel.ID
	if _, err := GuardedSupersedeCompletedTestSubtask(topLevel, request); err == nil {
		t.Fatal("top-level done task must remain terminal")
	}
}

func TestGuardedInvalidateCompletedTestSubtaskCanFailForCodexRepair(t *testing.T) {
	parentID := uuid.New()
	task := &model.Task{ID: uuid.New(), ParentTaskID: &parentID, Phase: "test", Status: model.StatusDone}
	event, err := GuardedInvalidateCompletedTestSubtask(task, TransitionRequest{
		Target: model.StatusFailed, Actor: "policy:sglang-safe-auto", ExpectedStatus: model.StatusDone,
		Evidence: Evidence{TaskID: task.ID, Actor: "policy:sglang-safe-auto", Source: "review_gate", Reason: "review rejected"},
	})
	if err != nil {
		t.Fatalf("invalidate completed test subtask: %v", err)
	}
	if task.Status != model.StatusFailed || event.OldValue != string(model.StatusDone) || event.NewValue != string(model.StatusFailed) {
		t.Fatalf("unexpected invalidation: status=%q event=%#v", task.Status, event)
	}
}

func TestValidateTransition_TestWritingToPlanning(t *testing.T) {
	// The recovery path for empty test subtasks requires test_writing → planning
	// to be a valid transition so the orchestrator can send the task back
	// for replanning with a directive to generate test subtasks.
	err := ValidateTransition(model.StatusTestWriting, model.StatusPlanning)
	if err != nil {
		t.Errorf("ValidateTransition(test_writing, planning) = %v, want nil — "+
			"test_writing → planning must be valid for empty-subtask recovery", err)
	}
}

func TestNeedsClarification_IsApprovalGate(t *testing.T) {
	if !model.StatusNeedsClarification.IsApprovalGate() {
		t.Error("StatusNeedsClarification.IsApprovalGate() = false, want true")
	}
}

func TestNeedsClarification_IsNotActionable(t *testing.T) {
	if model.StatusNeedsClarification.IsActionable() {
		t.Error("StatusNeedsClarification.IsActionable() = true, want false")
	}
}

func TestParseTaskStatus_NeedsClarification(t *testing.T) {
	status, err := model.ParseTaskStatus("needs_clarification")
	if err != nil {
		t.Fatalf("ParseTaskStatus(\"needs_clarification\") returned error: %v", err)
	}
	if status != model.StatusNeedsClarification {
		t.Errorf("ParseTaskStatus(\"needs_clarification\") = %q, want %q", status, model.StatusNeedsClarification)
	}
}

func TestValidateTransition_BacklogToInProgress(t *testing.T) {
	// Quick fix tasks need backlog → in_progress to skip planning.
	if err := ValidateTransition(model.StatusBacklog, model.StatusInProgress); err != nil {
		t.Errorf("ValidateTransition(backlog, in_progress) = %v, want nil", err)
	}
}

func TestValidateTransition_BacklogExistingTargetsStillWork(t *testing.T) {
	// Ensure the existing backlog transitions are not broken.
	tests := []struct {
		name   string
		target model.TaskStatus
	}{
		{"backlog to planning", model.StatusPlanning},
		{"backlog to paused", model.StatusPaused},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTransition(model.StatusBacklog, tc.target); err != nil {
				t.Errorf("ValidateTransition(backlog, %q) = %v, want nil", tc.target, err)
			}
		})
	}
}

func TestTransitionTask_InvalidTransition(t *testing.T) {
	task := &model.Task{
		ID:     uuid.New(),
		Status: model.StatusTestWriting,
	}
	_, err := TransitionTask(task, model.StatusDone, "user", nil)
	if err == nil {
		t.Error("TransitionTask should return error for invalid transition test_writing -> done")
	}
	if task.Status != model.StatusTestWriting {
		t.Errorf("task.Status should remain %q after failed transition, got %q", model.StatusTestWriting, task.Status)
	}
}

func TestGuardedTransitionTask_RecordsEvidenceEnvelope(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	task := &model.Task{
		ID:        uuid.New(),
		Status:    model.StatusInProgress,
		UpdatedAt: now.Add(-time.Minute),
	}
	attemptID := uuid.New()

	event, err := GuardedTransitionTask(task, TransitionRequest{
		Target:         model.StatusTestingReady,
		ExpectedStatus: model.StatusInProgress,
		Evidence: Evidence{
			TaskID:           task.ID,
			AttemptID:        attemptID,
			Actor:            "orchestrator",
			Source:           "docker-events",
			Reason:           "container exited zero after watchdog push",
			NormalizedReason: "worker_completed",
			Timestamp:        now,
			References: map[string]any{
				"container_id": "container-1",
				"branch":       "feature/task",
			},
		},
	})
	if err != nil {
		t.Fatalf("GuardedTransitionTask returned error: %v", err)
	}

	if task.Status != model.StatusTestingReady {
		t.Fatalf("task.Status = %q, want %q", task.Status, model.StatusTestingReady)
	}
	if !task.UpdatedAt.Equal(now) {
		t.Fatalf("task.UpdatedAt = %v, want %v", task.UpdatedAt, now)
	}
	if event.Actor != "orchestrator" {
		t.Fatalf("event.Actor = %q, want orchestrator", event.Actor)
	}
	if !event.CreatedAt.Equal(now) {
		t.Fatalf("event.CreatedAt = %v, want %v", event.CreatedAt, now)
	}
	evidence, ok := event.Details["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("event details missing evidence envelope: %#v", event.Details)
	}
	if evidence["task_id"] != task.ID.String() {
		t.Fatalf("evidence task_id = %v, want %s", evidence["task_id"], task.ID)
	}
	if evidence["attempt_id"] != attemptID.String() {
		t.Fatalf("evidence attempt_id = %v, want %s", evidence["attempt_id"], attemptID)
	}
	if evidence["normalized_reason"] != "worker_completed" {
		t.Fatalf("evidence normalized_reason = %v, want worker_completed", evidence["normalized_reason"])
	}
}

func TestGuardedTransitionTask_RequiresEvidence(t *testing.T) {
	task := &model.Task{ID: uuid.New(), Status: model.StatusInProgress}

	_, err := GuardedTransitionTask(task, TransitionRequest{
		Target: model.StatusTestingReady,
		Evidence: Evidence{
			TaskID: task.ID,
			Actor:  "orchestrator",
			Source: "docker-events",
		},
	})
	if !errors.Is(err, ErrMissingEvidence) {
		t.Fatalf("GuardedTransitionTask error = %v, want ErrMissingEvidence", err)
	}
	if task.Status != model.StatusInProgress {
		t.Fatalf("task.Status = %q, want unchanged", task.Status)
	}
}

func TestGuardedTransitionTask_RejectsStaleObservedStatus(t *testing.T) {
	task := &model.Task{ID: uuid.New(), Status: model.StatusTestingReady}

	_, err := GuardedTransitionTask(task, TransitionRequest{
		Target:         model.StatusMerging,
		ExpectedStatus: model.StatusInProgress,
		Evidence: Evidence{
			TaskID:           task.ID,
			Actor:            "dremctl",
			Source:           "recovery-api",
			NormalizedReason: "operator_recovery",
		},
	})
	if !errors.Is(err, ErrStaleTransition) {
		t.Fatalf("GuardedTransitionTask error = %v, want ErrStaleTransition", err)
	}
	if task.Status != model.StatusTestingReady {
		t.Fatalf("task.Status = %q, want unchanged", task.Status)
	}
}

func TestGuardedTransitionTask_RejectsStaleObservedUpdatedAt(t *testing.T) {
	observed := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	task := &model.Task{ID: uuid.New(), Status: model.StatusInProgress, UpdatedAt: observed.Add(time.Second)}

	_, err := GuardedTransitionTask(task, TransitionRequest{
		Target:            model.StatusTestingReady,
		ObservedUpdatedAt: &observed,
		Evidence: Evidence{
			TaskID:           task.ID,
			Actor:            "orchestrator",
			Source:           "completion-reconciler",
			NormalizedReason: "worker_completed",
		},
	})
	if !errors.Is(err, ErrStaleTransition) {
		t.Fatalf("GuardedTransitionTask error = %v, want ErrStaleTransition", err)
	}
}
