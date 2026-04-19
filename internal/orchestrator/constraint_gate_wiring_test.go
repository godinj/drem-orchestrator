package orchestrator

import (
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
// Constraint gate wiring tests — end-to-end retry behavior
// ---------------------------------------------------------------------------
//
// These tests verify that the ConstraintGatePolicy retry mechanism is correctly
// wired into processTestWriting (test_review gate) and checkFeatureCompletion
// (testing_ready gate). They exercise backoff timing, retry counting, early
// termination, max-retry exhaustion, and context cleanup on pass.
//
// Context keys under test:
//   constraint_gate_retries         — int, number of failed constraint gate attempts
//   constraint_gate_next_check      — RFC3339 timestamp, earliest next evaluation
//   constraint_gate_previous_failed — int, failed constraint count from prior attempt
//   constraint_violations           — string, formatted violation report
// ---------------------------------------------------------------------------

// setupMainWorktreeWithConstraints creates a main worktree with a constraints.toml
// and a small passing file so the delta comparison has a valid baseline.
func setupMainWorktreeWithConstraints(t *testing.T, bareRepoPath, constraintsTOML string) string {
	t.Helper()
	mainDir := filepath.Join(bareRepoPath, "main")
	runGitCmd(t, bareRepoPath, "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")

	dremDir := filepath.Join(mainDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	smallFile := filepath.Join(mainDir, "baseline.go")
	if err := os.WriteFile(smallFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, mainDir, "add", ".")
	runGitCmd(t, mainDir, "commit", "-m", "add constraints and baseline")
	return mainDir
}

// constraintFailWorktree sets up a feature worktree with a max_lines constraint
// and a file that violates it (exceeds the line limit).
func constraintFailWorktree(t *testing.T, bareRepoPath, featureName string, limit int) string {
	t.Helper()
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create a file that exceeds the line limit.
	var lines []string
	for i := 0; i < limit+10; i++ {
		lines = append(lines, "// line")
	}
	bigFile := filepath.Join(featureDir, "big.go")
	content := "package main\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(bigFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	constraintsToml := `
[[max_lines]]
name = "file size check"
glob = "*.go"
limit = ` + itoa(limit) + `
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsToml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add big file and constraints")
	return featureDir
}

// constraintPassWorktree sets up a feature worktree with a max_lines constraint
// and a file that satisfies it.
func constraintPassWorktree(t *testing.T, bareRepoPath, featureName string) string {
	t.Helper()
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	smallFile := filepath.Join(featureDir, "small.go")
	if err := os.WriteFile(smallFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	constraintsToml := `
[[max_lines]]
name = "file size check"
glob = "*.go"
limit = 1000
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsToml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add small file and constraints")
	return featureDir
}

// itoa converts an int to its string representation without importing strconv
// to keep the test file focused.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ---------------------------------------------------------------------------
// test_review gate (processTestWriting) — constraint gate wiring
// ---------------------------------------------------------------------------

// TestConstraintGateWiring_TestReview_FirstFailure verifies that the first
// constraint failure at the test_review gate keeps the task in test_writing,
// sets constraint_gate_retries=1, and sets constraint_gate_next_check in the
// future.
func TestConstraintGateWiring_TestReview_FirstFailure(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	setupMainWorktreeWithConstraints(t, bareRepoPath, "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 5\n")
	featureName := "gate-retry-first-fail"
	constraintFailWorktree(t, bareRepoPath, featureName, 5)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"baseline_tests_checked": true,
		},
	}
	db.Create(&parent)

	// Create a done test-phase subtask.
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must stay in test_writing.
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected status test_writing, got %s", updated.Status)
	}

	// constraint_gate_retries must be 1.
	retries, ok := updated.Context["constraint_gate_retries"].(float64)
	if !ok {
		t.Fatal("expected constraint_gate_retries in context")
	}
	if int(retries) != 1 {
		t.Errorf("expected constraint_gate_retries=1, got %v", retries)
	}

	// constraint_gate_next_check must be set and in the future.
	nextCheckStr, ok := updated.Context["constraint_gate_next_check"].(string)
	if !ok {
		t.Fatal("expected constraint_gate_next_check in context")
	}
	nextCheck, err := time.Parse(time.RFC3339, nextCheckStr)
	if err != nil {
		t.Fatalf("invalid constraint_gate_next_check format: %v", err)
	}
	if !nextCheck.After(time.Now()) {
		t.Error("expected constraint_gate_next_check to be in the future")
	}
}

// TestConstraintGateWiring_TestReview_BackoffRespected verifies that when
// constraint_gate_next_check is in the future, processTestWriting returns
// immediately without re-evaluating constraints (retries not incremented).
func TestConstraintGateWiring_TestReview_BackoffRespected(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	featureName := "gate-retry-backoff"
	constraintFailWorktree(t, bareRepoPath, featureName, 5)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	// Set up a parent that already has 2 retries and a future next_check.
	futureCheck := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"baseline_tests_checked":          true,
			"constraint_gate_retries":         float64(2),
			"constraint_gate_next_check":      futureCheck,
			"constraint_gate_previous_failed": float64(3),
			"constraint_violations":           "previous violations",
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must stay in test_writing.
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected status test_writing, got %s", updated.Status)
	}

	// Retries must NOT have been incremented.
	retries, ok := updated.Context["constraint_gate_retries"].(float64)
	if !ok {
		t.Fatal("expected constraint_gate_retries in context")
	}
	if int(retries) != 2 {
		t.Errorf("expected constraint_gate_retries=2 (unchanged), got %v", retries)
	}
}

// TestConstraintGateWiring_TestReview_MaxRetriesExhausted verifies that after
// MaxConstraintGateRetries failures, the task transitions to failed with a
// descriptive reason listing which constraints failed.
func TestConstraintGateWiring_TestReview_MaxRetriesExhausted(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	setupMainWorktreeWithConstraints(t, bareRepoPath, "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 5\n")
	featureName := "gate-retry-max"
	constraintFailWorktree(t, bareRepoPath, featureName, 5)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	// Set retries to MaxConstraintGateRetries-1 so the next failure exhausts.
	// Also set next_check in the past so the gate actually evaluates.
	pastCheck := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"baseline_tests_checked":          true,
			"constraint_gate_retries":         float64(MaxConstraintGateRetries - 1),
			"constraint_gate_next_check":      pastCheck,
			"constraint_gate_previous_failed": float64(1),
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must transition to failed.
	if updated.Status != model.StatusFailed {
		t.Errorf("expected status failed, got %s", updated.Status)
	}

	// Failure reason must mention the constraint name.
	reason, ok := updated.Context["failure_reason"].(string)
	if !ok {
		t.Fatal("expected failure_reason in context")
	}
	if !strings.Contains(reason, "file size check") {
		t.Errorf("expected failure reason to mention constraint name, got: %s", reason)
	}
	if !strings.Contains(reason, "constraint") {
		t.Errorf("expected failure reason to mention 'constraint', got: %s", reason)
	}
}

// TestConstraintGateWiring_TestReview_EarlyTermination verifies that when the
// per-constraint comparison (feature vs master) shows the same magnitude of
// blocking constraints across retry attempts, the task fails early.
//
// Setup: master is clean (no violations). Feature introduces a NEW violation
// (PASS→FAIL). The previous attempt already recorded magnitude=1. The gate
// detects no improvement in magnitude and terminates early.
func TestConstraintGateWiring_TestReview_EarlyTermination(t *testing.T) {
	// Master worktree: constraints.toml with max_lines limit=5, no violating file.
	bareRepoPath := setupTestRepoWithMainBranch(t)
	featureName := "gate-retry-early-term"

	// Add a main worktree for master comparison.
	mainDir := filepath.Join(bareRepoPath, "main")
	runGitCmd(t, bareRepoPath, "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")

	// Commit constraints.toml to master (no violating files, so master passes).
	dremDir := filepath.Join(mainDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	masterConstraints := "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 5\n"
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(masterConstraints), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, mainDir, "add", ".")
	runGitCmd(t, mainDir, "commit", "-m", "add constraints to master")

	// Feature worktree: inherits constraints.toml, adds a file that violates
	// the max_lines limit (NEW violation — PASS→FAIL relative to master).
	constraintFailWorktree(t, bareRepoPath, featureName, 5)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	// Set constraint_gate_previous_failed=1, reflecting that the prior evaluation
	// found magnitude=1 (1 newly-blocked constraint). The current evaluation will
	// also yield magnitude=1, so ShouldTerminateEarly fires.
	pastCheck := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"baseline_tests_checked":          true,
			"constraint_gate_retries":         float64(2),
			"constraint_gate_next_check":      pastCheck,
			"constraint_gate_previous_failed": float64(1),
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must transition to failed (early termination — no improvement in
	// per-constraint comparison magnitude across retry attempts).
	if updated.Status != model.StatusFailed {
		t.Errorf("expected status failed (early termination), got %s", updated.Status)
	}

	reason, ok := updated.Context["failure_reason"].(string)
	if !ok {
		t.Fatal("expected failure_reason in context")
	}
	if !strings.Contains(strings.ToLower(reason), "no improvement") && !strings.Contains(strings.ToLower(reason), "not improv") {
		t.Errorf("expected failure reason to mention lack of improvement, got: %s", reason)
	}
}

// TestConstraintGateWiring_TestReview_PassAfterFailure verifies that when
// constraints pass after a previous failure, all constraint_gate_* keys and
// constraint_violations are cleared, and the task transitions to test_review.
func TestConstraintGateWiring_TestReview_PassAfterFailure(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	setupMainWorktreeWithConstraints(t, bareRepoPath, "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 1000\n")
	featureName := "gate-retry-pass"
	constraintPassWorktree(t, bareRepoPath, featureName)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	pastCheck := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"baseline_tests_checked":          true,
			"constraint_gate_retries":         float64(2),
			"constraint_gate_next_check":      pastCheck,
			"constraint_gate_previous_failed": float64(3),
			"constraint_violations":           "previous violations",
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-sub",
		Description:  "test subtask",
		Status:       model.StatusDone,
		Phase:        "test",
	}
	db.Create(&sub)

	if err := o.processTestWriting(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must transition to test_review.
	if updated.Status != model.StatusTestReview {
		t.Errorf("expected status test_review, got %s", updated.Status)
	}

	// All constraint gate context keys must be cleared.
	for _, key := range []string{
		"constraint_gate_retries",
		"constraint_gate_next_check",
		"constraint_gate_previous_failed",
		"constraint_violations",
	} {
		if _, ok := updated.Context[key]; ok {
			t.Errorf("expected %s to be cleared from context", key)
		}
	}
}

// ---------------------------------------------------------------------------
// testing_ready gate (checkFeatureCompletion) — constraint gate wiring
// ---------------------------------------------------------------------------

// TestConstraintGateWiring_TestingReady_FirstFailure verifies that the first
// constraint failure at the testing_ready gate keeps the task in in_progress,
// sets constraint_gate_retries=1, and sets constraint_gate_next_check.
func TestConstraintGateWiring_TestingReady_FirstFailure(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	setupMainWorktreeWithConstraints(t, bareRepoPath, "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 5\n")
	featureName := "gate-tr-first-fail"
	constraintFailWorktree(t, bareRepoPath, featureName, 5)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "test subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must stay in in_progress.
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", updated.Status)
	}

	// constraint_gate_retries must be 1.
	retries, ok := updated.Context["constraint_gate_retries"].(float64)
	if !ok {
		t.Fatal("expected constraint_gate_retries in context")
	}
	if int(retries) != 1 {
		t.Errorf("expected constraint_gate_retries=1, got %v", retries)
	}

	// constraint_gate_next_check must be set and in the future.
	nextCheckStr, ok := updated.Context["constraint_gate_next_check"].(string)
	if !ok {
		t.Fatal("expected constraint_gate_next_check in context")
	}
	nextCheck, err := time.Parse(time.RFC3339, nextCheckStr)
	if err != nil {
		t.Fatalf("invalid constraint_gate_next_check format: %v", err)
	}
	if !nextCheck.After(time.Now()) {
		t.Error("expected constraint_gate_next_check to be in the future")
	}
}

// TestConstraintGateWiring_TestingReady_MaxRetriesExhausted verifies that
// after MaxConstraintGateRetries failures at the testing_ready gate, the task
// transitions to failed.
func TestConstraintGateWiring_TestingReady_MaxRetriesExhausted(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	setupMainWorktreeWithConstraints(t, bareRepoPath, "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 5\n")
	featureName := "gate-tr-max-retry"
	constraintFailWorktree(t, bareRepoPath, featureName, 5)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	pastCheck := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"constraint_gate_retries":         float64(MaxConstraintGateRetries - 1),
			"constraint_gate_next_check":      pastCheck,
			"constraint_gate_previous_failed": float64(1),
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "test subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must transition to failed.
	if updated.Status != model.StatusFailed {
		t.Errorf("expected status failed, got %s", updated.Status)
	}

	// Failure reason must mention constraints.
	reason, ok := updated.Context["failure_reason"].(string)
	if !ok {
		t.Fatal("expected failure_reason in context")
	}
	if !strings.Contains(reason, "constraint") {
		t.Errorf("expected failure reason to mention 'constraint', got: %s", reason)
	}
}

// TestConstraintGateWiring_TestingReady_PassAfterRetries verifies that when
// constraints pass at the testing_ready gate after previous failures, the task
// transitions to testing_ready and all gate context is cleared.
func TestConstraintGateWiring_TestingReady_PassAfterRetries(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)
	setupMainWorktreeWithConstraints(t, bareRepoPath, "[[max_lines]]\nname = \"file size check\"\nglob = \"*.go\"\nlimit = 1000\n")
	featureName := "gate-tr-pass-retry"
	constraintPassWorktree(t, bareRepoPath, featureName)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	pastCheck := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"constraint_gate_retries":         float64(3),
			"constraint_gate_next_check":      pastCheck,
			"constraint_gate_previous_failed": float64(2),
			"constraint_violations":           "previous violations",
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "test subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Task must transition to testing_ready.
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected status testing_ready, got %s", updated.Status)
	}

	// All constraint gate context keys must be cleared.
	for _, key := range []string{
		"constraint_gate_retries",
		"constraint_gate_next_check",
		"constraint_gate_previous_failed",
		"constraint_violations",
	} {
		if _, ok := updated.Context[key]; ok {
			t.Errorf("expected %s to be cleared from context", key)
		}
	}
}
