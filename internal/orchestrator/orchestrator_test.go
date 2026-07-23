package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// testOrchestrator creates an Orchestrator with a test DB and minimal
// dependencies. The worktree manager and runner are set up with dummy paths.
func testOrchestrator(t *testing.T, db *gorm.DB, wtManager WorktreeManager) *Orchestrator {
	t.Helper()
	projectID := uuid.New()
	events := make(chan Event, 100)
	return &Orchestrator{
		db:              db,
		projectID:       projectID,
		worktree:        wtManager,
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "test-orchestrator"),
	}
}

// setupTestRepoWithMainBranch creates a temporary bare git repo with the default
// branch explicitly set to "main". This is required by tests that branch
// off "main" by name (e.g., createFeatureWorktree).
func setupTestRepoWithMainBranch(t *testing.T) string {
	t.Helper()
	bareRepoPath := testutil.SetupBareRepo(t)
	// Detect the current default branch and rename it to "main" if needed.
	defaultBranch, err := testutil.RunGit([]string{"symbolic-ref", "--short", "HEAD"}, bareRepoPath)
	if err != nil {
		t.Fatalf("detect default branch: %v", err)
	}
	if defaultBranch != "main" {
		if _, err := testutil.RunGit([]string{"branch", "-m", defaultBranch, "main"}, bareRepoPath); err != nil {
			t.Fatalf("rename branch to main: %v", err)
		}
		if _, err := testutil.RunGit([]string{"symbolic-ref", "HEAD", "refs/heads/main"}, bareRepoPath); err != nil {
			t.Fatalf("set HEAD to main: %v", err)
		}
	}
	return bareRepoPath
}

// runGitCmd runs a git command in the given directory and returns the output.
func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := testutil.RunGit(args, dir)
	if err != nil {
		t.Fatalf("git %v in %s failed: %v", args, dir, err)
	}
	return out
}

