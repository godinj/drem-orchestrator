package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// verifyTestsBeforeMerge tests
// ---------------------------------------------------------------------------

func TestVerifyTestsBeforeMerge_PassFirstTry(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		TestCommand: "true", // always exits 0
		TestTimeout: 30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
	}

	result, err := o.verifyTestsBeforeMerge(subtask, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected test to pass")
	}
	if result.AttemptCount != 1 {
		t.Errorf("expected 1 attempt, got %d", result.AttemptCount)
	}
	if result.Command != "true" {
		t.Errorf("expected command 'true', got %q", result.Command)
	}
}

func TestVerifyTestsBeforeMerge_PassOnRetry(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// Create a temp dir with a counter file to track attempts.
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "attempt_counter")
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Script: fail on first call, succeed on second.
	script := fmt.Sprintf(`#!/bin/sh
count=$(cat %s)
count=$((count + 1))
echo $count > %s
if [ "$count" -lt 2 ]; then
  echo "FAIL attempt $count"
  exit 1
fi
echo "PASS attempt $count"
exit 0
`, counterFile, counterFile)

	scriptFile := filepath.Join(dir, "test.sh")
	if err := os.WriteFile(scriptFile, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	o.testGate = TestGateConfig{
		TestCommand: "sh " + scriptFile,
		TestTimeout: 30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
	}

	result, err := o.verifyTestsBeforeMerge(subtask, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected test to pass on retry")
	}
	if result.AttemptCount != 2 {
		t.Errorf("expected 2 attempts, got %d", result.AttemptCount)
	}
}

func TestVerifyTestsBeforeMerge_AllRetriesFail(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		TestCommand: "false", // always exits 1
		TestTimeout: 30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
	}

	result, err := o.verifyTestsBeforeMerge(subtask, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected test to fail after all retries")
	}
	if result.AttemptCount != 3 {
		t.Errorf("expected 3 attempts, got %d", result.AttemptCount)
	}
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit code")
	}
}

func TestVerifyTestsBeforeMerge_MissingToolClassifiesRuntimeMissingTool(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		TestCommand: "definitely-not-a-real-test-tool-42 --version",
		TestTimeout: 30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
	}

	result, err := o.verifyTestsBeforeMerge(subtask, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Fatal("expected missing tool to fail the gate")
	}
	if result.ExitCode != 127 {
		t.Fatalf("expected shell exit 127, got %d with output %q", result.ExitCode, result.Output)
	}
	if result.FailureClass != "runtime_missing_tool" {
		t.Fatalf("expected runtime_missing_tool, got %q", result.FailureClass)
	}
}

func TestVerifyTestsBeforeMerge_NoTestCommand(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		TestCommand: "", // no command configured
		TestTimeout: 30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
	}

	result, err := o.verifyTestsBeforeMerge(subtask, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected degraded mode to return passed=true")
	}
}

// ---------------------------------------------------------------------------
// verifyCompilationBeforeMerge tests
// ---------------------------------------------------------------------------

func TestVerifyCompilationBeforeMerge_Passes(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		CompileCommand: "true",
		TestTimeout:    30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}

	result, err := o.verifyCompilationBeforeMerge(subtask, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected compilation to pass")
	}
	if result.AttemptCount != 1 {
		t.Errorf("expected 1 attempt (no retries for compilation), got %d", result.AttemptCount)
	}
}

func TestVerifyCompilationBeforeMerge_Fails(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		CompileCommand: "false",
		TestTimeout:    30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}

	result, err := o.verifyCompilationBeforeMerge(subtask, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected compilation to fail")
	}
}

func TestVerifyCompilationBeforeMerge_NoCommand_UnknownLanguage(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		CompileCommand: "", // empty
		TestTimeout:    30 * time.Second,
	}

	// Use a temp dir without any known project files.
	dir := t.TempDir()

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}

	result, err := o.verifyCompilationBeforeMerge(subtask, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected skip (passed=true) when no compile command found")
	}
}

func TestVerifyCompilationBeforeMerge_InfersGoVet(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)
	o.testGate = TestGateConfig{
		CompileCommand: "", // empty — should infer
		TestTimeout:    30 * time.Second,
	}

	// Create a temp dir with a go.mod file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}

	result, err := o.verifyCompilationBeforeMerge(subtask, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Command != "go vet ./..." {
		t.Errorf("expected inferred command 'go vet ./...', got %q", result.Command)
	}
}

// ---------------------------------------------------------------------------
// scopeTestCommand tests
// ---------------------------------------------------------------------------

func TestScopeTestCommand_GoPackages(t *testing.T) {
	changedFiles := []string{
		"internal/track/tracker.go",
		"internal/track/tracker_test.go",
		"internal/render/view.go",
	}

	scopedCmd, didScope := scopeTestCommand("go test ./...", changedFiles)
	if !didScope {
		t.Fatal("expected scoping to apply")
	}
	if !strings.Contains(scopedCmd, "./internal/track/...") {
		t.Errorf("expected scoped command to contain ./internal/track/..., got %q", scopedCmd)
	}
	if !strings.Contains(scopedCmd, "./internal/render/...") {
		t.Errorf("expected scoped command to contain ./internal/render/..., got %q", scopedCmd)
	}
	if strings.Contains(scopedCmd, "./...") && !strings.Contains(scopedCmd, "/...") {
		t.Errorf("expected ./... to be replaced, got %q", scopedCmd)
	}
}

