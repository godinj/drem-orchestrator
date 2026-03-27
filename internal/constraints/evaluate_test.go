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

// TestEvalMaxMatchesUniqueCountMode verifies that count_mode='unique' deduplicates
// identical match strings across files in the same directory.
//
// Core bug scenario: splitting a file into multiple files should not increase the
// import count when the same import path appears in more than one file.
// With count_mode='total' (default), each occurrence is counted separately —
// splitting one file with 2 imports into two files that share one import raises the
// total from 2 to 3. With count_mode='unique', only distinct match strings are
// counted, so the split has no effect.
//
// Verified: splitting a file within a package does not increase the import count.
func TestEvalMaxMatchesUniqueCountMode(t *testing.T) {
	tests := []struct {
		name       string
		constraint MaxMatchesConstraint
		files      map[string]string
		wantPass   bool
		wantMsg    string
	}{
		{
			// Two files share one import line; unique count = 2, total = 3.
			// With count_mode='unique' and limit=2, should pass.
			name: "unique deduplicates shared imports across files",
			constraint: MaxMatchesConstraint{
				Name:      "internal imports",
				Glob:      "**/*.go",
				Pattern:   `"internal/`,
				Limit:     2,
				Scope:     "directory",
				CountMode: "unique",
			},
			files: map[string]string{
				// a.go imports two paths; b.go shares one of them
				"pkg/a.go": "\t\"internal/model\"\n\t\"internal/state\"\n",
				"pkg/b.go": "\t\"internal/model\"\n",
			},
			wantPass: true, // unique = {internal/model, internal/state} = 2 <= limit 2
		},
		{
			// Same setup but count_mode='total' — total = 3 > limit 2, must fail.
			// This confirms that the default behavior is unchanged.
			name: "default count_mode total counts all occurrences",
			constraint: MaxMatchesConstraint{
				Name:    "internal imports",
				Glob:    "**/*.go",
				Pattern: `"internal/`,
				Limit:   2,
				Scope:   "directory",
				// CountMode intentionally left empty — defaults to total
			},
			files: map[string]string{
				"pkg/a.go": "\t\"internal/model\"\n\t\"internal/state\"\n",
				"pkg/b.go": "\t\"internal/model\"\n",
			},
			wantPass: false,
			wantMsg:  "pkg/ has 3 matches, exceeds limit of 2",
		},
		{
			// Splitting one file into two does not change the unique import count.
			// Before split: one file with both imports → unique = 2.
			// After split: two files, one with both + one with the shared import → unique = 2.
			// With count_mode='unique' and limit=2, both scenarios pass.
			name: "splitting a file does not increase unique import count",
			constraint: MaxMatchesConstraint{
				Name:      "internal imports",
				Glob:      "**/*.go",
				Pattern:   `"internal/`,
				Limit:     2,
				Scope:     "directory",
				CountMode: "unique",
			},
			files: map[string]string{
				// After split: original logic extracted to two files, both import internal/model
				"tui/board.go":        "\t\"internal/model\"\n\t\"internal/config\"\n",
				"tui/status_badge.go": "\t\"internal/model\"\n",
			},
			wantPass: true, // unique = {internal/model, internal/config} = 2 <= limit 2
		},
		{
			// Unique counting still detects violations when genuinely distinct paths exceed limit.
			name: "unique count exceeds limit fails",
			constraint: MaxMatchesConstraint{
				Name:      "internal imports",
				Glob:      "**/*.go",
				Pattern:   `"internal/`,
				Limit:     2,
				Scope:     "directory",
				CountMode: "unique",
			},
			files: map[string]string{
				"pkg/a.go": "\t\"internal/model\"\n\t\"internal/state\"\n\t\"internal/config\"\n",
				"pkg/b.go": "\t\"internal/model\"\n", // shared, does not add to unique count
			},
			wantPass: false, // unique = {internal/model, internal/state, internal/config} = 3 > 2
		},
		{
			// Shrink-only exception with count_mode='unique': directory is grandfathered at
			// baseline 3. As long as unique count <= 3, it passes even though limit = 1.
			name: "shrink-only exception works with unique counting",
			constraint: MaxMatchesConstraint{
				Name:      "internal imports",
				Glob:      "**/*.go",
				Pattern:   `"internal/`,
				Limit:     1,
				Scope:     "directory",
				CountMode: "unique",
				Exceptions: []MatchesException{
					{Path: "legacy/", Rule: "shrink-only", BaselineCount: 3},
				},
			},
			files: map[string]string{
				// 3 unique paths shared across two files
				"legacy/a.go": "\t\"internal/model\"\n\t\"internal/state\"\n",
				"legacy/b.go": "\t\"internal/model\"\n\t\"internal/config\"\n",
			},
			wantPass: true, // unique = 3 == baseline 3, not exceeding it
		},
		{
			// Shrink-only exception: unique count exceeds baseline should fail.
			name: "shrink-only exception over baseline fails with unique counting",
			constraint: MaxMatchesConstraint{
				Name:      "internal imports",
				Glob:      "**/*.go",
				Pattern:   `"internal/`,
				Limit:     1,
				Scope:     "directory",
				CountMode: "unique",
				Exceptions: []MatchesException{
					{Path: "legacy/", Rule: "shrink-only", BaselineCount: 2},
				},
			},
			files: map[string]string{
				"legacy/a.go": "\t\"internal/model\"\n\t\"internal/state\"\n",
				"legacy/b.go": "\t\"internal/model\"\n\t\"internal/config\"\n",
			},
			wantPass: false, // unique = 3 > baseline 2
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
		constraint noMatchConstraint
		files      map[string]string
		wantPass   bool
		wantMsg    string
	}{
		{
			name: "file with no matches passes",
			constraint: noMatchConstraint{
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
			constraint: noMatchConstraint{
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
			constraint: noMatchConstraint{
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
		constraint commandConstraint
		wantPass   bool
		wantMsg    string
	}{
		{
			name: "command exits zero passes",
			constraint: commandConstraint{
				Name:   "true",
				Run:    "true",
				Expect: "exit_zero",
			},
			wantPass: true,
		},
		{
			name: "command exits non-zero fails",
			constraint: commandConstraint{
				Name:   "false",
				Run:    "echo 'failure message' && false",
				Expect: "exit_zero",
			},
			wantPass: false,
			wantMsg:  "failure message",
		},
		{
			name: "empty_output with empty stdout passes",
			constraint: commandConstraint{
				Name:   "empty",
				Run:    "true",
				Expect: "empty_output",
			},
			wantPass: true,
		},
		{
			name: "empty_output with non-empty stdout fails",
			constraint: commandConstraint{
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
		Commands: []commandConstraint{
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