// createFeatureWorktree creates a feature worktree in the bare repo.
func createFeatureWorktree(t *testing.T, bareRepoPath, featureName string) string {
	t.Helper()
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	branchName := "feature/" + featureName
	if err := os.MkdirAll(filepath.Dir(featureDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.RunGit([]string{"branch", branchName, "main"}, bareRepoPath); err != nil {
		t.Fatalf("create branch %s: %v", branchName, err)
	}
	if _, err := testutil.RunGit([]string{"worktree", "add", featureDir, branchName}, bareRepoPath); err != nil {
		t.Fatalf("add worktree %s: %v", branchName, err)
	}
	testutil.RunGit([]string{"config", "user.email", "test@test.com"}, featureDir)
	testutil.RunGit([]string{"config", "user.name", "Test"}, featureDir)
	return featureDir
}

func recordBranchAcceptanceForTest(t *testing.T, task *model.Task, repoDir, baseRef string) {
	t.Helper()
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["branch_acceptance"] = map[string]any{
		"accepted": true,
		"base_ref": baseRef,
		"base_sha": runGitCmd(t, repoDir, "rev-parse", baseRef),
		"head_sha": runGitCmd(t, repoDir, "rev-parse", task.WorktreeBranch),
	}
}

func persistBranchAcceptanceForTest(t *testing.T, db *gorm.DB, task *model.Task) {
	t.Helper()
	raw, ok := task.Context["branch_acceptance"].(map[string]any)
	if !ok {
		t.Fatal("test task has no branch acceptance compatibility evidence")
	}
	record := model.BranchAcceptanceRecord{
		ID: uuid.New(), TaskID: task.ID, AgentID: uuid.New(),
		Branch: task.WorktreeBranch, Accepted: true,
		BaseBranch: fmt.Sprint(raw["base_ref"]), BaseSHA: fmt.Sprint(raw["base_sha"]),
		HeadSHA: fmt.Sprint(raw["head_sha"]), Details: model.JSONField(raw),
		Actor: "test", Source: "test",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("persist typed branch acceptance: %v", err)
	}
}

// createAgentBranch creates an agent branch off the feature branch
// and optionally adds a commit. Returns the branch name.
func createAgentBranch(t *testing.T, bareRepoPath, featureName, branchName string, addCommit bool) string {
	t.Helper()
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-test")
	featureBranch := "feature/" + featureName

	// Create agent branch from feature branch.
	if _, err := testutil.RunGit([]string{"branch", branchName, featureBranch}, bareRepoPath); err != nil {
		t.Fatalf("create branch %s: %v", branchName, err)
	}
	if err := os.MkdirAll(filepath.Dir(agentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.RunGit([]string{"worktree", "add", agentDir, branchName}, bareRepoPath); err != nil {
		t.Fatalf("add worktree %s: %v", branchName, err)
	}
	testutil.RunGit([]string{"config", "user.email", "test@test.com"}, agentDir)
	testutil.RunGit([]string{"config", "user.name", "Test"}, agentDir)

	if addCommit {
		testFile := filepath.Join(agentDir, "agent-work.txt")
		if err := os.WriteFile(testFile, []byte("agent work"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := testutil.RunGit([]string{"add", "."}, agentDir); err != nil {
			t.Fatalf("git add: %v", err)
		}
		if _, err := testutil.RunGit([]string{"commit", "-m", "agent work commit"}, agentDir); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	// Merge agent branch into feature to simulate already-merged.
	if _, err := testutil.RunGit([]string{"merge", "--no-ff", branchName, "-m", "merge agent"}, featureDir); err != nil {
		t.Fatalf("merge %s into feature: %v", branchName, err)
	}

	return branchName
}

// ---------------------------------------------------------------------------
// isWorkAlreadyMerged tests
// ---------------------------------------------------------------------------

func TestIsWorkAlreadyMerged_NoAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	subtask := &model.Task{
		ID:              uuid.New(),
		AssignedAgentID: nil,
	}
	if o.isWorkAlreadyMerged(subtask, "/tmp/fake") {
		t.Error("expected false when no agent assigned")
	}
}

func TestIsWorkAlreadyMerged_AgentNoBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "", // no branch
	}
	db.Create(&ag)

	subtask := &model.Task{
		ID:              uuid.New(),
		AssignedAgentID: &agentID,
	}
	if o.isWorkAlreadyMerged(subtask, "/tmp/fake") {
		t.Error("expected false when agent has no branch")
	}
}

func TestIsWorkAlreadyMerged_BranchIsAncestor(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature"
	createFeatureWorktree(t, bareRepoPath, featureName)
	agentBranch := createAgentBranch(t, bareRepoPath, featureName, "worktree-agent-test", true)

	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentIdle,
		WorktreeBranch: agentBranch,
	}
	db.Create(&ag)

	subtask := &model.Task{
		ID:              uuid.New(),
		AssignedAgentID: &agentID,
	}

	if !o.isWorkAlreadyMerged(subtask, featureDir) {
		t.Error("expected true when agent branch is ancestor of feature HEAD")
	}
}

func TestIsWorkAlreadyMerged_EqualBranchRejected(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature-equal"
	createFeatureWorktree(t, bareRepoPath, featureName)
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	featureBranch := "feature/" + featureName
	agentBranch := "worktree-agent-equal"
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	db.Create(&model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentIdle,
		WorktreeBranch: agentBranch,
	})

	subtask := &model.Task{ID: uuid.New(), AssignedAgentID: &agentID}
	if o.isWorkAlreadyMerged(subtask, featureDir) {
		t.Error("expected false when agent branch is equal to feature HEAD")
	}
}

func TestIsWorkAlreadyMerged_EphemeralOnlyBranchRejected(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature-ephemeral"
	createFeatureWorktree(t, bareRepoPath, featureName)
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	featureBranch := "feature/" + featureName
	agentBranch := "worktree-agent-ephemeral"
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-ephemeral")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(agentDir, "plan.json"), []byte(`{"plan":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "ephemeral only")
	runGitCmd(t, featureDir, "merge", "--no-ff", agentBranch, "-m", "merge ephemeral")

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	db.Create(&model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentIdle,
		WorktreeBranch: agentBranch,
	})

	subtask := &model.Task{ID: uuid.New(), AssignedAgentID: &agentID}
	if o.isWorkAlreadyMerged(subtask, featureDir) {
		t.Error("expected false when agent branch only changed ephemeral files")
	}
}

func TestIsWorkAlreadyMerged_NonZeroContainerDeathBlocks(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature-container-death"
	createFeatureWorktree(t, bareRepoPath, featureName)
	agentBranch := createAgentBranch(t, bareRepoPath, featureName, "worktree-agent-container-death", true)
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	containerID := "container-dead-1"
	db.Create(&model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentDead,
		WorktreeBranch: agentBranch,
		TmuxSession:    containerID,
	})
	subtask := &model.Task{ID: uuid.New(), AssignedAgentID: &agentID}
	db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    subtask.ID,
		EventType: "container_died",
		Details: model.JSONField{
			"container_id": containerID,
			"exit_code":    float64(1),
		},
		Actor:     "docker-events",
		CreatedAt: time.Now(),
	})

	if o.isWorkAlreadyMerged(subtask, featureDir) {
		t.Error("expected false when the worker has a non-zero container death event")
	}
}

