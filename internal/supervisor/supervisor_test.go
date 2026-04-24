package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
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
	s := New("claude", 30*time.Second, model.AgentCLIConfig{Effort: "low"})
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.bin != "claude" {
		t.Errorf("bin = %q, want %q", s.bin, "claude")
	}
	if s.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want %v", s.timeout, 30*time.Second)
	}
	if s.cliConfig.Effort != "low" {
		t.Errorf("cliConfig.Effort = %q, want %q", s.cliConfig.Effort, "low")
	}
}

// ---------------------------------------------------------------------------
// 2. evaluate()
// ---------------------------------------------------------------------------

func TestEvaluate_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, "hello", 0)

	s := New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
	got, err := s.evaluate(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("evaluate() = %q, want %q", got, "hello")
	}
}

func TestEvaluate_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeClaudeBin(t, dir, "oops", 1)

	s := New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
	_, err := s.evaluate(context.Background(), "test prompt")
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

	s := New(bin, 100*time.Millisecond, model.AgentCLIConfig{Effort: "low"})
	_, err := s.evaluate(context.Background(), "test prompt")
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

	s := New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
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

	s := New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
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

	s := New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
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

	s := New(bin, 5*time.Second, model.AgentCLIConfig{Effort: "low"})
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
// 4. Prompt generation functions
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
// 7. PlanDepthReviewPrompt
// ---------------------------------------------------------------------------

func TestPlanDepthReviewPrompt_ContainsRequiredFields(t *testing.T) {
	prompt := PlanDepthReviewPrompt(
		"Implement caching layer",
		"Add Redis-backed caching for API responses",
		`{"subtasks": [{"title": "Add cache interface"}]}`,
		0.35,
	)

	checks := []struct {
		name   string
		needle string
	}{
		{"task title", "Implement caching layer"},
		{"task description", "Add Redis-backed caching for API responses"},
		{"plan JSON", "Add cache interface"},
		{"depth score", "0.35"},
		{"assessment field", `"assessment"`},
		{"shallow_areas field", `"shallow_areas"`},
		{"recommendations field", `"recommendations"`},
		{"rejection_reason field", `"rejection_reason"`},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(prompt, c.needle) {
				t.Errorf("PlanDepthReviewPrompt missing %s: %q", c.name, c.needle)
			}
		})
	}
}

func TestPlanDepthReviewPrompt_EmptyPlanJSON(t *testing.T) {
	prompt := PlanDepthReviewPrompt("Title", "Desc", "", 0.0)

	if !strings.Contains(prompt, "Title") {
		t.Error("prompt should contain task title even with empty plan")
	}
	if !strings.Contains(prompt, "0.00") {
		t.Error("prompt should contain formatted zero depth score")
	}
}

func TestPlanDepthReviewPrompt_ZeroDepthScore(t *testing.T) {
	prompt := PlanDepthReviewPrompt("Title", "Desc", "{}", 0.0)

	if !strings.Contains(prompt, "0.00") {
		t.Error("prompt should render zero depth score as 0.00")
	}
}

func TestPlanDepthReviewPrompt_Truncation(t *testing.T) {
	longPlan := strings.Repeat("X", truncDiffOutput+1000)
	prompt := PlanDepthReviewPrompt("Title", "Desc", longPlan, 0.5)

	if strings.Contains(prompt, longPlan) {
		t.Error("expected long plan JSON to be truncated")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("expected truncation marker '...' in prompt")
	}
}

func TestPlanDepthReviewPrompt_DescriptionTruncation(t *testing.T) {
	longDesc := strings.Repeat("D", truncTaskDesc+500)
	prompt := PlanDepthReviewPrompt("Title", longDesc, "{}", 0.5)

	if strings.Contains(prompt, longDesc) {
		t.Error("expected long description to be truncated")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("expected truncation marker '...' in prompt")
	}
}

