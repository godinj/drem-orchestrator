package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/constraints"
)

func TestValidatePlanConstraints(t *testing.T) {
	tests := []struct {
		name         string
		subtasks     []planEntry
		cfg          *constraints.Config
		worktreeRoot string // set per-test when needed
		wantValid    bool
		wantWarnings int
		wantContains []string // substrings expected in warnings
	}{
		{
			name: "nil config returns empty result",
			subtasks: []planEntry{
				{Title: "Add feature", EstimatedFiles: []string{"internal/foo.go"}},
			},
			cfg:          nil,
			wantValid:    true,
			wantWarnings: 0,
		},
		{
			name: "subtask targets shrink-only file (max_lines)",
			subtasks: []planEntry{
				{Title: "Refactor orchestrator", EstimatedFiles: []string{"internal/orchestrator/orchestrator.go"}},
			},
			cfg: &constraints.Config{
				MaxLines: []constraints.MaxLinesConstraint{
					{
						Name:  "god-file-prevention",
						Glob:  "*.go",
						Limit: 500,
						Exceptions: []constraints.LinesException{
							{
								Path:          "internal/orchestrator/orchestrator.go",
								Rule:          "shrink-only",
								BaselineLines: 2800,
							},
						},
					},
				},
			},
			wantValid:    true,
			wantWarnings: 1,
			wantContains: []string{
				"shrink-only",
				"baseline: 2800 lines",
				"internal/orchestrator/orchestrator.go",
				`Subtask 0`,
			},
		},
		{
			name: "subtask targets shrink-only file (max_matches)",
			subtasks: []planEntry{
				{Title: "Clean up agent", EstimatedFiles: []string{"internal/agent/runner.go"}},
			},
			cfg: &constraints.Config{
				MaxMatches: []constraints.MaxMatchesConstraint{
					{
						Name:    "switch-complexity",
						Glob:    "*.go",
						Pattern: `case\s+`,
						Limit:   20,
						Exceptions: []constraints.MatchesException{
							{
								Path:          "internal/agent/runner.go",
								Rule:          "shrink-only",
								BaselineCount: 35,
							},
						},
					},
				},
			},
			wantValid:    true,
			wantWarnings: 1,
			wantContains: []string{
				"shrink-only",
				"baseline: 35 matches",
				`"switch-complexity"`,
				"internal/agent/runner.go",
			},
		},
		{
			name: "subtask targets unconstrained file",
			subtasks: []planEntry{
				{Title: "Add handler", EstimatedFiles: []string{"internal/api/handler.go"}},
			},
			cfg: &constraints.Config{
				MaxLines: []constraints.MaxLinesConstraint{
					{
						Name:  "god-file-prevention",
						Glob:  "*.go",
						Limit: 500,
						Exceptions: []constraints.LinesException{
							{
								Path:          "internal/orchestrator/orchestrator.go",
								Rule:          "shrink-only",
								BaselineLines: 2800,
							},
						},
					},
				},
			},
			wantValid:    true,
			wantWarnings: 0,
		},
		{
			name: "multiple subtasks mixed hits",
			subtasks: []planEntry{
				{Title: "Safe change", EstimatedFiles: []string{"internal/api/handler.go"}},
				{Title: "Risky change", EstimatedFiles: []string{"internal/orchestrator/orchestrator.go"}},
			},
			cfg: &constraints.Config{
				MaxLines: []constraints.MaxLinesConstraint{
					{
						Name:  "god-file-prevention",
						Glob:  "*.go",
						Limit: 500,
						Exceptions: []constraints.LinesException{
							{
								Path:          "internal/orchestrator/orchestrator.go",
								Rule:          "shrink-only",
								BaselineLines: 2800,
							},
						},
					},
				},
			},
			wantValid:    true,
			wantWarnings: 1,
			wantContains: []string{
				`Subtask 1`,
				"Risky change",
			},
		},
		{
			name: "non-shrink-only exception does not warn",
			subtasks: []planEntry{
				{Title: "Change file", EstimatedFiles: []string{"internal/big.go"}},
			},
			cfg: &constraints.Config{
				MaxLines: []constraints.MaxLinesConstraint{
					{
						Name:  "limit",
						Glob:  "*.go",
						Limit: 500,
						Exceptions: []constraints.LinesException{
							{
								Path:          "internal/big.go",
								Rule:          "monitoring",
								BaselineLines: 600,
							},
						},
					},
				},
			},
			wantValid:    true,
			wantWarnings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidatePlanConstraints(tt.subtasks, tt.cfg, tt.worktreeRoot)

			if result.Valid != tt.wantValid {
				t.Errorf("Valid = %v, want %v", result.Valid, tt.wantValid)
			}

			if len(result.Warnings) != tt.wantWarnings {
				t.Errorf("got %d warnings, want %d\nwarnings: %v",
					len(result.Warnings), tt.wantWarnings, result.Warnings)
			}

			allWarnings := strings.Join(result.Warnings, "\n")
			for _, sub := range tt.wantContains {
				if !strings.Contains(allWarnings, sub) {
					t.Errorf("warnings should contain %q, got: %v", sub, result.Warnings)
				}
			}
		})
	}
}

