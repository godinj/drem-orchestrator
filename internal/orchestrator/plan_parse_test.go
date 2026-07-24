package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestParsePlan(t *testing.T) {
	tests := []struct {
		name             string
		plan             model.JSONField
		wantSubtaskCount int
		wantErr          bool
		checkSubtasks    func(t *testing.T, result *parsePlanResult)
	}{
		{
			name: "plan with phase and tests_for fields",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Write unit tests",
						"description": "Tests for the auth module",
						"agent_type":  "coder",
						"phase":       "test",
						"tests_for":   []any{float64(1)},
						"files":       []any{"auth_test.go"},
					},
					map[string]any{
						"title":       "Implement auth module",
						"description": "Core auth logic",
						"agent_type":  "coder",
						"phase":       "implementation",
						"files":       []any{"auth.go"},
					},
				},
			},
			wantSubtaskCount: 2,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				if result.Subtasks[0].Phase != "test" {
					t.Errorf("subtask[0].Phase = %q, want %q", result.Subtasks[0].Phase, "test")
				}
				if len(result.Subtasks[0].TestsFor) != 1 || result.Subtasks[0].TestsFor[0] != 1 {
					t.Errorf("subtask[0].TestsFor = %v, want [1]", result.Subtasks[0].TestsFor)
				}
				if result.Subtasks[1].Phase != "implementation" {
					t.Errorf("subtask[1].Phase = %q, want %q", result.Subtasks[1].Phase, "implementation")
				}
				if len(result.Subtasks[1].TestsFor) != 0 {
					t.Errorf("subtask[1].TestsFor = %v, want empty", result.Subtasks[1].TestsFor)
				}
			},
		},
		{
			name: "plan with tdd_exceptions",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Write tests",
						"description": "Test suite",
						"agent_type":  "coder",
						"phase":       "test",
						"files":       []any{"test.go"},
					},
					map[string]any{
						"title":       "Implement feature",
						"description": "Feature code",
						"agent_type":  "coder",
						"phase":       "implementation",
						"files":       []any{"feature.go"},
					},
					map[string]any{
						"title":       "Wire up integration",
						"description": "Glue code",
						"agent_type":  "coder",
						"phase":       "integration",
						"files":       []any{"main.go"},
					},
				},
				"tdd_exceptions": []any{
					map[string]any{
						"subtask_index": float64(2),
						"reason":        "Integration wiring only",
					},
				},
			},
			wantSubtaskCount: 3,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				if len(result.TDDExceptions) != 1 {
					t.Fatalf("TDDExceptions count = %d, want 1", len(result.TDDExceptions))
				}
				if result.TDDExceptions[0].SubtaskIndex != 2 {
					t.Errorf("TDDExceptions[0].SubtaskIndex = %d, want 2", result.TDDExceptions[0].SubtaskIndex)
				}
				if result.TDDExceptions[0].Reason != "Integration wiring only" {
					t.Errorf("TDDExceptions[0].Reason = %q, want %q", result.TDDExceptions[0].Reason, "Integration wiring only")
				}
			},
		},
		{
			name: "old format without tdd_exceptions is backward compatible",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Build feature",
						"description": "Feature code",
						"agent_type":  "coder",
						"files":       []any{"feature.go"},
					},
				},
			},
			wantSubtaskCount: 1,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				if len(result.TDDExceptions) != 0 {
					t.Errorf("TDDExceptions = %v, want empty", result.TDDExceptions)
				}
			},
		},
		{
			name: "old format without phase fields is backward compatible",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Add models",
						"description": "Data models",
						"agent_type":  "coder",
						"files":       []any{"models.go"},
					},
					map[string]any{
						"title":       "Add handlers",
						"description": "HTTP handlers",
						"agent_type":  "coder",
						"files":       []any{"handlers.go"},
					},
				},
			},
			wantSubtaskCount: 2,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				for i, s := range result.Subtasks {
					if s.Phase != "" {
						t.Errorf("subtask[%d].Phase = %q, want empty", i, s.Phase)
					}
					if len(s.TestsFor) != 0 {
						t.Errorf("subtask[%d].TestsFor = %v, want empty", i, s.TestsFor)
					}
				}
			},
		},
		{
			name: "tests_for on test subtask is preserved",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Test auth",
						"description": "Auth tests",
						"agent_type":  "coder",
						"phase":       "test",
						"tests_for":   []any{float64(1)},
						"files":       []any{"auth_test.go"},
					},
					map[string]any{
						"title":       "Impl auth",
						"description": "Auth implementation",
						"agent_type":  "coder",
						"phase":       "implementation",
						"files":       []any{"auth.go"},
					},
				},
			},
			wantSubtaskCount: 2,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				if len(result.Subtasks[0].TestsFor) != 1 {
					t.Fatalf("subtask[0].TestsFor length = %d, want 1", len(result.Subtasks[0].TestsFor))
				}
				if result.Subtasks[0].TestsFor[0] != 1 {
					t.Errorf("subtask[0].TestsFor[0] = %d, want 1", result.Subtasks[0].TestsFor[0])
				}
			},
		},
		{
			name:             "nil plan returns error",
			plan:             nil,
			wantSubtaskCount: 0,
			wantErr:          true,
		},
		{
			name:             "plan without subtasks key returns error",
			plan:             model.JSONField{"other": "data"},
			wantSubtaskCount: 0,
			wantErr:          true,
		},
		{
			name: "multiple tdd_exceptions",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Tests",
						"description": "Tests",
						"agent_type":  "coder",
						"phase":       "test",
						"files":       []any{"test.go"},
					},
					map[string]any{
						"title":       "Config setup",
						"description": "Config",
						"agent_type":  "coder",
						"phase":       "implementation",
						"files":       []any{"config.go"},
					},
					map[string]any{
						"title":       "Wire up",
						"description": "Wiring",
						"agent_type":  "coder",
						"phase":       "integration",
						"files":       []any{"main.go"},
					},
				},
				"tdd_exceptions": []any{
					map[string]any{
						"subtask_index": float64(1),
						"reason":        "Config-only changes",
					},
					map[string]any{
						"subtask_index": float64(2),
						"reason":        "Integration wiring only",
					},
				},
			},
			wantSubtaskCount: 3,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				if len(result.TDDExceptions) != 2 {
					t.Fatalf("TDDExceptions count = %d, want 2", len(result.TDDExceptions))
				}
				if result.TDDExceptions[0].SubtaskIndex != 1 {
					t.Errorf("TDDExceptions[0].SubtaskIndex = %d, want 1", result.TDDExceptions[0].SubtaskIndex)
				}
				if result.TDDExceptions[1].SubtaskIndex != 2 {
					t.Errorf("TDDExceptions[1].SubtaskIndex = %d, want 2", result.TDDExceptions[1].SubtaskIndex)
				}
			},
		},
		{
			name: "agent_type defaults to coder when empty",
			plan: model.JSONField{
				"subtasks": []any{
					map[string]any{
						"title":       "Do thing",
						"description": "Thing",
						"files":       []any{"thing.go"},
					},
				},
			},
			wantSubtaskCount: 1,
			wantErr:          false,
			checkSubtasks: func(t *testing.T, result *parsePlanResult) {
				t.Helper()
				if result.Subtasks[0].AgentType != "coder" {
					t.Errorf("AgentType = %q, want %q", result.Subtasks[0].AgentType, "coder")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parsePlan(tt.plan)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(result.Subtasks) != tt.wantSubtaskCount {
				t.Fatalf("subtask count = %d, want %d", len(result.Subtasks), tt.wantSubtaskCount)
			}

			if tt.checkSubtasks != nil {
				tt.checkSubtasks(t, result)
			}
		})
	}
}

func TestParsePlanLegacyIntegrationWritableScopeFallsBackToFiles(t *testing.T) {
	parsed, err := parsePlan(model.JSONField{"subtasks": []any{map[string]any{
		"title": "assemble", "description": "wire", "phase": "integration", "files": []any{"src/a.cpp", "cmake/a.cmake"},
	}}})
	require.NoError(t, err)
	require.Nil(t, parsed.Subtasks[0].WritableFiles)
	require.Equal(t, []string{"src/a.cpp", "cmake/a.cmake"}, writablePlanFiles(parsed.Subtasks[0]))
}