// ---------------------------------------------------------------------------
// 9. DepthConstraintDiagnosisPrompt
// ---------------------------------------------------------------------------

func TestDepthConstraintDiagnosisPrompt_ContainsRequiredFields(t *testing.T) {
	prompt := DepthConstraintDiagnosisPrompt(
		"Implement auth flow",
		"Package internal/auth: export_ratio 0.45 exceeds limit 0.15",
		"diff --git a/internal/auth/auth.go b/internal/auth/auth.go\n+func PublicHelper() {}",
	)

	checks := []struct {
		name   string
		needle string
	}{
		{"task title", "Implement auth flow"},
		{"constraint report", "export_ratio 0.45"},
		{"diff output", "diff --git"},
		{"violations field", `"violations"`},
		{"root_cause field", `"root_cause"`},
		{"recommendation field", `"recommendation"`},
		{"rejection_reason field", `"rejection_reason"`},
		{"package field", `"package"`},
		{"metric field", `"metric"`},
		{"actual_value field", `"actual_value"`},
		{"limit field", `"limit"`},
		{"suggestion field", `"suggestion"`},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(prompt, c.needle) {
				t.Errorf("DepthConstraintDiagnosisPrompt missing %s: %q", c.name, c.needle)
			}
		})
	}
}

func TestDepthConstraintDiagnosisPrompt_EmptyDiff(t *testing.T) {
	prompt := DepthConstraintDiagnosisPrompt("Title", "report content", "")

	if !strings.Contains(prompt, "Title") {
		t.Error("prompt should contain task title even with empty diff")
	}
	if !strings.Contains(prompt, "report content") {
		t.Error("prompt should contain constraint report even with empty diff")
	}
}

func TestDepthConstraintDiagnosisPrompt_DiffTruncation(t *testing.T) {
	longDiff := strings.Repeat("Z", truncDiffOutput+1000)
	prompt := DepthConstraintDiagnosisPrompt("Title", "report", longDiff)

	if strings.Contains(prompt, longDiff) {
		t.Error("expected long diff to be truncated")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("expected truncation marker '...' in prompt")
	}
}

func TestDepthConstraintDiagnosisPrompt_ReportTruncation(t *testing.T) {
	longReport := strings.Repeat("R", truncBuildOutput+1000)
	prompt := DepthConstraintDiagnosisPrompt("Title", longReport, "diff")

	if strings.Contains(prompt, longReport) {
		t.Error("expected long constraint report to be truncated")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("expected truncation marker '...' in prompt")
	}
}

// ---------------------------------------------------------------------------
// 10. PlanDepthReview JSON round-trip
// ---------------------------------------------------------------------------

