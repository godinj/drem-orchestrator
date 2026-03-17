package constraints

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file at the given relative path under root with the
// specified content, creating any necessary parent directories.
func writeFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEvalMaxLines(t *testing.T) {
	tests := []struct {
		name       string
		constraint MaxLinesConstraint
		files      map[string]string // relPath -> content
		wantPass   bool
		wantMsg    string // substring expected in messages
	}{
		{
			name: "file under limit passes",
			constraint: MaxLinesConstraint{
				Name:  "line limit",
				Glob:  "**/*.go",
				Limit: 10,
			},
			files: map[string]string{
				"src/a.go": "line1\nline2\nline3\n",
			},
			wantPass: true,
		},
		{
			name: "file over limit fails",
			constraint: MaxLinesConstraint{
				Name:  "line limit",
				Glob:  "**/*.go",
				Limit: 2,
			},
			files: map[string]string{
				"src/a.go": "line1\nline2\nline3\n",
			},
			wantPass: false,
			wantMsg:  "src/a.go has 3 lines, exceeds limit of 2",
		},
		{
			name: "excluded file is skipped",
			constraint: MaxLinesConstraint{
				Name:    "line limit",
				Glob:    "**/*.go",
				Exclude: []string{"*_test.go"},
				Limit:   2,
			},
			files: map[string]string{
				"src/a_test.go": "line1\nline2\nline3\nline4\n",
			},
			wantPass: true,
		},
		{
			name: "shrink-only exception under baseline passes",
			constraint: MaxLinesConstraint{
				Name:  "line limit",
				Glob:  "**/*.go",
				Limit: 2,
				Exceptions: []LinesException{
					{Path: "src/big.go", Rule: "shrink-only", BaselineLines: 10},
				},
			},
			files: map[string]string{
				"src/big.go": "line1\nline2\nline3\nline4\nline5\n",
			},
			wantPass: true,
		},
		{
			name: "shrink-only exception over baseline fails",
			constraint: MaxLinesConstraint{
				Name:  "line limit",
				Glob:  "**/*.go",
				Limit: 2,
				Exceptions: []LinesException{
					{Path: "src/big.go", Rule: "shrink-only", BaselineLines: 3},
				},
			},
			files: map[string]string{
				"src/big.go": "line1\nline2\nline3\nline4\n",
			},
			wantPass: false,
			wantMsg:  "shrink-only baseline of 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tt.files {
				writeFile(t, root, path, content)
			}

			result, err := evalMaxLines(tt.constraint, root, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Passed != tt.wantPass {
				t.Errorf("expected passed=%v, got %v; messages: %v", tt.wantPass, result.Passed, result.Messages)
			}
			if tt.wantMsg != "" {
				found := false
				for _, msg := range result.Messages {
					if strings.Contains(msg, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected message containing %q, got %v", tt.wantMsg, result.Messages)
				}
			}
		})
	}
}

func TestEvalMaxMatches(t *testing.T) {
	tests := []struct {
		name       string
		constraint MaxMatchesConstraint
		files      map[string]string
		wantPass   bool
		wantMsg    string
	}{
		{
			name: "file with matches under limit passes",
			constraint: MaxMatchesConstraint{
				Name:    "exports",
				Glob:    "**/*.go",
				Pattern: "^func [A-Z]",
				Limit:   5,
				Scope:   "file",
			},
			files: map[string]string{
				"src/a.go": "func Alpha() {}\nfunc beta() {}\nfunc Gamma() {}\n",
			},
			wantPass: true,
		},
		{
			name: "file with matches over limit fails",
			constraint: MaxMatchesConstraint{
				Name:    "exports",
				Glob:    "**/*.go",
				Pattern: "^func [A-Z]",
				Limit:   1,
				Scope:   "file",
			},
			files: map[string]string{
				"src/a.go": "func Alpha() {}\nfunc Beta() {}\n",
			},
			wantPass: false,
			wantMsg:  "src/a.go has 2 matches, exceeds limit of 1",
		},
		{
			name: "scope directory aggregates across files",
			constraint: MaxMatchesConstraint{
				Name:    "imports",
				Glob:    "**/*.go",
				Pattern: "import",
				Limit:   2,
				Scope:   "directory",
			},
			files: map[string]string{
				"pkg/a.go": "import \"fmt\"\nimport \"os\"\n",
				"pkg/b.go": "import \"net\"\n",
			},
			wantPass: false,
			wantMsg:  "pkg/ has 3 matches, exceeds limit of 2",
		},
		{
			name: "shrink-only exception under baseline passes",
			constraint: MaxMatchesConstraint{
				Name:    "exports",
				Glob:    "**/*.go",
				Pattern: "^func [A-Z]",
				Limit:   1,
				Scope:   "file",
				Exceptions: []MatchesException{
					{Path: "src/big.go", Rule: "shrink-only", BaselineCount: 10},
				},
			},
			files: map[string]string{
				"src/big.go": "func Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n",
			},
			wantPass: true,
		},
		{
			name: "shrink-only exception over baseline fails",
			constraint: MaxMatchesConstraint{
				Name:    "exports",
				Glob:    "**/*.go",
				Pattern: "^func [A-Z]",
				Limit:   1,
				Scope:   "file",
				Exceptions: []MatchesException{
					{Path: "src/big.go", Rule: "shrink-only", BaselineCount: 2},
				},
			},
			files: map[string]string{
				"src/big.go": "func Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n",
			},
			wantPass: false,
			wantMsg:  "shrink-only baseline of 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tt.files {
				writeFile(t, root, path, content)
			}

			result, err := evalMaxMatches(tt.constraint, root, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Passed != tt.wantPass {
				t.Errorf("expected passed=%v, got %v; messages: %v", tt.wantPass, result.Passed, result.Messages)
			}
			if tt.wantMsg != "" {
				found := false
				for _, msg := range result.Messages {
					if strings.Contains(msg, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected message containing %q, got %v", tt.wantMsg, result.Messages)
				}
			}
		})
	}
}

