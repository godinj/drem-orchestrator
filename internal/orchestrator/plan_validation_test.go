package orchestrator

import (
	"strings"
	"testing"
)

func TestValidatePlan(t *testing.T) {
	tests := []struct {
		name         string
		subtasks     []planEntry
		exceptions   []tddException
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
			name: "test subtask without full dependencies warns (legacy)",
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
			name: "test subtask with full dependencies no warning (legacy)",
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
			result := ValidatePlan(tt.subtasks, tt.exceptions)

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

func TestValidatePlanTDD(t *testing.T) {
	tests := []struct {
		name            string
		subtasks        []planEntry
		exceptions      []tddException
		wantValid       bool
		wantWarningSubs []string // substrings to match in warnings
		wantErrorSubs   []string // substrings to match in errors
	}{
		{
			name: "no test subtasks no exceptions errors",
			subtasks: []planEntry{
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
				{Title: "Impl B", Phase: "implementation", Files: []string{"b.go"}},
			},
			wantValid:     false,
			wantErrorSubs: []string{"no test subtasks"},
		},
		{
			name: "valid 1:1 mapping",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go", "a.go"}, TestsFor: []int{2}, Dependencies: []int{}},
				{Title: "Test B", Phase: "test", Files: []string{"b_test.go", "b.go"}, TestsFor: []int{3}, Dependencies: []int{}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}, Dependencies: []int{0}},
				{Title: "Impl B", Phase: "implementation", Files: []string{"b.go"}, Dependencies: []int{1}},
			},
			wantValid:       true,
			wantWarningSubs: nil,
			wantErrorSubs:   nil,
		},
		{
			name: "tests_for references 2 impl subtasks errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1, 2}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
				{Title: "Impl B", Phase: "implementation", Files: []string{"b.go"}},
			},
			wantValid:     false,
			wantErrorSubs: []string{"must reference exactly one"},
		},
		{
			name: "two tests reference same impl errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{2}},
				{Title: "Test B", Phase: "test", Files: []string{"b_test.go"}, TestsFor: []int{2}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			wantValid:       false,
			wantWarningSubs: []string{"no file overlap"},
			wantErrorSubs:   []string{"Duplicate test coverage"},
		},
		{
			name: "tests_for out of bounds errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{99}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			wantValid:     false,
			wantErrorSubs: []string{"out-of-bounds"},
		},
		{
			name: "tests_for references non-implementation subtask errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1}},
				{Title: "Setup", Phase: "setup", Files: []string{"setup.go"}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			wantValid:     false,
			wantErrorSubs: []string{"non-implementation"},
		},
		{
			name: "test depends on impl subtask errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go", "a.go"}, TestsFor: []int{1}, Dependencies: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			wantValid:     false,
			wantErrorSubs: []string{"tests must run before implementation"},
		},
		{
			name: "more than 50% exceptions warns",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go", "a.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
				{Title: "Impl B", Phase: "implementation", Files: []string{"b.go"}},
				{Title: "Impl C", Phase: "implementation", Files: []string{"c.go"}},
			},
			exceptions: []tddException{
				{SubtaskIndex: 2, Reason: "config only"},
				{SubtaskIndex: 3, Reason: "docs only"},
			},
			wantValid:       true,
			wantWarningSubs: []string{"More than 50%"},
			wantErrorSubs:   nil,
		},
		{
			name: "exception references test-phase subtask errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			exceptions: []tddException{
				{SubtaskIndex: 0, Reason: "wrong"},
			},
			wantValid:       false,
			wantWarningSubs: []string{"no file overlap"},
			wantErrorSubs:   []string{"references test-phase"},
		},
		{
			name: "exception references out of bounds errors",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			exceptions: []tddException{
				{SubtaskIndex: 99, Reason: "nonexistent"},
			},
			wantValid:       false,
			wantWarningSubs: []string{"no file overlap"},
			wantErrorSubs:   []string{"out-of-bounds subtask index 99"},
		},
		{
			name: "old format plan no phases skips TDD checks",
			subtasks: []planEntry{
				{Title: "Implement feature A", Files: []string{"a.go"}},
				{Title: "Implement feature B", Files: []string{"b.go"}},
				{Title: "Add tests", Files: []string{"a_test.go", "b_test.go"}, Dependencies: []int{0, 1}},
			},
			wantValid:       true,
			wantWarningSubs: nil,
			wantErrorSubs:   nil,
		},
		{
			name: "file overlap warning between test and impl",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			wantValid:       true,
			wantWarningSubs: []string{"no file overlap"},
			wantErrorSubs:   nil,
		},
		{
			name: "no file overlap warning when files overlap",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go", "a.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}, Dependencies: []int{0}},
			},
			wantValid:       true,
			wantWarningSubs: nil,
			wantErrorSubs:   nil,
		},
		{
			name: "impl subtask with exception but no test is valid",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go", "a.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}, Dependencies: []int{0}},
				{Title: "Impl B config", Phase: "implementation", Files: []string{"config.go"}},
			},
			exceptions: []tddException{
				{SubtaskIndex: 2, Reason: "config only, no logic"},
			},
			wantValid:       true,
			wantWarningSubs: nil,
			wantErrorSubs:   nil,
		},
		{
			name: "all impl exempted skips no-test-subtasks error",
			subtasks: []planEntry{
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			exceptions: []tddException{
				{SubtaskIndex: 0, Reason: "trivial change"},
			},
			wantValid:       true,
			wantWarningSubs: []string{"More than 50%"},
			wantErrorSubs:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidatePlan(tt.subtasks, tt.exceptions)

			if result.Valid != tt.wantValid {
				t.Errorf("Valid = %v, want %v\nerrors: %v\nwarnings: %v",
					result.Valid, tt.wantValid, result.Errors, result.Warnings)
			}

			// Check error substrings.
			if len(tt.wantErrorSubs) > 0 {
				for _, sub := range tt.wantErrorSubs {
					found := false
					for _, e := range result.Errors {
						if strings.Contains(e, sub) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error containing %q, got errors: %v", sub, result.Errors)
					}
				}
			} else if tt.wantErrorSubs == nil && len(result.Errors) > 0 {
				t.Errorf("expected no errors, got: %v", result.Errors)
			}

			// Check warning substrings.
			if len(tt.wantWarningSubs) > 0 {
				for _, sub := range tt.wantWarningSubs {
					found := false
					for _, w := range result.Warnings {
						if strings.Contains(w, sub) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected warning containing %q, got warnings: %v", sub, result.Warnings)
					}
				}
			} else if tt.wantWarningSubs == nil && len(result.Warnings) > 0 {
				t.Errorf("expected no warnings, got: %v", result.Warnings)
			}
		})
	}
}

