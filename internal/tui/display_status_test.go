package tui

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// TestDisplayStatus_NeedsClarificationTruncation verifies that needs_clarification
// is truncated to needs_clar for display purposes.
func TestDisplayStatus_NeedsClarificationTruncation(t *testing.T) {
	t.Run("needs_clarification maps to needs_clar", func(t *testing.T) {
		got := DisplayStatus(model.StatusNeedsClarification)
		want := "needs_clar"
		if got != want {
			t.Errorf("DisplayStatus(StatusNeedsClarification) = %q, want %q", got, want)
		}
	})
}

// TestDisplayStatus_AllStatusesPassThrough verifies that all statuses except
// needs_clarification display unchanged.
func TestDisplayStatus_AllStatusesPassThrough(t *testing.T) {
	tests := []struct {
		status model.TaskStatus
		want   string
	}{
		{model.StatusClassifying, "classifying"},
		{model.StatusBacklog, "backlog"},
		{model.StatusPlanning, "planning"},
		{model.StatusPlanReview, "plan_review"},
		{model.StatusTestWriting, "test_writing"},
		{model.StatusTestReview, "test_review"},
		{model.StatusInProgress, "in_progress"},
		{model.StatusTestingReady, "testing_ready"},
		{model.StatusMerging, "merging"},
		{model.StatusPaused, "paused"},
		{model.StatusDone, "done"},
		{model.StatusFailed, "failed"},
		{model.StatusRejected, "rejected"},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := DisplayStatus(tc.status)
			if got != tc.want {
				t.Errorf("DisplayStatus(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestDisplayStatus_ReturnsString verifies that DisplayStatus returns a string
// for all valid TaskStatus values.
func TestDisplayStatus_ReturnsString(t *testing.T) {
	tests := []struct {
		name   string
		status model.TaskStatus
	}{
		{"classifying", model.StatusClassifying},
		{"backlog", model.StatusBacklog},
		{"planning", model.StatusPlanning},
		{"needs_clarification", model.StatusNeedsClarification},
		{"plan_review", model.StatusPlanReview},
		{"test_writing", model.StatusTestWriting},
		{"test_review", model.StatusTestReview},
		{"in_progress", model.StatusInProgress},
		{"testing_ready", model.StatusTestingReady},
		{"merging", model.StatusMerging},
		{"paused", model.StatusPaused},
		{"done", model.StatusDone},
		{"failed", model.StatusFailed},
		{"rejected", model.StatusRejected},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := DisplayStatus(tc.status)
			if result == "" {
				t.Errorf("DisplayStatus(%q) returned empty string", tc.status)
			}
		})
	}
}

// TestDisplayStatus_NeedsClarificationNotFull verifies that needs_clarification
// is NOT displayed as the full string, only as the truncated version.
func TestDisplayStatus_NeedsClarificationNotFull(t *testing.T) {
	got := DisplayStatus(model.StatusNeedsClarification)
	if got == "needs_clarification" {
		t.Errorf("DisplayStatus(StatusNeedsClarification) should not return full string %q, got %q", "needs_clarification", got)
	}
	if got != "needs_clar" {
		t.Errorf("DisplayStatus(StatusNeedsClarification) returned unexpected value %q, want %q", got, "needs_clar")
	}
}

// TestDisplayStatus_SpecialCaseOnly verifies that truncation only applies to
// needs_clarification, not to other similar names.
func TestDisplayStatus_SpecialCaseOnly(t *testing.T) {
	// Verify plan_review is NOT truncated
	if got := DisplayStatus(model.StatusPlanReview); got != "plan_review" {
		t.Errorf("DisplayStatus(StatusPlanReview) = %q, want %q (should not be truncated)", got, "plan_review")
	}

	// Verify test_review is NOT truncated
	if got := DisplayStatus(model.StatusTestReview); got != "test_review" {
		t.Errorf("DisplayStatus(StatusTestReview) = %q, want %q (should not be truncated)", got, "test_review")
	}

	// Verify test_writing is NOT truncated
	if got := DisplayStatus(model.StatusTestWriting); got != "test_writing" {
		t.Errorf("DisplayStatus(StatusTestWriting) = %q, want %q (should not be truncated)", got, "test_writing")
	}

	// Verify testing_ready is NOT truncated
	if got := DisplayStatus(model.StatusTestingReady); got != "testing_ready" {
		t.Errorf("DisplayStatus(StatusTestingReady) = %q, want %q (should not be truncated)", got, "testing_ready")
	}
}

// TestDisplayStatus_MultipleCallsSameResult verifies that DisplayStatus is
// deterministic and returns the same result for multiple calls with the same input.
func TestDisplayStatus_MultipleCallsSameResult(t *testing.T) {
	statuses := []model.TaskStatus{
		model.StatusNeedsClarification,
		model.StatusInProgress,
		model.StatusBacklog,
		model.StatusDone,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			first := DisplayStatus(status)
			second := DisplayStatus(status)
			third := DisplayStatus(status)

			if first != second {
				t.Errorf("DisplayStatus(%q) returned different results: %q, %q", status, first, second)
			}
			if second != third {
				t.Errorf("DisplayStatus(%q) returned different results: %q, %q", status, second, third)
			}
		})
	}
}

// TestDisplayStatus_TruncationLength verifies the exact length of the truncated
// needs_clarification status.
func TestDisplayStatus_TruncationLength(t *testing.T) {
	got := DisplayStatus(model.StatusNeedsClarification)
	expectedLen := len("needs_clar")
	if len(got) != expectedLen {
		t.Errorf("DisplayStatus(StatusNeedsClarification) length = %d, want %d", len(got), expectedLen)
	}
	if got != "needs_clar" {
		t.Errorf("DisplayStatus(StatusNeedsClarification) = %q, want %q", got, "needs_clar")
	}
}

// TestDisplayStatus_EmptyInputHandling tests behavior with edge cases (empty status).
// This verifies the function handles invalid/empty input gracefully.
func TestDisplayStatus_EmptyInputHandling(t *testing.T) {
	emptyStatus := model.TaskStatus("")
	result := DisplayStatus(emptyStatus)
	// An empty status should either return empty string or some default value
	// The implementation should not panic
	if result == "needs_clar" {
		t.Errorf("DisplayStatus with empty input should not return truncated value")
	}
}

// TestDisplayStatus_InverseMapping verifies that we can identify which input
// produces "needs_clar" output (only needs_clarification should produce it).
func TestDisplayStatus_InverseMapping(t *testing.T) {
	// Check that only needs_clarification produces "needs_clar"
	allStatuses := []model.TaskStatus{
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
	}

	needsClarCount := 0
	for _, status := range allStatuses {
		if DisplayStatus(status) == "needs_clar" {
			needsClarCount++
			if status != model.StatusNeedsClarification {
				t.Errorf("DisplayStatus(%q) returned 'needs_clar' but expected it only from StatusNeedsClarification", status)
			}
		}
	}

	if needsClarCount != 1 {
		t.Errorf("Expected exactly 1 status to produce 'needs_clar', but got %d", needsClarCount)
	}
}
