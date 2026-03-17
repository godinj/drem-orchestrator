package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain object",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "object with surrounding text",
			input: `Here is the result: {"key": "value"} that's it`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "markdown code fence",
			input: "```json\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "nested objects",
			input: `{"outer": {"inner": "value"}, "arr": [1, 2]}`,
			want:  `{"outer": {"inner": "value"}, "arr": [1, 2]}`,
		},
		{
			name:  "array",
			input: `Some text [{"a": 1}, {"b": 2}] more text`,
			want:  `[{"a": 1}, {"b": 2}]`,
		},
		{
			name:  "escaped quotes in strings",
			input: `{"msg": "he said \"hello\""}`,
			want:  `{"msg": "he said \"hello\""}`,
		},
		{
			name:  "braces inside strings",
			input: `{"code": "func() { return }"}`,
			want:  `{"code": "func() { return }"}`,
		},
		{
			name:  "no json",
			input: "just plain text",
			want:  "",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "unclosed object",
			input: `{"key": "value"`,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateForPrompt(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "empty",
			input:  "",
			maxLen: 5,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForPrompt(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForPrompt(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper: writeFakeClaudeBin creates a shell script that prints stdout and
// exits with the given code. Returns the path to the script.
// ---------------------------------------------------------------------------

func writeFakeClaudeBin(t *testing.T, dir string, stdout string, exitCode int) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-claude")
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\nexit %d\n", stdout, exitCode)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}
	return bin
}

// ---------------------------------------------------------------------------
// 1. New() constructor
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	s := New("claude", 30*time.Second)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.claudeBin != "claude" {
		t.Errorf("claudeBin = %q, want %q", s.claudeBin, "claude")
	}
	if s.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want %v", s.timeout, 30*time.Second)
	}
}

// ---------------------------------------------------------------------------
// 2. Evaluate()
// ---------------------------------------------------------------------------

func TestEvaluate_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, "hello", 0)

	s := New(bin, 5*time.Second)
	got, err := s.Evaluate(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("Evaluate() = %q, want %q", got, "hello")
	}
}

func TestEvaluate_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, "oops", 1)

	s := New(bin, 5*time.Second)
	_, err := s.Evaluate(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "supervisor evaluate") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "supervisor evaluate")
	}
}

func TestEvaluate_Timeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nsleep 2\necho done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude bin: %v", err)
	}

	s := New(bin, 100*time.Millisecond)
	_, err := s.Evaluate(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "timeout")
	}
}

// ---------------------------------------------------------------------------
// 3. EvaluateJSON()
// ---------------------------------------------------------------------------

func TestEvaluateJSON_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, `{"key":"val"}`, 0)

	s := New(bin, 5*time.Second)
	var result map[string]string
	err := s.EvaluateJSON(context.Background(), "test", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["key"] != "val" {
		t.Errorf("result[key] = %q, want %q", result["key"], "val")
	}
}

func TestEvaluateJSON_NoJSON(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, "no json here", 0)

	s := New(bin, 5*time.Second)
	var result map[string]string
	err := s.EvaluateJSON(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no JSON found") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "no JSON found")
	}
}

func TestEvaluateJSON_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, `{"broken":}`, 0)

	s := New(bin, 5*time.Second)
	var result map[string]string
	err := s.EvaluateJSON(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "unmarshal")
	}
}

func TestEvaluateJSON_WrappedJSON(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, `Here is the result: {"key":"val"} done`, 0)

	s := New(bin, 5*time.Second)
	var result map[string]string
	err := s.EvaluateJSON(context.Background(), "test", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["key"] != "val" {
		t.Errorf("result[key] = %q, want %q", result["key"], "val")
	}
}

// ---------------------------------------------------------------------------
// 4. WriteJournalEntry()
// ---------------------------------------------------------------------------

func TestWriteJournalEntry_Success(t *testing.T) {
	dir := t.TempDir()
	entry := JournalEntry{
		Timestamp: time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC),
		AgentName: "test-agent",
		TaskID:    "task-123",
		TaskTitle: "Fix bug",
		Type:      "failure_diagnosis",
		Summary:   "Agent crashed",
		Details:   map[string]string{"key": "value"},
		Outcome:   "retried",
	}

	err := WriteJournalEntry(dir, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedFile := filepath.Join(dir, "test-agent-20260314-103000.md")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected file %s to exist", expectedFile)
	}
}

