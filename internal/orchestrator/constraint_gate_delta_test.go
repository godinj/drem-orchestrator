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
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ---------------------------------------------------------------------------
// Per-constraint delta tests — evaluateConstraintGate with master comparison
// ---------------------------------------------------------------------------
//
// These tests verify that the constraint gate makes decisions based on
// per-constraint delta between the feature worktree and the master worktree,
// using CompareReports. The gate must allow tasks that do not worsen any
// individual constraint relative to master, even when pre-existing violations
// remain.
//
// Test scenarios (state transition notation: master→feature):
//   1. FAIL→FAIL (same)   — pre-existing violation, unchanged    → PASS
//   2. PASS→FAIL           — new violation introduced              → BLOCK
//   3. FAIL→FAIL (worse)   — existing violation worsened           → BLOCK
//   4. FAIL→PASS           — violation fixed                       → PASS
//   5. Mix FAIL→PASS + FAIL→FAIL (same) — fix one, keep other     → PASS
//   6. PASS→PASS           — all constraints pass in both          → PASS
// ---------------------------------------------------------------------------

// deltaConstraintsTOML is the constraints config committed to both master and
// feature worktrees in per-constraint delta tests. Uses a modest limit so it's
// easy to construct violating and non-violating files.
const deltaConstraintsTOML = `[[max_lines]]
name = "file size check"
glob = "*.go"
limit = 5
`

// deltaConstraintsTwoTOML has two independent max_lines constraints for
// scenario 5 (mix: fix one violation, keep another pre-existing).
const deltaConstraintsTwoTOML = `[[max_lines]]
name = "go file limit"
glob = "*.go"
limit = 5

[[max_lines]]
name = "text file limit"
glob = "*.txt"
limit = 5
`

// smallGoContent returns Go source with fewer lines than the 5-line limit.
func smallGoContent() string {
	return "package p\n"
}

// bigGoContent returns Go source that exceeds the 5-line limit (15 comment lines).
func bigGoContent() string {
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 15; i++ {
		b.WriteString("// filler line\n")
	}
	return b.String()
}

// smallTxtContent returns text content within the 5-line limit.
func smallTxtContent() string { return "line one\n" }

// bigTxtContent returns text content that exceeds the 5-line limit.
func bigTxtContent() string {
	var b strings.Builder
	for i := 0; i < 15; i++ {
		b.WriteString("filler line\n")
	}
	return b.String()
}

// setupDeltaRepo creates a bare repo with a main worktree containing the
// given constraints TOML committed. Returns (bareRepoPath, mainWorktreePath).
func setupDeltaRepo(t *testing.T, constraintsTOML string) (string, string) {
	t.Helper()
	bareRepo := setupTestRepoWithMainBranch(t)

	mainDir := filepath.Join(bareRepo, "main")
	runGitCmd(t, bareRepo, "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")

	dremDir := filepath.Join(mainDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, mainDir, "add", ".")
	runGitCmd(t, mainDir, "commit", "-m", "add constraints")
	return bareRepo, mainDir
}

// writeAndCommit writes a file in the given worktree and commits it.
func writeAndCommit(t *testing.T, wtDir, filename, content, msg string) {
	t.Helper()
	path := filepath.Join(wtDir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, wtDir, "add", ".")
	runGitCmd(t, wtDir, "commit", "-m", msg)
}

// removeAndCommit removes a file in the given worktree and commits the removal.
func removeAndCommit(t *testing.T, wtDir, filename, msg string) {
	t.Helper()
	runGitCmd(t, wtDir, "rm", "-f", filename)
	runGitCmd(t, wtDir, "commit", "-m", msg)
}

// runDeltaGate sets up the DB, orchestrator, task, and subtask, then calls
// checkFeatureCompletion and returns the task's status afterwards.
func runDeltaGate(t *testing.T, bareRepo, featureName string) model.TaskStatus {
	t.Helper()
	db := testutil.NewTestDB(t)
	wm := &worktree.Manager{BareRepoPath: bareRepo, DefaultBranch: "main"}
	o := testOrchestrator(t, db, wm)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepo}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "delta test",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	return updated.Status
}

