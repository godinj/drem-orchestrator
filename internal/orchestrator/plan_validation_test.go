package orchestrator

import (
	"testing"
)

func TestValidatePlan(t *testing.T) {
	tests := []struct {
		name         string
		subtasks     []planEntry
		wantValid    bool
		wantWarnings []string
		wantErrors   []string
	}{
		{
			name: "valid plan with no overlap",
			subtasks: []planEntry{
				{Title: "Add models", Files: []string{"models.go"}},
				{Title: "Add handlers", Files: []string{"handlers.go"}},
				{Title: "Integration", Files: []string{"main.go"}, Dependencies: []int{0, 1}},
			},
			wantValid:    true,
			wantWarnings: nil,
			wantErrors:   nil,
		},
		{
			name: "file overlap without dependency warns",
			subtasks: []planEntry{
				{Title: "Feature A", Files: []string{"shared.go", "a.go"}},
				{Title: "Feature B", Files: []string{"shared.go", "b.go"}},
			},
			wantValid: true,
			wantWarnings: []string{
				"Subtasks 0 and 1 overlap on [shared.go] but have no dependency — they will be serialized",
			},
			wantErrors: nil,
		},
		{
			name: "file overlap with dependency does not warn",
			subtasks: []planEntry{
				{Title: "Feature A", Files: []string{"shared.go", "a.go"}},
				{Title: "Feature B", Files: []string{"shared.go", "b.go"}, Dependencies: []int{0}},
			},
			wantValid:    true,
			wantWarnings: nil,
			wantErrors:   nil,
		},
		{
			name: "empty files warns about scheduling",
			subtasks: []planEntry{
				{Title: "Subtask 1", Files: []string{"a.go"}},
				{Title: "Subtask 2"},
				{Title: "Subtask 3"},
			},
			wantValid: true,
			wantWarnings: []string{
				"2 subtask(s) have no files listed — scheduling will be degraded",
			},
			wantErrors: nil,
		},
		{
			name: "subtask count exceeds 8 warns",
			subtasks: []planEntry{
				{Title: "S1", Files: []string{"1.go"}},
				{Title: "S2", Files: []string{"2.go"}},
				{Title: "S3", Files: []string{"3.go"}},
				{Title: "S4", Files: []string{"4.go"}},
				{Title: "S5", Files: []string{"5.go"}},
				{Title: "S6", Files: []string{"6.go"}},
				{Title: "S7", Files: []string{"7.go"}},
				{Title: "S8", Files: []string{"8.go"}},
				{Title: "S9", Files: []string{"9.go"}},
			},
			wantValid: true,
			wantWarnings: []string{
				"Plan has 9 subtasks (recommended max: 8)",
			},
			wantErrors: nil,
		},
		{
			name: "dependency cycle is an error",
			subtasks: []planEntry{
				{Title: "A", Files: []string{"a.go"}, Dependencies: []int{1}},
				{Title: "B", Files: []string{"b.go"}, Dependencies: []int{2}},
				{Title: "C", Files: []string{"c.go"}, Dependencies: []int{0}},
			},
			wantValid:    false,
			wantWarnings: nil,
			wantErrors: []string{
				"Dependency cycle detected in subtask dependencies",
			},
		},
		{
			name: "test subtask without full dependencies warns",
			subtasks: []planEntry{
				{Title: "Implement feature A", Files: []string{"a.go"}},
				{Title: "Implement feature B", Files: []string{"b.go"}},
				{Title: "Add tests", Files: []string{"a_test.go", "b_test.go"}, Dependencies: []int{0}},
			},
			wantValid: true,
			wantWarnings: []string{
				"Test subtask 'Add tests' does not depend on all implementation subtasks",
			},
			wantErrors: nil,
		},
		{
			name: "test subtask with full dependencies no warning",
			subtasks: []planEntry{
				{Title: "Implement feature A", Files: []string{"a.go"}},
				{Title: "Implement feature B", Files: []string{"b.go"}},
				{Title: "Add tests", Files: []string{"a_test.go", "b_test.go"}, Dependencies: []int{0, 1}},
			},
			wantValid:    true,
			wantWarnings: nil,
			wantErrors:   nil,
		},
		{
			name: "estimated_files used when files empty",
			subtasks: []planEntry{
				{Title: "Subtask 1", EstimatedFiles: []string{"shared.go", "a.go"}},
				{Title: "Subtask 2", EstimatedFiles: []string{"shared.go", "b.go"}},
			},
			wantValid: true,
			wantWarnings: []string{
				"Subtasks 0 and 1 overlap on [shared.go] but have no dependency — they will be serialized",
			},
			wantErrors: nil,
		},
		{
			name: "self-referencing dependency cycle",
			subtasks: []planEntry{
				{Title: "A", Files: []string{"a.go"}, Dependencies: []int{0}},
			},
			wantValid:    false,
			wantWarnings: nil,
			wantErrors: []string{
				"Dependency cycle detected in subtask dependencies",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidatePlan(tt.subtasks)

			if result.Valid != tt.wantValid {
				t.Errorf("Valid = %v, want %v", result.Valid, tt.wantValid)
			}

			if len(result.Warnings) != len(tt.wantWarnings) {
				t.Errorf("got %d warnings, want %d\ngot:  %v\nwant: %v",
					len(result.Warnings), len(tt.wantWarnings), result.Warnings, tt.wantWarnings)
			} else {
				for i, w := range result.Warnings {
					if w != tt.wantWarnings[i] {
						t.Errorf("warning[%d] = %q, want %q", i, w, tt.wantWarnings[i])
					}
				}
			}

			if len(result.Errors) != len(tt.wantErrors) {
				t.Errorf("got %d errors, want %d\ngot:  %v\nwant: %v",
					len(result.Errors), len(tt.wantErrors), result.Errors, tt.wantErrors)
			} else {
				for i, e := range result.Errors {
					if e != tt.wantErrors[i] {
						t.Errorf("error[%d] = %q, want %q", i, e, tt.wantErrors[i])
					}
				}
			}
		})
	}
}

