package model

import "testing"

func TestParseTaskStatus(t *testing.T) {
	tests := []struct {
		input string
		want  TaskStatus
	}{
		{"backlog", StatusBacklog},
		{"planning", StatusPlanning},
		{"plan_review", StatusPlanReview},
		{"in_progress", StatusInProgress},
		{"testing_ready", StatusTestingReady},
		{"merging", StatusMerging},
		{"paused", StatusPaused},
		{"done", StatusDone},
		{"failed", StatusFailed},
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

func TestParseTaskStatus_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "nonexistent"},
		{"empty string", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTaskStatus(tc.input)
			if err == nil {
				t.Errorf("ParseTaskStatus(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestParseAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  AgentType
	}{
		{"orchestrator", AgentOrchestrator},
		{"planner", AgentPlanner},
		{"coder", AgentCoder},
		{"researcher", AgentResearcher},
		{"reviewer", AgentReviewer},
		{"fixer", AgentFixer},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseAgentType(tc.input)
			if err != nil {
				t.Fatalf("ParseAgentType(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseAgentType(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseAgentType_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "invalid_agent"},
		{"empty string", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentType(tc.input)
			if err == nil {
				t.Errorf("ParseAgentType(%q) expected error, got nil", tc.input)
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

func TestTaskStatusString(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   string
	}{
		{StatusBacklog, "backlog"},
		{StatusPlanning, "planning"},
		{StatusPlanReview, "plan_review"},
		{StatusInProgress, "in_progress"},
		{StatusDone, "done"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.status.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentTypeString(t *testing.T) {
	tests := []struct {
		agentType AgentType
		want      string
	}{
		{AgentOrchestrator, "orchestrator"},
		{AgentPlanner, "planner"},
		{AgentCoder, "coder"},
		{AgentResearcher, "researcher"},
		{AgentReviewer, "reviewer"},
		{AgentFixer, "fixer"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.agentType.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTaskCategory(t *testing.T) {
	tests := []struct {
		input string
		want  TaskCategory
	}{
		{"standard", CategoryStandard},
		{"quickfix", CategoryQuickFix},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseTaskCategory(tc.input)
			if err != nil {
				t.Fatalf("ParseTaskCategory(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseTaskCategory(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseTaskCategory_Error(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"invalid value", "bogus"},
		{"empty string", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTaskCategory(tc.input)
			if err == nil {
				t.Errorf("ParseTaskCategory(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestTaskCategoryString(t *testing.T) {
	tests := []struct {
		cat  TaskCategory
		want string
	}{
		{CategoryStandard, "standard"},
		{CategoryQuickFix, "quickfix"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.cat.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTaskCategory_IsQuickFix(t *testing.T) {
	tests := []struct {
		name string
		cat  TaskCategory
		want bool
	}{
		{"quickfix returns true", CategoryQuickFix, true},
		{"standard returns false", CategoryStandard, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cat.IsQuickFix(); got != tc.want {
				t.Errorf("IsQuickFix() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentStatusString(t *testing.T) {
	tests := []struct {
		status AgentStatus
		want   string
	}{
		{AgentIdle, "idle"},
		{AgentWorking, "working"},
		{AgentBlocked, "blocked"},
		{AgentDead, "dead"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.status.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