func TestScopeTestCommand_NoEllipsis(t *testing.T) {
	changedFiles := []string{"internal/track/tracker.go"}

	scopedCmd, didScope := scopeTestCommand("go test ./internal/track/...", changedFiles)
	if didScope {
		t.Error("expected no scoping when command doesn't have ./...")
	}
	if scopedCmd != "go test ./internal/track/..." {
		t.Errorf("expected original command, got %q", scopedCmd)
	}
}

func TestScopeTestCommand_EmptyFileList(t *testing.T) {
	scopedCmd, didScope := scopeTestCommand("go test ./...", nil)
	if didScope {
		t.Error("expected no scoping with empty file list")
	}
	if scopedCmd != "go test ./..." {
		t.Errorf("expected original command, got %q", scopedCmd)
	}
}

func TestScopeTestCommand_NonGoFiles(t *testing.T) {
	changedFiles := []string{
		"README.md",
		"docs/setup.md",
	}

	scopedCmd, didScope := scopeTestCommand("go test ./...", changedFiles)
	if didScope {
		t.Error("expected no scoping when no .go files changed")
	}
	if scopedCmd != "go test ./..." {
		t.Errorf("expected original command, got %q", scopedCmd)
	}
}

func TestScopeTestCommand_RootLevelGoFile(t *testing.T) {
	changedFiles := []string{
		"main.go",
	}

	scopedCmd, didScope := scopeTestCommand("go test ./...", changedFiles)
	if didScope {
		t.Error("expected no scoping when root-level go files change (falls back to full)")
	}
	if scopedCmd != "go test ./..." {
		t.Errorf("expected original command, got %q", scopedCmd)
	}
}

// ---------------------------------------------------------------------------
// storeTestResult tests
// ---------------------------------------------------------------------------

func TestStoreTestResult_PopulatesAgentConfig(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// Create project and agent.
	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	ag := &model.Agent{
		ID:        uuid.New(),
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "test-agent",
		Status:    model.AgentWorking,
	}
	db.Create(ag)

	result := &TestResult{
		Passed:       true,
		Output:       "all tests passed",
		ExitCode:     0,
		RunAt:        time.Now(),
		Duration:     1.5,
		Command:      "go test ./...",
		Scoped:       false,
		AttemptCount: 1,
	}

	o.storeTestResult(ag, result)

	// Reload agent from DB and check config.
	var reloaded model.Agent
	db.First(&reloaded, "id = ?", ag.ID)

	if reloaded.Config == nil {
		t.Fatal("expected agent config to be populated")
	}
	lastResult, ok := reloaded.Config["last_test_result"]
	if !ok {
		t.Fatal("expected last_test_result in agent config")
	}

	// The result is stored as a map after JSON round-trip.
	resultMap, ok := lastResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", lastResult)
	}
	if passed, ok := resultMap["passed"].(bool); !ok || !passed {
		t.Errorf("expected passed=true in stored result, got %v", resultMap["passed"])
	}
}

// ---------------------------------------------------------------------------
// Test timeout
// ---------------------------------------------------------------------------

func TestRunCommandWithTimeout_TimesOut(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// Run a command that sleeps longer than the timeout.
	result := o.runCommandWithTimeout(t.TempDir(), "sleep 3", 200*time.Millisecond)
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit code for timed-out command")
	}
	if !strings.Contains(result.Output, "timeout exceeded") {
		t.Errorf("expected timeout message in output, got %q", result.Output)
	}
}

// ---------------------------------------------------------------------------
// Output truncation
// ---------------------------------------------------------------------------

func TestOutputTruncation(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// Generate output longer than 5000 characters.
	dir := t.TempDir()
	script := `#!/bin/sh
for i in $(seq 1 1000); do
  echo "line $i: this is a test output line that makes the total output very long"
done
exit 1
`
	scriptFile := filepath.Join(dir, "longout.sh")
	if err := os.WriteFile(scriptFile, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	o.testGate = TestGateConfig{
		TestCommand: "sh " + scriptFile,
		TestTimeout: 30 * time.Second,
	}

	subtask := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "desc",
		Status:      model.StatusInProgress,
	}

	result, err := o.verifyTestsBeforeMerge(subtask, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Output) > 5000 {
		t.Errorf("expected output truncated to 5000 chars, got %d", len(result.Output))
	}
}

// ---------------------------------------------------------------------------
// taskPhase tests
// ---------------------------------------------------------------------------

