package orchestrator

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// testDB creates an in-memory SQLite database with auto-migration.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// testOrchestrator creates an Orchestrator with a test DB and minimal
// dependencies. The worktree manager and runner are set up with dummy paths.
func testOrchestrator(t *testing.T, db *gorm.DB, wtManager *worktree.Manager) *Orchestrator {
	t.Helper()
	projectID := uuid.New()
	events := make(chan Event, 100)
	return &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wtManager,
		events:    events,
		logger:    slog.Default().With("component", "test-orchestrator"),
	}
}

// initBareRepo creates a temporary bare git repo with a default branch
// and a feature branch worktree for testing.
func initBareRepo(t *testing.T) (bareRepoPath string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	bareRepoPath = filepath.Join(tmpDir, "test.git")

	// Create a bare repo.
	runGitCmd(t, tmpDir, "init", "--bare", bareRepoPath)

	// Create a temporary clone to make the initial commit.
	cloneDir := filepath.Join(tmpDir, "clone")
	runGitCmd(t, tmpDir, "clone", bareRepoPath, cloneDir)
	runGitCmd(t, cloneDir, "config", "user.email", "test@test.com")
	runGitCmd(t, cloneDir, "config", "user.name", "Test")

	// Create an initial commit on main.
	initFile := filepath.Join(cloneDir, "init.txt")
	if err := os.WriteFile(initFile, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, cloneDir, "add", ".")
	runGitCmd(t, cloneDir, "commit", "-m", "initial commit")
	runGitCmd(t, cloneDir, "push", "origin", "HEAD:main")

	// Set HEAD to main in the bare repo.
	runGitCmd(t, bareRepoPath, "symbolic-ref", "HEAD", "refs/heads/main")

	return bareRepoPath, func() {}
}

// createFeatureWorktree creates a feature worktree in the bare repo.
func createFeatureWorktree(t *testing.T, bareRepoPath, featureName string) string {
	t.Helper()
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	branchName := "feature/" + featureName
	if err := os.MkdirAll(filepath.Dir(featureDir), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, bareRepoPath, "branch", branchName, "main")
	runGitCmd(t, bareRepoPath, "worktree", "add", featureDir, branchName)
	runGitCmd(t, featureDir, "config", "user.email", "test@test.com")
	runGitCmd(t, featureDir, "config", "user.name", "Test")
	return featureDir
}

// createAgentBranch creates an agent branch off the feature branch
// and optionally adds a commit. Returns the branch name.
func createAgentBranch(t *testing.T, bareRepoPath, featureName, branchName string, addCommit bool) string {
	t.Helper()
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-test")
	featureBranch := "feature/" + featureName

	// Create agent branch from feature branch.
	runGitCmd(t, bareRepoPath, "branch", branchName, featureBranch)
	if err := os.MkdirAll(filepath.Dir(agentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, branchName)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")

	if addCommit {
		testFile := filepath.Join(agentDir, "agent-work.txt")
		if err := os.WriteFile(testFile, []byte("agent work"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, agentDir, "add", ".")
		runGitCmd(t, agentDir, "commit", "-m", "agent work commit")
	}

	// Merge agent branch into feature to simulate already-merged.
	runGitCmd(t, featureDir, "merge", "--no-ff", branchName, "-m", "merge agent")

	return branchName
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// isWorkAlreadyMerged tests
// ---------------------------------------------------------------------------

func TestIsWorkAlreadyMerged_NoAgent(t *testing.T) {
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "test-feature"
	createFeatureWorktree(t, bareRepoPath, featureName)
	agentBranch := createAgentBranch(t, bareRepoPath, featureName, "worktree-agent-test", true)

	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
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

func TestIsWorkAlreadyMerged_BranchDiverged(t *testing.T) {
	bareRepoPath, cleanup := initBareRepo(t)
	defer cleanup()

	featureName := "test-feature-diverge"
	createFeatureWorktree(t, bareRepoPath, featureName)
	featureDir := filepath.Join(bareRepoPath, "feature", featureName, "integration")

	// Create agent branch without merging it into the feature.
	branchName := "worktree-agent-diverged"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", branchName, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-diverged")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, branchName)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")
	testFile := filepath.Join(agentDir, "diverged.txt")
	if err := os.WriteFile(testFile, []byte("diverged work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "diverged commit")

	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: bareRepoPath, DefaultBranch: "main"}
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
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	// Create project.
	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:        parentID,
		ProjectID: o.projectID,
		Title:     "parent",
		Description: "test parent",
		Status:    model.StatusInProgress,
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

func TestCheckFeatureCompletion_MixedFailedAndInProgress(t *testing.T) {
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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

	// Parent should stay in_progress since sub2 is still running.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress, got %s", updated.Status)
	}
}

func TestCheckFeatureCompletion_AllTerminalSomeFailed(t *testing.T) {
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/fake", DefaultBranch: "main"}
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
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/bare-repo.git", DefaultBranch: "main"}
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

func TestResolveFeatureWorktree_NoParent(t *testing.T) {
	db := testDB(t)
	wt := &worktree.Manager{BareRepoPath: "/tmp/bare-repo.git", DefaultBranch: "main"}
	o := testOrchestrator(t, db, wt)

	subtask := &model.Task{
		ID:           uuid.New(),
		ParentTaskID: nil,
		Title:        "standalone",
		Description:  "test",
	}

	result := o.resolveFeatureWorktree(subtask)
	if result != "" {
		t.Errorf("expected empty string for subtask without parent, got %q", result)
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
	db := testDB(t)
	project := model.Project{
		ID:   uuid.New(),
		Name: "test-verify",
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
	db := testDB(t)
	project := model.Project{
		ID:   uuid.New(),
		Name: "test-verify",
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