func TestPlanDepthReview_JSONRoundTrip(t *testing.T) {
	original := PlanDepthReview{
		Assessment:      "adjustable",
		ShallowAreas:    []string{"subtask-1: thin wrapper around API", "subtask-3: no internal logic"},
		Recommendations: []string{"add validation layer", "merge subtask-1 and subtask-2"},
		RejectionReason: "Plan lacks depth in API integration layer",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded PlanDepthReview
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Assessment != original.Assessment {
		t.Errorf("Assessment = %q, want %q", decoded.Assessment, original.Assessment)
	}
	if len(decoded.ShallowAreas) != len(original.ShallowAreas) {
		t.Fatalf("ShallowAreas length = %d, want %d", len(decoded.ShallowAreas), len(original.ShallowAreas))
	}
	for i, area := range decoded.ShallowAreas {
		if area != original.ShallowAreas[i] {
			t.Errorf("ShallowAreas[%d] = %q, want %q", i, area, original.ShallowAreas[i])
		}
	}
	if len(decoded.Recommendations) != len(original.Recommendations) {
		t.Fatalf("Recommendations length = %d, want %d", len(decoded.Recommendations), len(original.Recommendations))
	}
	for i, rec := range decoded.Recommendations {
		if rec != original.Recommendations[i] {
			t.Errorf("Recommendations[%d] = %q, want %q", i, rec, original.Recommendations[i])
		}
	}
	if decoded.RejectionReason != original.RejectionReason {
		t.Errorf("RejectionReason = %q, want %q", decoded.RejectionReason, original.RejectionReason)
	}
}

func TestPlanDepthReview_FundamentallyShallow(t *testing.T) {
	review := PlanDepthReview{
		Assessment:      "fundamentally_shallow",
		ShallowAreas:    []string{"entire plan is pass-through glue"},
		Recommendations: nil,
		RejectionReason: "Task only produces thin wrappers with no internal logic",
	}

	data, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded PlanDepthReview
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Assessment != "fundamentally_shallow" {
		t.Errorf("Assessment = %q, want %q", decoded.Assessment, "fundamentally_shallow")
	}
	if decoded.Recommendations != nil {
		t.Errorf("Recommendations = %v, want nil", decoded.Recommendations)
	}
}

// ---------------------------------------------------------------------------
// 11. DepthConstraintDiagnosis JSON round-trip
// ---------------------------------------------------------------------------

func TestDepthConstraintDiagnosis_JSONRoundTrip(t *testing.T) {
	original := DepthConstraintDiagnosis{
		Violations: []depthViolation{
			{
				Package:     "internal/orchestrator",
				Metric:      "export_ratio",
				ActualValue: "0.25",
				Limit:       "0.15",
				Suggestion:  "internalize helper functions StartLoop and StopLoop",
			},
			{
				Package:     "internal/api",
				Metric:      "pass_through_count",
				ActualValue: "7",
				Limit:       "3",
				Suggestion:  "merge api and handler packages to remove indirection",
			},
		},
		RootCause:       "new code added pass-through wrappers without internal logic",
		Recommendation:  "refactor to internalize exported helpers and merge thin packages",
		RejectionReason: "Integration branch violates depth constraints in 2 packages",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DepthConstraintDiagnosis
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Violations) != 2 {
		t.Fatalf("Violations length = %d, want 2", len(decoded.Violations))
	}

	v := decoded.Violations[0]
	if v.Package != "internal/orchestrator" {
		t.Errorf("Violations[0].Package = %q, want %q", v.Package, "internal/orchestrator")
	}
	if v.Metric != "export_ratio" {
		t.Errorf("Violations[0].Metric = %q, want %q", v.Metric, "export_ratio")
	}
	if v.ActualValue != "0.25" {
		t.Errorf("Violations[0].ActualValue = %q, want %q", v.ActualValue, "0.25")
	}
	if v.Limit != "0.15" {
		t.Errorf("Violations[0].Limit = %q, want %q", v.Limit, "0.15")
	}
	if v.Suggestion == "" {
		t.Error("Violations[0].Suggestion should not be empty")
	}

	if decoded.RootCause != original.RootCause {
		t.Errorf("RootCause = %q, want %q", decoded.RootCause, original.RootCause)
	}
	if decoded.Recommendation != original.Recommendation {
		t.Errorf("Recommendation = %q, want %q", decoded.Recommendation, original.Recommendation)
	}
	if decoded.RejectionReason != original.RejectionReason {
		t.Errorf("RejectionReason = %q, want %q", decoded.RejectionReason, original.RejectionReason)
	}
}

func TestDepthConstraintDiagnosis_EmptyViolations(t *testing.T) {
	diagnosis := DepthConstraintDiagnosis{
		Violations:      []depthViolation{},
		RootCause:       "false positive",
		Recommendation:  "no action needed",
		RejectionReason: "",
	}

	data, err := json.Marshal(diagnosis)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DepthConstraintDiagnosis
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Violations) != 0 {
		t.Errorf("Violations length = %d, want 0", len(decoded.Violations))
	}
	if decoded.RejectionReason != "" {
		t.Errorf("RejectionReason = %q, want empty", decoded.RejectionReason)
	}
}