func TestWriteJournalEntry_EmptyAgentName(t *testing.T) {
	dir := t.TempDir()
	entry := JournalEntry{
		Timestamp: time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC),
		AgentName: "",
		TaskID:    "task-123",
		TaskTitle: "Fix bug",
		Type:      "failure_diagnosis",
	}

	err := WriteJournalEntry(dir, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// When AgentName is empty, "unknown" should be used in the filename
	expectedFile := filepath.Join(dir, "unknown-20260314-103000.md")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected file %s to exist (empty agent name -> 'unknown')", expectedFile)
	}
}

func TestWriteJournalEntry_SanitizesName(t *testing.T) {
	dir := t.TempDir()
	entry := JournalEntry{
		Timestamp: time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC),
		AgentName: "foo/bar baz",
		TaskID:    "task-123",
		TaskTitle: "Fix bug",
		Type:      "failure_diagnosis",
	}

	err := WriteJournalEntry(dir, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "/" -> "-" and " " -> "-"
	expectedFile := filepath.Join(dir, "foo-bar-baz-20260314-103000.md")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected file %s to exist (sanitized name)", expectedFile)
	}
}

func TestWriteJournalEntry_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "nested")
	entry := JournalEntry{
		Timestamp: time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC),
		AgentName: "agent",
		TaskID:    "task-123",
		TaskTitle: "Fix bug",
		Type:      "failure_diagnosis",
	}

	err := WriteJournalEntry(dir, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedFile := filepath.Join(dir, "agent-20260314-103000.md")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected file %s to exist (directory created)", expectedFile)
	}
}