// TestConstraintGateDelta_PreExistingViolation_Passes verifies that a feature
// with the SAME pre-existing violation as master (FAIL→FAIL, same magnitude)
// is allowed through the gate. This is the core fix for the pipeline standstill:
// pre-existing import/line violations in master must not block every task.
func TestConstraintGateDelta_PreExistingViolation_Passes(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-preexisting"

	// Master: violating big.go committed (FAIL baseline).
	writeAndCommit(t, mainDir, "big.go", bigGoContent(), "add violating file to master")

	// Feature branches from master and inherits big.go unchanged (FAIL→FAIL same).
	// A trivial commit is needed so the orchestrator's "no changes" guard doesn't fire.
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "feature_work.go", smallGoContent(), "feature work (trivial change)")

	status := runDeltaGate(t, bareRepo, featureName)

	// Pre-existing violation unchanged → gate must allow (testing_ready).
	if status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (pre-existing violation unchanged), got %s", status)
	}
}

// TestConstraintGateDelta_NewViolation_Blocks verifies that a feature
// introducing a NEW violation not present in master (PASS→FAIL) is blocked.
func TestConstraintGateDelta_NewViolation_Blocks(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-new-violation"

	// Master: small.go (passes constraint) — no violation.
	writeAndCommit(t, mainDir, "small.go", smallGoContent(), "add passing file to master")

	// Feature: branches from master, adds big.go (NEW violation — PASS→FAIL).
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "big.go", bigGoContent(), "introduce new violation")

	status := runDeltaGate(t, bareRepo, featureName)

	// New violation introduced → gate must block (in_progress, not testing_ready).
	if status == model.StatusTestingReady {
		t.Errorf("expected gate to block (new violation), but task reached testing_ready")
	}
	if status == model.StatusFailed {
		// Fail only on exhaustion, not first attempt.
		t.Errorf("task failed immediately; expected in_progress (gate blocks but doesn't exhaust on first attempt)")
	}
}

// TestConstraintGateDelta_WorsenedViolation_Blocks verifies that a feature
// worsening an existing violation (FAIL→FAIL with more messages) is blocked.
func TestConstraintGateDelta_WorsenedViolation_Blocks(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-worsened"

	// Master: big.go violates (1 message = 1 violating file).
	writeAndCommit(t, mainDir, "big.go", bigGoContent(), "add one violating file to master")

	// Feature: inherits big.go and adds another violating file (2 messages — WORSENED).
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "also_big.go", bigGoContent(), "add second violating file (worsened)")

	status := runDeltaGate(t, bareRepo, featureName)

	// Worsened violation → gate must block.
	if status == model.StatusTestingReady {
		t.Errorf("expected gate to block (worsened violation), but task reached testing_ready")
	}
}

// TestConstraintGateDelta_FixedViolation_Passes verifies that a feature
// fixing a violation present in master (FAIL→PASS) is allowed through the gate.
func TestConstraintGateDelta_FixedViolation_Passes(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-fixed"

	// Master: big.go violates (FAIL baseline).
	writeAndCommit(t, mainDir, "big.go", bigGoContent(), "add violating file to master")

	// Feature: removes big.go — violation fixed (FAIL→PASS).
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	removeAndCommit(t, featureDir, "big.go", "fix violation: remove big file")

	status := runDeltaGate(t, bareRepo, featureName)

	// Violation fixed → gate must allow (testing_ready).
	if status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (violation fixed), got %s", status)
	}
}