func TestTaskPhase(t *testing.T) {
	tests := []struct {
		name     string
		context  model.JSONField
		expected string
	}{
		{
			name:     "nil context",
			context:  nil,
			expected: "",
		},
		{
			name:     "no phase key",
			context:  model.JSONField{"other": "val"},
			expected: "",
		},
		{
			name:     "test phase",
			context:  model.JSONField{"phase": "test"},
			expected: "test",
		},
		{
			name:     "implementation phase",
			context:  model.JSONField{"phase": "implementation"},
			expected: "implementation",
		},
		{
			name:     "integration phase",
			context:  model.JSONField{"phase": "integration"},
			expected: "integration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &model.Task{Context: tt.context}
			got := taskPhase(task)
			if got != tt.expected {
				t.Errorf("taskPhase() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// inferTestCommand / inferCompileCommand tests
// ---------------------------------------------------------------------------

func TestInferTestCommand_Go(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := inferTestCommand(dir)
	if cmd != "go test ./..." {
		t.Errorf("expected 'go test ./...', got %q", cmd)
	}
}

func TestInferTestCommand_Unknown(t *testing.T) {
	dir := t.TempDir()
	cmd := inferTestCommand(dir)
	if cmd != "" {
		t.Errorf("expected empty string for unknown project, got %q", cmd)
	}
}

func TestInferCompileCommand_Go(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := inferCompileCommand(dir)
	if cmd != "go vet ./..." {
		t.Errorf("expected 'go vet ./...', got %q", cmd)
	}
}

func TestInferCompileCommand_Unknown(t *testing.T) {
	dir := t.TempDir()
	cmd := inferCompileCommand(dir)
	if cmd != "" {
		t.Errorf("expected empty string for unknown project, got %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// defaultTestGateConfig
// ---------------------------------------------------------------------------

func TestDefaultTestGateConfig(t *testing.T) {
	cfg := defaultTestGateConfig()
	if !cfg.ScopedTests {
		t.Error("expected ScopedTests to be true by default")
	}
	if cfg.TestTimeout != 5*time.Minute {
		t.Errorf("expected 5m timeout, got %v", cfg.TestTimeout)
	}
	if cfg.TestCommand != "" {
		t.Errorf("expected empty TestCommand by default, got %q", cfg.TestCommand)
	}
	if cfg.CompileCommand != "" {
		t.Errorf("expected empty CompileCommand by default, got %q", cfg.CompileCommand)
	}
}

// ---------------------------------------------------------------------------
// ApplyTestCommandInference — Bug H Option A startup wiring
// ---------------------------------------------------------------------------

// TestInferTestCommand_AppliedAtStartup covers the bootstrap-time
// inference wiring. When drem.toml leaves test_command empty but the
// project's main worktree contains a known build marker (go.mod,
// package.json, etc.), the orchestrator populates TestGateConfig
// before the first merge runs. This prevents drem-merger's
// parseFlags from rejecting an empty --test-cmd and the fail-close
// guard in dispatchMerge from tripping for any project whose type is
// inferable.
//
// See plans/bug-h-merger-crash-on-v17-advance.md (Option A, inference
// step).
func TestInferTestCommand_AppliedAtStartup(t *testing.T) {
	t.Run("go project gets 'go test ./...'", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		o := &Orchestrator{logger: testLogger()}
		o.SetTestGateConfig(TestGateConfig{TestCommand: ""})
		o.ApplyTestCommandInference(dir)
		if got := o.testGate.TestCommand; got != "go test ./..." {
			t.Errorf("expected 'go test ./...' after inference, got %q", got)
		}
	})

	t.Run("node project gets 'npm test'", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		o := &Orchestrator{logger: testLogger()}
		o.SetTestGateConfig(TestGateConfig{TestCommand: ""})
		o.ApplyTestCommandInference(dir)
		if got := o.testGate.TestCommand; got != "npm test" {
			t.Errorf("expected 'npm test', got %q", got)
		}
	})

	t.Run("explicit test_command from drem.toml wins over inference", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		o := &Orchestrator{logger: testLogger()}
		o.SetTestGateConfig(TestGateConfig{TestCommand: "make test"})
		o.ApplyTestCommandInference(dir)
		if got := o.testGate.TestCommand; got != "make test" {
			t.Errorf("inference must not override explicit config; got %q", got)
		}
	})

	t.Run("unknown project type leaves TestCommand empty (guard trips later)", func(t *testing.T) {
		dir := t.TempDir() // no go.mod / package.json / etc.
		o := &Orchestrator{logger: testLogger()}
		o.SetTestGateConfig(TestGateConfig{TestCommand: ""})
		o.ApplyTestCommandInference(dir)
		if got := o.testGate.TestCommand; got != "" {
			t.Errorf("unknown project type must leave TestCommand empty, got %q", got)
		}
	})

	t.Run("empty project dir is a no-op", func(t *testing.T) {
		o := &Orchestrator{logger: testLogger()}
		o.SetTestGateConfig(TestGateConfig{TestCommand: ""})
		o.ApplyTestCommandInference("")
		if got := o.testGate.TestCommand; got != "" {
			t.Errorf("empty dir must not change TestCommand, got %q", got)
		}
	})
}