func TestValidatePlanConstraints_NearLimit(t *testing.T) {
	// Create a temp directory with a file at 750 lines.
	tmpDir := t.TempDir()
	targetFile := "internal/service/big.go"
	targetDir := filepath.Join(tmpDir, filepath.Dir(targetFile))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a file with 750 lines.
	var content strings.Builder
	for i := 0; i < 750; i++ {
		content.WriteString("// line\n")
	}
	if err := os.WriteFile(filepath.Join(tmpDir, targetFile), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		lineCount    int // number of lines to write, 0 means use existing
		limit        int
		wantWarnings int
		wantContains string
	}{
		{
			name:         "file at 750/800 triggers near-limit warning",
			limit:        800,
			wantWarnings: 1,
			wantContains: "750/800 lines (90%+ of ceiling)",
		},
		{
			name:         "file at 750/2000 does not trigger warning",
			limit:        2000,
			wantWarnings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &constraints.Config{
				MaxLines: []constraints.MaxLinesConstraint{
					{
						Name:  "size-limit",
						Glob:  "*.go",
						Limit: tt.limit,
					},
				},
			}
			subtasks := []planEntry{
				{Title: "Modify service", EstimatedFiles: []string{targetFile}},
			}

			result := ValidatePlanConstraints(subtasks, cfg, tmpDir)

			if len(result.Warnings) != tt.wantWarnings {
				t.Errorf("got %d warnings, want %d\nwarnings: %v",
					len(result.Warnings), tt.wantWarnings, result.Warnings)
			}

			if tt.wantContains != "" {
				allWarnings := strings.Join(result.Warnings, "\n")
				if !strings.Contains(allWarnings, tt.wantContains) {
					t.Errorf("warnings should contain %q, got: %v", tt.wantContains, result.Warnings)
				}
			}

			if !result.Valid {
				t.Error("near-limit should produce warnings, not errors")
			}
		})
	}
}

func TestValidatePlanConstraints_NearLimitSmallFile(t *testing.T) {
	// Create a temp directory with a file well under the limit.
	tmpDir := t.TempDir()
	targetFile := "internal/service/small.go"
	targetDir := filepath.Join(tmpDir, filepath.Dir(targetFile))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a file with 100 lines.
	var content strings.Builder
	for i := 0; i < 100; i++ {
		content.WriteString("// line\n")
	}
	if err := os.WriteFile(filepath.Join(tmpDir, targetFile), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &constraints.Config{
		MaxLines: []constraints.MaxLinesConstraint{
			{
				Name:  "size-limit",
				Glob:  "*.go",
				Limit: 800,
			},
		},
	}
	subtasks := []planEntry{
		{Title: "Small change", EstimatedFiles: []string{targetFile}},
	}

	result := ValidatePlanConstraints(subtasks, cfg, tmpDir)

	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings for file well under limit, got: %v", result.Warnings)
	}
	if !result.Valid {
		t.Error("result should be valid")
	}
}

func TestValidatePlanConstraints_EmptyWorktreeSkipsNearLimit(t *testing.T) {
	cfg := &constraints.Config{
		MaxLines: []constraints.MaxLinesConstraint{
			{
				Name:  "size-limit",
				Glob:  "*.go",
				Limit: 100,
			},
		},
	}
	subtasks := []planEntry{
		{Title: "Change", EstimatedFiles: []string{"internal/foo.go"}},
	}

	// Empty worktreeRoot should skip near-limit checks entirely.
	result := ValidatePlanConstraints(subtasks, cfg, "")

	if len(result.Warnings) != 0 {
		t.Errorf("expected no warnings when worktreeRoot is empty, got: %v", result.Warnings)
	}
	if !result.Valid {
		t.Error("result should be valid")
	}
}