// TestConstraintGateDelta_MixFixAndPreexisting_Passes verifies that a feature
// fixing one constraint (FAIL→PASS) while keeping another pre-existing violation
// unchanged (FAIL→FAIL same) is allowed through the gate.
func TestConstraintGateDelta_MixFixAndPreexisting_Passes(t *testing.T) {
	// Two constraints: "go file limit" (*.go) and "text file limit" (*.txt).
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTwoTOML)
	featureName := "delta-mix"

	// Master: violating files for BOTH constraints.
	writeAndCommit(t, mainDir, "fix_me.go", bigGoContent(), "add violating go file to master")
	writeAndCommit(t, mainDir, "preexisting.txt", bigTxtContent(), "add violating txt file to master")

	// Feature: fixes go file violation (FAIL→PASS for "go file limit"),
	// leaves txt file unchanged (FAIL→FAIL same for "text file limit").
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "fix_me.go", smallGoContent(), "fix go file violation")
	// preexisting.txt is inherited unchanged — no action needed.

	status := runDeltaGate(t, bareRepo, featureName)

	// One fixed, one pre-existing unchanged → no worsening → gate must allow.
	if status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (fixed one + pre-existing unchanged), got %s", status)
	}
}

// TestConstraintGateDelta_AllPassInBoth_Passes verifies the base case: when
// all constraints pass in both master and feature (PASS→PASS), the gate allows.
func TestConstraintGateDelta_AllPassInBoth_Passes(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-all-pass"

	// Master: small.go passes constraint.
	writeAndCommit(t, mainDir, "small.go", smallGoContent(), "add passing file to master")

	// Feature: adds another small file — still passes.
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "also_small.go", smallGoContent(), "add another passing file")

	status := runDeltaGate(t, bareRepo, featureName)

	// No violations in either → gate must allow (testing_ready).
	if status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (all constraints pass in both), got %s", status)
	}
}

// TestConstraintGateDelta_MasterEvalFailure_Skips verifies that when the master
// worktree has a broken/missing constraints config, the gate SKIPs rather than
// blocking the task. The task should proceed to testing_ready because we cannot
// compute a meaningful delta when the baseline is unavailable.
func TestConstraintGateDelta_MasterEvalFailure_Skips(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-master-broken"

	// Master: commit a small passing file so feature has something to branch from.
	writeAndCommit(t, mainDir, "small.go", smallGoContent(), "add passing file to master")

	// Feature: branches from master, adds a big file that violates the constraint.
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "big.go", bigGoContent(), "add violating file in feature")

	// Corrupt master's constraints.toml AFTER branching so feature still has valid config
	// but baseline evaluation will fail.
	if err := os.WriteFile(filepath.Join(mainDir, ".drem", "constraints.toml"), []byte("this is not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, mainDir, "add", ".")
	runGitCmd(t, mainDir, "commit", "-m", "corrupt constraints on master")

	status := runDeltaGate(t, bareRepo, featureName)

	// Master eval fails → gate should SKIP → task reaches testing_ready.
	if status != model.StatusTestingReady {
		t.Errorf("expected testing_ready (gate SKIP on master eval failure), got %s", status)
	}
}

// TestConstraintGateDelta_BackoffContext_Respected verifies that when the gate
// has a future next_check timestamp, evaluation is skipped regardless of the
// worktree state. This ensures the backoff mechanism works with the delta gate.
func TestConstraintGateDelta_BackoffContext_Respected(t *testing.T) {
	bareRepo, mainDir := setupDeltaRepo(t, deltaConstraintsTOML)
	featureName := "delta-backoff"

	writeAndCommit(t, mainDir, "small.go", smallGoContent(), "passing master file")

	// Feature: new violation — would block if evaluated, but backoff is active.
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	writeAndCommit(t, featureDir, "big.go", bigGoContent(), "new violation in feature")

	db := testutil.NewTestDB(t)
	wm := &worktree.Manager{BareRepoPath: bareRepo, DefaultBranch: "main"}
	o := testOrchestrator(t, db, wm)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepo}
	db.Create(&project)

	futureCheck := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "backoff test",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"constraint_gate_retries":    float64(1),
			"constraint_gate_next_check": futureCheck,
		},
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub1",
		Description:  "subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)

	// Backoff is active — task must remain in_progress, retries must NOT change.
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected in_progress (backoff active), got %s", updated.Status)
	}
	retries, _ := updated.Context["constraint_gate_retries"].(float64)
	if int(retries) != 1 {
		t.Errorf("expected retries=1 (unchanged during backoff), got %v", retries)
	}
}