func TestWriteJournalEntry_ContentFormat(t *testing.T) {
	dir := t.TempDir()
	entry := JournalEntry{
		Timestamp: time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC),
		AgentName: "test-agent",
		TaskID:    "task-123",
		TaskTitle: "Fix the bug",
		Type:      "failure_diagnosis",
		Summary:   "Agent crashed due to OOM",
		Details:   map[string]string{"RootCause": "memory leak"},
		Outcome:   "retried successfully",
	}

	err := WriteJournalEntry(dir, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(dir, "test-agent-20260314-103000.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := string(data)

	checks := []struct {
		name   string
		needle string
	}{
		{"heading", "# 2026-03-14 10:30:00 — failure_diagnosis"},
		{"agent", "**Agent**: test-agent"},
		{"task", "**Task**: Fix the bug (`task-123`)"},
		{"summary", "**Summary**: Agent crashed due to OOM"},
		{"details", "**RootCause**: memory leak"},
		{"outcome", "**Outcome**: retried successfully"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.needle) {
			t.Errorf("content missing %s: want substring %q in:\n%s", c.name, c.needle, content)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. Prompt generation functions
// ---------------------------------------------------------------------------

func TestFailureDiagnosisPrompt(t *testing.T) {
	prompt := FailureDiagnosisPrompt(
		"Implement auth flow",
		"Add OAuth2 support with Google provider",
		"coder",
		"Error: cannot connect to database\nstack trace here",
		"exit code 1",
	)

	checks := []string{
		"Implement auth flow",
		"coder",
		"root_cause",
		"should_retry",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Errorf("FailureDiagnosisPrompt missing %q", needle)
		}
	}
}

func TestFailureDiagnosisPrompt_Truncation(t *testing.T) {
	longDesc := strings.Repeat("A", 2000)
	prompt := FailureDiagnosisPrompt("title", longDesc, "coder", "out", "err")
	// The description is truncated to 1000 chars, so the full 2000-char string
	// should not appear, but the truncated prefix should.
	if strings.Contains(prompt, longDesc) {
		t.Error("expected long description to be truncated")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("expected truncation marker '...' in prompt")
	}
}

func TestMergeConflictPrompt(t *testing.T) {
	prompt := MergeConflictPrompt(
		"feature/auth",
		"main",
		[]string{"pkg/auth.go", "pkg/handler.go"},
		"diff output here",
	)

	checks := []string{
		"feature/auth",
		"main",
		"pkg/auth.go",
		"pkg/handler.go",
		"resolution_strategy",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Errorf("MergeConflictPrompt missing %q", needle)
		}
	}
}

func TestBuildFailurePrompt(t *testing.T) {
	prompt := BuildFailurePrompt(
		"/worktrees/feature-x",
		"cannot find package",
		[]string{"main.go", "lib.go", "util.go"},
	)

	checks := []string{
		"/worktrees/feature-x",
		"main.go",
		"lib.go",
		"util.go",
		"can_auto_fix",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Errorf("BuildFailurePrompt missing %q", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. OnDemandPrompt()
// ---------------------------------------------------------------------------

func TestOnDemandPrompt_BasicFields(t *testing.T) {
	prompt := OnDemandPrompt(OnDemandOpts{
		TaskTitle:     "Implement Feature X",
		TaskDesc:      "Description of feature",
		TaskID:        "task-abc-123",
		Status:        "in_progress",
		Branch:        "feature/x",
		DBPath:        "/tmp/drem.db",
		BareRepoPath:  "/repos/project.git",
		DefaultBranch: "main",
		JournalDir:    "/journals",
	})

	checks := []string{
		"task-abc-123",
		"Implement Feature X",
		"in_progress",
		"feature/x",
		"main",
		"/repos/project.git",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Errorf("OnDemandPrompt missing %q", needle)
		}
	}
}

func TestOnDemandPrompt_WithSubtasks(t *testing.T) {
	prompt := OnDemandPrompt(OnDemandOpts{
		TaskTitle:     "Parent Task",
		TaskID:        "task-parent",
		Status:        "in_progress",
		Branch:        "feature/parent",
		DefaultBranch: "main",
		BareRepoPath:  "/repos/project.git",
		DBPath:        "/tmp/drem.db",
		JournalDir:    "/journals",
		Subtasks: []SubtaskInfo{
			{ID: "sub-1", Title: "Subtask One", Status: "done", Branch: "agent/sub1"},
			{ID: "sub-2", Title: "Subtask Two", Status: "in_progress", Branch: "agent/sub2"},
		},
	})

	checks := []string{
		"## Subtasks",
		"Subtask One",
		"Subtask Two",
		"sub-1",
		"sub-2",
		"done",
		"in_progress",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Errorf("OnDemandPrompt with subtasks missing %q", needle)
		}
	}
}

func TestOnDemandPrompt_NoSubtasks(t *testing.T) {
	prompt := OnDemandPrompt(OnDemandOpts{
		TaskTitle:     "Solo Task",
		TaskID:        "task-solo",
		Status:        "in_progress",
		Branch:        "feature/solo",
		DefaultBranch: "main",
		BareRepoPath:  "/repos/project.git",
		DBPath:        "/tmp/drem.db",
		JournalDir:    "/journals",
		Subtasks:      nil,
	})

	if strings.Contains(prompt, "## Subtasks") {
		t.Error("OnDemandPrompt with no subtasks should not contain '## Subtasks' heading")
	}
}

func TestOnDemandPrompt_DatabaseSection(t *testing.T) {
	prompt := OnDemandPrompt(OnDemandOpts{
		TaskTitle:     "DB Task",
		TaskID:        "task-db",
		Status:        "in_progress",
		Branch:        "feature/db",
		DefaultBranch: "main",
		BareRepoPath:  "/repos/project.git",
		DBPath:        "/data/orchestrator.db",
		JournalDir:    "/journals",
	})

	checks := []string{
		"/data/orchestrator.db",
		"sqlite3",
		"SELECT",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Errorf("OnDemandPrompt database section missing %q", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. slugify and JournalFilename
// ---------------------------------------------------------------------------

func TestSlugify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "basic words", input: "Hello World", want: "hello-world"},
		{name: "special chars", input: "Foo & Bar!", want: "foo-bar"},
		{name: "empty string", input: "", want: "unknown"},
		{name: "only dashes", input: "---", want: "unknown"},
		{name: "mixed case with numbers", input: "Task 42 Done", want: "task-42-done"},
		{name: "leading trailing spaces", input: "  hello  ", want: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestJournalFilename(t *testing.T) {
	filename := JournalFilename("Automation Lanes & Modes")

	if !strings.HasPrefix(filename, "supervisor-journal-") {
		t.Errorf("filename %q should start with 'supervisor-journal-'", filename)
	}
	if !strings.HasSuffix(filename, ".md") {
		t.Errorf("filename %q should end with '.md'", filename)
	}
	if !strings.Contains(filename, "automation-lanes-modes") {
		t.Errorf("filename %q should contain slugified title 'automation-lanes-modes'", filename)
	}
}
