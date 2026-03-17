package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func writeConstraintsConfig(t *testing.T, dir string, tomlContent string) {
	t.Helper()
	dremDir := filepath.Join(dir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(tomlContent), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadContextFiles(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		wantEmpty bool
		wantParts []string // substrings that must appear in the output
	}{
		{
			name: "valid config and context file",
			setup: func(t *testing.T, dir string) {
				writeConstraintsConfig(t, dir, `context_files = ["ARCH.md"]`)
				if err := os.WriteFile(filepath.Join(dir, "ARCH.md"), []byte("# Architecture\n\nMax 300 lines per file."), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantEmpty: false,
			wantParts: []string{
				"## Architecture & Constraints",
				"### ARCH.md",
				"# Architecture",
				"Max 300 lines per file.",
			},
		},
		{
			name: "no config file",
			setup: func(t *testing.T, dir string) {
				// No .drem/ directory at all
			},
			wantEmpty: true,
		},
		{
			name: "config but missing context file",
			setup: func(t *testing.T, dir string) {
				writeConstraintsConfig(t, dir, `context_files = ["missing.md"]`)
			},
			wantEmpty: true,
		},
		{
			name: "multiple context files",
			setup: func(t *testing.T, dir string) {
				writeConstraintsConfig(t, dir, `context_files = ["ARCH.md", "RULES.md"]`)
				if err := os.WriteFile(filepath.Join(dir, "ARCH.md"), []byte("Architecture rules here."), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "RULES.md"), []byte("Coding rules here."), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantEmpty: false,
			wantParts: []string{
				"### ARCH.md",
				"Architecture rules here.",
				"### RULES.md",
				"Coding rules here.",
			},
		},
		{
			name: "empty worktree path",
			setup: func(t *testing.T, dir string) {
				// Will be called with empty string
			},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			worktreePath := dir
			if tt.name == "empty worktree path" {
				worktreePath = ""
			}

			got := readContextFiles(worktreePath)

			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty string, got:\n%s", got)
				}
				return
			}

			if got == "" {
				t.Fatal("expected non-empty string, got empty")
			}

			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("output missing expected substring %q\nfull output:\n%s", part, got)
				}
			}
		})
	}
}

func TestGenerateIncludesContextFiles(t *testing.T) {
	dir := t.TempDir()

	// Set up .drem/constraints.toml and a context file.
	writeConstraintsConfig(t, dir, `context_files = ["CONSTRAINTS.md"]`)
	if err := os.WriteFile(filepath.Join(dir, "CONSTRAINTS.md"), []byte("No file may exceed 300 lines."), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := Opts{
		Task: &model.Task{
			Title:       "Test task",
			Description: "A test task description.",
		},
		AgentType:    model.AgentCoder,
		WorktreePath: dir,
	}

	got := Generate(opts)

	wantParts := []string{
		"## Architecture & Constraints",
		"### CONSTRAINTS.md",
		"No file may exceed 300 lines.",
	}

	for _, part := range wantParts {
		if !strings.Contains(got, part) {
			t.Errorf("Generate() output missing expected substring %q", part)
		}
	}
}
