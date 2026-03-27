package tui

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// TestDisplayStatus_NeedsClarificationTruncation verifies that needs_clarification
// is truncated to needs_clar for display purposes.
func TestDisplayStatus_NeedsClarificationTruncation(t *testing.T) {
	t.Run("needs_clarification maps to needs_clar", func(t *testing.T) {
		got := DisplayStatus(string(model.StatusNeedsClarification))
		want := "needs_clar"
		if got != want {
			t.Errorf("DisplayStatus(%q) = %q, want %q", string(model.StatusNeedsClarification), got, want)
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
			got := DisplayStatus(string(tc.status))
			if got != tc.want {
				t.Errorf("DisplayStatus(%q) = %q, want %q", string(tc.status), got, tc.want)
			}
		})
	}
}

// TestDisplayStatus_EmptyStringHandling verifies that an empty string passes through unchanged.
func TestDisplayStatus_EmptyStringHandling(t *testing.T) {
	result := DisplayStatus("")
	if result == "needs_clar" {
		t.Errorf("DisplayStatus(\"\") should not return truncated value")
	}
	if result != "" {
		t.Errorf("DisplayStatus(\"\") = %q, want %q", result, "")
	}
}

// TestDisplayStatus_InverseMapping verifies that only needs_clarification produces "needs_clar".
func TestDisplayStatus_InverseMapping(t *testing.T) {
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
		if DisplayStatus(string(status)) == "needs_clar" {
			needsClarCount++
			if status != model.StatusNeedsClarification {
				t.Errorf("DisplayStatus(%q) returned 'needs_clar' but expected it only from StatusNeedsClarification", string(status))
			}
		}
	}

	if needsClarCount != 1 {
		t.Errorf("Expected exactly 1 status to produce 'needs_clar', got %d", needsClarCount)
	}
}