func TestIsWorkAlreadyMerged_AgentmonPushFailureIsTelemetryOnly(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature-push-failure"
	createFeatureWorktree(t, bareRepoPath, featureName)
	agentBranch := createAgentBranch(t, bareRepoPath, featureName, "worktree-agent-push-failure", true)
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	db.Create(&model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentDead,
		WorktreeBranch: agentBranch,
		TmuxSession:    "container-push-failure",
	})
	subtask := &model.Task{ID: uuid.New(), AssignedAgentID: &agentID}
	db.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    subtask.ID,
		EventType: "build_error",
		NewValue:  "failed to push some refs to '/bare'",
		Actor:     "watchdog",
		CreatedAt: time.Now(),
	})

	if !o.isWorkAlreadyMerged(subtask, featureDir) {
		t.Error("agentmon build_error telemetry must not override authoritative branch and attempt evidence")
	}
}

func TestFeatureBranchHasChanges_IgnoresEphemeralOnly(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature-ephemeral-direct"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)
	if err := os.WriteFile(filepath.Join(featureDir, "plan.json"), []byte(`{"plan":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "ephemeral only")

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)
	task := &model.Task{ID: uuid.New()}

	if o.featureBranchHasChanges(task, featureDir) {
		t.Error("expected ephemeral-only feature changes not to count as task work")
	}
}

func TestIsWorkAlreadyMerged_BranchDiverged(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-feature-diverge"
	createFeatureWorktree(t, bareRepoPath, featureName)
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")

	// Create agent branch without merging it into the feature.
	branchName := "worktree-agent-diverged"
	featureBranch := "feature/" + featureName
	if _, err := testutil.RunGit([]string{"branch", branchName, featureBranch}, bareRepoPath); err != nil {
		t.Fatalf("create branch %s: %v", branchName, err)
	}
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-diverged")
	if _, err := testutil.RunGit([]string{"worktree", "add", agentDir, branchName}, bareRepoPath); err != nil {
		t.Fatalf("add worktree %s: %v", branchName, err)
	}
	testutil.RunGit([]string{"config", "user.email", "test@test.com"}, agentDir)
	testutil.RunGit([]string{"config", "user.name", "Test"}, agentDir)
	testFile := filepath.Join(agentDir, "diverged.txt")
	if err := os.WriteFile(testFile, []byte("diverged work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.RunGit([]string{"add", "."}, agentDir); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := testutil.RunGit([]string{"commit", "-m", "diverged commit"}, agentDir); err != nil {
		t.Fatalf("commit: %v", err)
	}

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	o := testOrchestrator(t, db, wt)

	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-agent",
		Status:         model.AgentIdle,
		WorktreeBranch: branchName,
	}
	db.Create(&ag)

	subtask := &model.Task{
		ID:              uuid.New(),
		AssignedAgentID: &agentID,
	}

	if o.isWorkAlreadyMerged(subtask, featureDir) {
		t.Error("expected false when agent branch has diverged from feature")
	}
}

// ---------------------------------------------------------------------------
// checkFeatureCompletion tests
// ---------------------------------------------------------------------------

func TestCheckFeatureCompletion_AllDone(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// Create project.
	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusInProgress,
		// No WorktreeBranch — skip the file change check.
	}
	db.Create(&parent)

	// Create done subtasks.
	for _, title := range []string{"sub1", "sub2", "sub3"} {
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

	// Reload parent to check status.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected parent status testing_ready, got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_FailedChildDrainsInProgressSibling(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// One failed, one still in_progress.
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "failed-sub",
		Description:  "test subtask",
		Status:       model.StatusFailed,
	}
	db.Create(&sub1)

	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "running-sub",
		Description:  "test subtask",
		Status:       model.StatusInProgress,
	}
	db.Create(&sub2)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A required failure cancels work that has not started, but an already
	// running sibling is allowed to finish and preserve its checkpoint.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to drain, got %s", updated.Status)
	}
	var running model.Task
	db.First(&running, "id = ?", sub2.ID)
	if running.Status != model.StatusInProgress {
		t.Errorf("expected running sibling to keep draining, got %s", running.Status)
	}
}

func TestCheckFeatureCompletion_AllTerminalSomeFailed(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	// All terminal: 2 done, 1 failed.
	sub1 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub1",
		Description:  "test subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub1)

	sub2 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "done-sub2",
		Description:  "test subtask",
		Status:       model.StatusDone,
	}
	db.Create(&sub2)

	sub3 := model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "failed-sub",
		Description:  "test subtask",
		Status:       model.StatusFailed,
	}
	db.Create(&sub3)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parent should be failed since all terminal and some failed.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected parent to be failed, got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_NoSubtasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test parent",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parent should remain in_progress.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// reconcileStuckAgents tests
// ---------------------------------------------------------------------------

func TestReconcileStuckAgents_AgentInRunnerMap(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestrator(t, db, wt)

	// We can't easily mock the runner, so we test the DB-level logic.
	// Create an orchestrator with a nil runner to verify the method
	// handles the case where an agent IS in the runner map (no action).
	// For this test, we verify the SQL query returns correct subtasks.

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test",
	}
	db.Create(&parent)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "test-agent",
		Status:    model.AgentWorking,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-sub",
		Description:     "test",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	// Verify the query finds this subtask.
	var subtasks []model.Task
	err := db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NOT NULL AND assigned_agent_id IS NOT NULL",
		o.projectID, model.StatusInProgress,
	).Find(&subtasks).Error
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	if len(subtasks) != 1 {
		t.Errorf("expected 1 stuck subtask, got %d", len(subtasks))
	}
}

// ---------------------------------------------------------------------------
// resolveFeatureWorktree test
// ---------------------------------------------------------------------------

func TestResolveFeatureWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/bare-repo.git", Default: "main"}
	o := testOrchestrator(t, db, wt)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/my-feature",
	}
	db.Create(&parent)

	subtask := &model.Task{
		ID:           uuid.New(),
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "sub",
		Description:  "test",
		Status:       model.StatusInProgress,
	}
	db.Create(subtask)

	result := o.resolveFeatureWorktree(subtask)
	expected := filepath.Join("/tmp/bare-repo.git", "feature", "my-feature", "integration")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestResolveFeatureWorktree_TopLevelTaskWithBranch verifies that a
// top-level task (ParentTaskID == nil) with its own WorktreeBranch resolves
// to the correct feature integration worktree path. This mirrors the
// branch-resolution logic used by agent_results.go for the normal
// completion path — the reconciler path must agree with it, otherwise
// stuck-agent recovery cannot see commits on top-level tasks and
// mis-routes them to the empty-work failure path.
func TestResolveFeatureWorktree_TopLevelTaskWithBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/bare-repo.git", Default: "main"}
	o := testOrchestrator(t, db, wt)

	task := &model.Task{
		ID:             uuid.New(),
		ParentTaskID:   nil,
		Title:          "top-level",
		Description:    "test",
		WorktreeBranch: "feature/my-feature",
	}

	result := o.resolveFeatureWorktree(task)
	expected := filepath.Join("/tmp/bare-repo.git", "feature", "my-feature", "integration")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestResolveFeatureWorktree_TopLevelTaskWithoutBranch covers the
// truly-degenerate case: a top-level task with no branch recorded at all.
// There is nothing to resolve, so the function must return "".
func TestResolveFeatureWorktree_TopLevelTaskWithoutBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/bare-repo.git", Default: "main"}
	o := testOrchestrator(t, db, wt)

	task := &model.Task{
		ID:           uuid.New(),
		ParentTaskID: nil,
		Title:        "standalone",
		Description:  "test",
	}

	result := o.resolveFeatureWorktree(task)
	if result != "" {
		t.Errorf("expected empty string for top-level task without branch, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// State machine transition tests (integration with TransitionTask)
// ---------------------------------------------------------------------------

func TestTransitionTask_FailedToInProgress(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusFailed,
		UpdatedAt:   time.Now(),
	}

	evt, err := state.TransitionTask(task, model.StatusInProgress, "supervisor", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if task.Status != model.StatusInProgress {
		t.Errorf("expected task status in_progress, got %s", task.Status)
	}
	if evt.OldValue != "failed" {
		t.Errorf("expected old value 'failed', got %s", evt.OldValue)
	}
	if evt.NewValue != "in_progress" {
		t.Errorf("expected new value 'in_progress', got %s", evt.NewValue)
	}
}

func TestTransitionTask_FailedToBacklog(t *testing.T) {
	task := &model.Task{
		ID:          uuid.New(),
		Title:       "test",
		Description: "test",
		Status:      model.StatusFailed,
		UpdatedAt:   time.Now(),
	}

	_, err := state.TransitionTask(task, model.StatusBacklog, "user", nil)
	if err != nil {
		t.Fatalf("expected transition to succeed, got: %v", err)
	}
	if task.Status != model.StatusBacklog {
		t.Errorf("expected task status backlog, got %s", task.Status)
	}
}

// ---------------------------------------------------------------------------
// Agent record verification in scheduleSubtasks
// ---------------------------------------------------------------------------

func TestAgentRecordVerification_MissingAgent(t *testing.T) {
	// This tests that the verification query works correctly.
	db := testutil.NewTestDB(t)
	project := model.Project{
		ID:           uuid.New(),
		Name:         "test-verify",
		BareRepoPath: "/tmp/fake",
	}
	db.Create(&project)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   project.ID,
		Title:       "verify-sub",
		Description: "test",
		Status:      model.StatusBacklog,
	}
	db.Create(&subtask)

	// Query should fail since no agent was created for this task.
	var verifyAgent model.Agent
	err := db.Where("current_task_id = ? AND status = ?",
		subtaskID, model.AgentWorking).First(&verifyAgent).Error
	if err == nil {
		t.Error("expected error when no agent exists for task")
	}
}

func TestAgentRecordVerification_AgentExists(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := model.Project{
		ID:           uuid.New(),
		Name:         "test-verify",
		BareRepoPath: "/tmp/fake",
	}
	db.Create(&project)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   project.ID,
		Title:       "verify-sub",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&subtask)

	// Create an agent with correct task ID and status.
	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     project.ID,
		AgentType:     model.AgentCoder,
		Name:          "test-agent",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
	}
	db.Create(&ag)

	// Query should succeed.
	var verifyAgent model.Agent
	err := db.Where("current_task_id = ? AND status = ?",
		subtaskID, model.AgentWorking).First(&verifyAgent).Error
	if err != nil {
		t.Errorf("expected agent to be found, got error: %v", err)
	}
	if verifyAgent.ID != agentID {
		t.Errorf("expected agent ID %s, got %s", agentID, verifyAgent.ID)
	}
}

// ---------------------------------------------------------------------------
// Completion type reference test (ensures our usage matches agent package)
// ---------------------------------------------------------------------------

func TestCompletionTypeUsage(t *testing.T) {
	// Verify we can create Completion values as used in reconcileStuckAgents.
	comp := agent.Completion{
		AgentID:    uuid.New(),
		ReturnCode: 0,
	}
	if comp.ReturnCode != 0 {
		t.Error("unexpected return code")
	}
}