func TestEvalNoMatch(t *testing.T) {
	tests := []struct {
		name       string
		constraint NoMatchConstraint
		files      map[string]string
		wantPass   bool
		wantMsg    string
	}{
		{
			name: "file with no matches passes",
			constraint: NoMatchConstraint{
				Name:    "no forbidden",
				Glob:    "**/*.go",
				Pattern: "forbidden",
			},
			files: map[string]string{
				"src/a.go": "package main\nfunc main() {}\n",
			},
			wantPass: true,
		},
		{
			name: "file with match fails",
			constraint: NoMatchConstraint{
				Name:    "no forbidden",
				Glob:    "**/*.go",
				Pattern: "forbidden",
			},
			files: map[string]string{
				"src/a.go": "package main\n// forbidden call here\n",
			},
			wantPass: false,
			wantMsg:  "src/a.go: // forbidden call here",
		},
		{
			name: "file under exclude_path is skipped",
			constraint: NoMatchConstraint{
				Name:        "no forbidden",
				Glob:        "**/*.go",
				ExcludePath: []string{"src/util/"},
				Pattern:     "forbidden",
			},
			files: map[string]string{
				"src/util/a.go": "package util\n// forbidden but allowed here\n",
			},
			wantPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tt.files {
				writeFile(t, root, path, content)
			}

			result, err := evalNoMatch(tt.constraint, root, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Passed != tt.wantPass {
				t.Errorf("expected passed=%v, got %v; messages: %v", tt.wantPass, result.Passed, result.Messages)
			}
			if tt.wantMsg != "" {
				found := false
				for _, msg := range result.Messages {
					if strings.Contains(msg, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected message containing %q, got %v", tt.wantMsg, result.Messages)
				}
			}
		})
	}
}

func TestEvalCommand(t *testing.T) {
	tests := []struct {
		name       string
		constraint CommandConstraint
		wantPass   bool
		wantMsg    string
	}{
		{
			name: "command exits zero passes",
			constraint: CommandConstraint{
				Name:   "true",
				Run:    "true",
				Expect: "exit_zero",
			},
			wantPass: true,
		},
		{
			name: "command exits non-zero fails",
			constraint: CommandConstraint{
				Name:   "false",
				Run:    "echo 'failure message' && false",
				Expect: "exit_zero",
			},
			wantPass: false,
			wantMsg:  "failure message",
		},
		{
			name: "empty_output with empty stdout passes",
			constraint: CommandConstraint{
				Name:   "empty",
				Run:    "true",
				Expect: "empty_output",
			},
			wantPass: true,
		},
		{
			name: "empty_output with non-empty stdout fails",
			constraint: CommandConstraint{
				Name:   "notempty",
				Run:    "echo 'some output'",
				Expect: "empty_output",
			},
			wantPass: false,
			wantMsg:  "some output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()

			result, err := evalCommand(tt.constraint, root)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Passed != tt.wantPass {
				t.Errorf("expected passed=%v, got %v; messages: %v", tt.wantPass, result.Passed, result.Messages)
			}
			if tt.wantMsg != "" {
				found := false
				for _, msg := range result.Messages {
					if strings.Contains(msg, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected message containing %q, got %v", tt.wantMsg, result.Messages)
				}
			}
		})
	}
}

func TestEvaluateFiles(t *testing.T) {
	root := t.TempDir()

	// Create files.
	writeFile(t, root, "src/a.go", "line1\nline2\nline3\n")
	writeFile(t, root, "src/b.go", strings.Repeat("line\n", 100))

	cfg := &Config{
		Commands: []CommandConstraint{
			{Name: "should be skipped", Run: "false", Expect: "exit_zero"},
		},
		MaxLines: []MaxLinesConstraint{
			{Name: "line limit", Glob: "**/*.go", Limit: 50},
		},
	}

	// Only check src/a.go (under limit), not src/b.go (over limit).
	report, err := EvaluateFiles(cfg, root, []string{"src/a.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not include command results.
	for _, r := range report.Results {
		if r.Type == "command" {
			t.Error("EvaluateFiles should not evaluate command constraints")
		}
	}

	if report.Failed != 0 {
		t.Errorf("expected 0 failures (only checked a.go), got %d; results: %v", report.Failed, report.Results)
	}
}

func TestFormatReport(t *testing.T) {
	report := &Report{
		Results: []Result{
			{Name: "gofmt", Type: "command", Passed: true},
			{Name: "line limit", Type: "max_lines", Passed: false, Messages: []string{"big.go has 900 lines, exceeds limit of 800"}},
		},
		Passed: 1,
		Failed: 1,
	}

	output := FormatReport(report)

	if !strings.Contains(output, "PASS: gofmt") {
		t.Errorf("expected PASS line in output, got:\n%s", output)
	}
	if !strings.Contains(output, "FAIL: line limit") {
		t.Errorf("expected FAIL line in output, got:\n%s", output)
	}
	if !strings.Contains(output, "big.go has 900 lines") {
		t.Errorf("expected violation detail in output, got:\n%s", output)
	}
	if !strings.Contains(output, "1 checks passed, 1 failed") {
		t.Errorf("expected summary in output, got:\n%s", output)
	}
}