func TestHasCycle(t *testing.T) {
	tests := []struct {
		name     string
		subtasks []planEntry
		want     bool
	}{
		{
			name: "no dependencies",
			subtasks: []planEntry{
				{Title: "A"},
				{Title: "B"},
			},
			want: false,
		},
		{
			name: "linear chain no cycle",
			subtasks: []planEntry{
				{Title: "A"},
				{Title: "B", Dependencies: []int{0}},
				{Title: "C", Dependencies: []int{1}},
			},
			want: false,
		},
		{
			name: "simple cycle",
			subtasks: []planEntry{
				{Title: "A", Dependencies: []int{1}},
				{Title: "B", Dependencies: []int{0}},
			},
			want: true,
		},
		{
			name: "three-node cycle",
			subtasks: []planEntry{
				{Title: "A", Dependencies: []int{2}},
				{Title: "B", Dependencies: []int{0}},
				{Title: "C", Dependencies: []int{1}},
			},
			want: true,
		},
		{
			name: "diamond no cycle",
			subtasks: []planEntry{
				{Title: "A"},
				{Title: "B", Dependencies: []int{0}},
				{Title: "C", Dependencies: []int{0}},
				{Title: "D", Dependencies: []int{1, 2}},
			},
			want: false,
		},
		{
			name: "out of range dependency ignored",
			subtasks: []planEntry{
				{Title: "A", Dependencies: []int{99}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCycle(tt.subtasks)
			if got != tt.want {
				t.Errorf("hasCycle() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTestSubtask(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Add tests for feature", true},
		{"Test integration", true},
		{"Unit Testing", true},
		{"Implement feature", false},
		{"TESTING SUITE", true},
		{"Build the system", false},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := isTestSubtask(planEntry{Title: tt.title})
			if got != tt.want {
				t.Errorf("isTestSubtask(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}
