package model

import "testing"

func TestParseTaskStatus_NewStatuses(t *testing.T) {
	tests := []struct {
		input string
		want  TaskStatus
	}{
		{"test_writing", StatusTestWriting},
		{"test_review", StatusTestReview},
		{"rejected", StatusRejected},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseTaskStatus(tc.input)
			if err != nil {
				t.Fatalf("ParseTaskStatus(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseTaskStatus(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsActionable(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{StatusBacklog, true},
		{StatusPlanning, true},
		{StatusInProgress, true},
		{StatusTestWriting, true},
		{StatusMerging, true},
		{StatusTestReview, false},
		{StatusRejected, false},
		{StatusPlanReview, false},
		{StatusTestingReady, false},
		{StatusDone, false},
		{StatusFailed, false},
		{StatusPaused, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := tc.status.IsActionable()
			if got != tc.want {
				t.Errorf("IsActionable(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestIsHumanGate(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{StatusPlanReview, true},
		{StatusTestReview, true},
		{StatusTestingReady, true},
		{StatusBacklog, false},
		{StatusPlanning, false},
		{StatusTestWriting, false},
		{StatusInProgress, false},
		{StatusMerging, false},
		{StatusDone, false},
		{StatusFailed, false},
		{StatusPaused, false},
		{StatusRejected, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := tc.status.IsHumanGate()
			if got != tc.want {
				t.Errorf("IsHumanGate(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}