func TestMergeTDDDependencies(t *testing.T) {
	tests := []struct {
		name         string
		subtasks     []planEntry
		checkIdx     int   // index of subtask to check
		wantDeps     []int // expected dependencies
		wantOriginal []int // verify original is unchanged
	}{
		{
			name: "test subtask adds dependency to impl",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			checkIdx:     1,
			wantDeps:     []int{0},
			wantOriginal: nil,
		},
		{
			name: "existing dependency not duplicated",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{1}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}, Dependencies: []int{0}},
			},
			checkIdx:     1,
			wantDeps:     []int{0},
			wantOriginal: []int{0},
		},
		{
			name: "preserves existing explicit dependencies",
			subtasks: []planEntry{
				{Title: "Setup", Phase: "setup", Files: []string{"setup.go"}},
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{2}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}, Dependencies: []int{0}},
			},
			checkIdx:     2,
			wantDeps:     []int{0, 1},
			wantOriginal: []int{0},
		},
		{
			name: "no-op for subtasks without tests_for",
			subtasks: []planEntry{
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
				{Title: "Impl B", Phase: "implementation", Files: []string{"b.go"}},
			},
			checkIdx:     0,
			wantDeps:     nil,
			wantOriginal: nil,
		},
		{
			name: "out of bounds tests_for ignored",
			subtasks: []planEntry{
				{Title: "Test A", Phase: "test", Files: []string{"a_test.go"}, TestsFor: []int{99}},
				{Title: "Impl A", Phase: "implementation", Files: []string{"a.go"}},
			},
			checkIdx:     1,
			wantDeps:     nil,
			wantOriginal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeTDDDependencies(tt.subtasks)

			// Verify the merged result has expected dependencies.
			gotDeps := result[tt.checkIdx].Dependencies
			if !intSliceEqual(gotDeps, tt.wantDeps) {
				t.Errorf("merged deps for subtask %d = %v, want %v", tt.checkIdx, gotDeps, tt.wantDeps)
			}

			// Verify original is not mutated.
			origDeps := tt.subtasks[tt.checkIdx].Dependencies
			if !intSliceEqual(origDeps, tt.wantOriginal) {
				t.Errorf("original deps mutated for subtask %d: got %v, want %v", tt.checkIdx, origDeps, tt.wantOriginal)
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
		name  string
		entry planEntry
		want  bool
	}{
		{"phase test", planEntry{Title: "Anything", Phase: "test"}, true},
		{"phase implementation", planEntry{Title: "Test something", Phase: "implementation"}, false},
		{"is_test flag", planEntry{Title: "Something", IsTest: true}, true},
		{"title: Add tests for feature", planEntry{Title: "Add tests for feature"}, true},
		{"title: Test integration", planEntry{Title: "Test integration"}, true},
		{"title: Unit Testing", planEntry{Title: "Unit Testing"}, true},
		{"title: Implement feature", planEntry{Title: "Implement feature"}, false},
		{"title: TESTING SUITE", planEntry{Title: "TESTING SUITE"}, true},
		{"title: Build the system", planEntry{Title: "Build the system"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTestSubtask(tt.entry)
			if got != tt.want {
				t.Errorf("isTestSubtask(%+v) = %v, want %v", tt.entry, got, tt.want)
			}
		})
	}
}

// intSliceEqual checks if two int slices have the same elements (order matters).
func intSliceEqual(a, b []int) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
