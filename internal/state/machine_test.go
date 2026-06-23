package state

import (
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

		// REJECTED is terminal
		{"rejected to backlog is INVALID", model.StatusRejected, model.StatusBacklog, true},
		{"rejected to planning is INVALID", model.StatusRejected, model.StatusPlanning, true},
		{"rejected to in_progress is INVALID", model.StatusRejected, model.StatusInProgress, true},
		{"rejected to test_writing is INVALID", model.StatusRejected, model.StatusTestWriting, true},

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
		{"in_progress to failed", model.StatusInProgress, model.StatusFailed, false},
		{"in_progress to paused", model.StatusInProgress, model.StatusPaused, false},
		{"testing_ready to merging", model.StatusTestingReady, model.StatusMerging, false},
		{"testing_ready to in_progress", model.StatusTestingReady, model.StatusInProgress, false},
		{"testing_ready to planning", model.StatusTestingReady, model.StatusPlanning, false},
		{"merging to done", model.StatusMerging, model.StatusDone, false},
		{"merging to failed", model.StatusMerging, model.StatusFailed, false},
		{"done is terminal", model.StatusDone, model.StatusBacklog, true},
		{"failed to backlog", model.StatusFailed, model.StatusBacklog, false},
		{"failed to in_progress", model.StatusFailed, model.StatusInProgress, false},
		{"failed to done is INVALID", model.StatusFailed, model.StatusDone, true},
		{"cancelled is terminal", model.StatusCancelled, model.StatusBacklog, true},
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

func TestNeedsClarification_IsHumanGate(t *testing.T) {
	if !model.StatusNeedsClarification.IsHumanGate() {
		t.Error("StatusNeedsClarification.IsHumanGate() = false, want true")
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
