package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ---------------------------------------------------------------------------
// Integration gate constraint check tests
// ---------------------------------------------------------------------------

// TestIntegrationGate_NoConstraintsConfig verifies that when no
// .drem/constraints.toml exists in the worktree, the parent task transitions
// normally to testing_ready.
func TestIntegrationGate_NoConstraintsConfig(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "gate-no-config"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Add a real commit so the empty-feature check passes.
	testFile := filepath.Join(featureDir, "code.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add code")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
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

	// Create done subtasks.
	for _, title := range []string{"sub1", "sub2"} {
		sub := model.Task{
			ID:           uuid.New(),
			ProjectID:    o.projectID,
			ParentTaskID: &parentID,
			Title:        title,
			Description:  "test subtask",
			Status:       model.StatusDone,
		}
		db.Create(&sub)
	}

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updated.Status)
	}
}

// TestIntegrationGate_ConstraintsPass verifies that when constraints are
// configured and all pass, the parent transitions to testing_ready.
func TestIntegrationGate_ConstraintsPass(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "gate-pass"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Add a small source file and a constraints config that allows it.
	testFile := filepath.Join(featureDir, "small.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	constraintsToml := `
[[constraint]]
name = "file size check"
type = "max_lines"
pattern = "*.go"
limit = 1000
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsToml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add code and constraints")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
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
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updated.Status)
	}
}

// TestIntegrationGate_ConstraintsFail verifies that when constraints fail,
// the parent stays in_progress and constraint_violations are stored in context.
func TestIntegrationGate_ConstraintsFail(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "gate-fail"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create a file that contains a forbidden pattern.
	testFile := filepath.Join(featureDir, "code.go")
	if err := os.WriteFile(testFile, []byte("package main\n\n// TODO: remove this debug line\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	constraintsToml := `
[[constraint]]
name = "no debug TODOs"
type = "no_match"
pattern = "TODO: remove this debug"
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsToml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add code with debug TODO")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
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

	// Parent should remain in_progress.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}

	// Verify constraint_violations is set in context.
	violations, ok := updated.Context["constraint_violations"]
	if !ok {
		t.Fatal("expected constraint_violations in parent context")
	}
	vStr, ok := violations.(string)
	if !ok {
		t.Fatal("expected constraint_violations to be a string")
	}
	if !strings.Contains(vStr, "FAIL") {
		t.Errorf("expected violation report to contain FAIL, got: %s", vStr)
	}
	if !strings.Contains(vStr, "no debug TODOs") {
		t.Errorf("expected violation report to mention constraint name, got: %s", vStr)
	}
}

// TestIntegrationGate_ViolationsClearedOnPass verifies that when constraints
// pass after a previous failure, the constraint_violations context is cleared.
func TestIntegrationGate_ViolationsClearedOnPass(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "gate-cleared"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create a clean file (no violations) with a passing constraint.
	testFile := filepath.Join(featureDir, "clean.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	constraintsToml := `
[[constraint]]
name = "file size check"
type = "max_lines"
pattern = "*.go"
limit = 1000
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsToml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add clean code")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	// Parent with pre-existing constraint_violations from a prior failure.
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
		Context: model.JSONField{
			"constraint_violations": "previous failure details",
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

	// Parent should transition to testing_ready.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updated.Status)
	}

	// Verify constraint_violations has been cleared.
	if _, ok := updated.Context["constraint_violations"]; ok {
		t.Error("expected constraint_violations to be cleared from context after passing")
	}
}

// TestIntegrationGate_CommandConstraintFails verifies that a command constraint
// that returns a non-zero exit code blocks the transition to testing_ready.
func TestIntegrationGate_CommandConstraintFails(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "gate-cmd-fail"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Add a file so the empty-feature check passes.
	testFile := filepath.Join(featureDir, "code.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dremDir := filepath.Join(featureDir, ".drem")
	if err := os.MkdirAll(dremDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// "false" is a standard Unix command that always exits with 1.
	constraintsToml := `
[[constraint]]
name = "always-fail check"
type = "command"
run = "false"
`
	if err := os.WriteFile(filepath.Join(dremDir, "constraints.toml"), []byte(constraintsToml), 0o644); err != nil {
		t.Fatal(err)
	}

	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "add code and failing command constraint")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
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

	// Parent should remain in_progress.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}

	// Verify constraint_violations is set.
	violations, ok := updated.Context["constraint_violations"]
	if !ok {
		t.Fatal("expected constraint_violations in parent context")
	}
	vStr, ok := violations.(string)
	if !ok {
		t.Fatal("expected constraint_violations to be a string")
	}
	if !strings.Contains(vStr, "FAIL") {
		t.Errorf("expected violation report to contain FAIL, got: %s", vStr)
	}
	if !strings.Contains(vStr, "always-fail check") {
		t.Errorf("expected violation report to mention constraint name, got: %s", vStr)
	}
}
