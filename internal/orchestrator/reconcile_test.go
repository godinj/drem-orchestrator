package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupReconcileTest creates a test orchestrator backed by a real bare repo,
// DB, and worktree manager. The orchestrator has a project record and is
// ready for reconciliation tests.
func setupReconcileTest(t *testing.T) (*Orchestrator, *gorm.DB, string) {
	t.Helper()
	db := testutil.NewTestDB(t)
	bareRepo := setupTestRepoWithMainBranch(t)

	project := model.Project{
		ID:            uuid.New(),
		Name:          "reconcile-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}
	db.Create(&project)

	host := NewHostManager(bareRepo, "main")
	wt := host.AsInterface()
	orch := testOrchestrator(t, db, wt)
	orch.projectID = project.ID
	// Agent-branch-into-feature merges run via orch.mergeAgentBranchIntoFeature
	// against orch.worktree (the real host-mode worktree manager wired above).
	// Always provide a runner so reconcileStuckAgents doesn't panic.
	// The runner's running map is empty, simulating no active agents.
	orch.runner = agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/nonexistent/claude", "", 0, nil)

	return orch, db, bareRepo
}

// ---------------------------------------------------------------------------
// Reconcile() top-level
// ---------------------------------------------------------------------------

func TestReconcile_NoIssues(t *testing.T) {
	orch, _, _ := setupReconcileTest(t)

	// With no tasks/agents, there should be nothing to reconcile.
	fixes, err := orch.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes on clean state, got %d", fixes)
	}
}

func TestReconcile_AggregatesFixes(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	// Set up a scenario that triggers reconcileStaleSubtasks:
	// A parent with a DONE subtask but no feature branch changes.
	featureName := "agg-test"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "agg-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "agg-agent",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	// A DONE subtask with no actual commits on the feature branch.
	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "agg-sub",
		Description:     "test subtask",
		Status:          model.StatusDone,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
	if fixes < 1 {
		t.Errorf("expected at least 1 fix from stale subtask reconciliation, got %d", fixes)
	}

	// Verify the terminal subtask was annotated, not reopened.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask status done, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared")
	}
}

// ---------------------------------------------------------------------------
// reconcileStaleSubtasks
// ---------------------------------------------------------------------------

func TestReconcileStaleSubtasks_WithCommits(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stale-with-commits"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	// Add a commit to the feature branch so it has changes.
	testFile := filepath.Join(featureDir, "feature-work.txt")
	if err := os.WriteFile(testFile, []byte("feature work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "feature work commit")

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stale-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "stale-agent",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stale-sub-with-commits",
		Description:     "subtask with commits on feature",
		Status:          model.StatusDone,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStaleSubtasks()
	if err != nil {
		t.Fatalf("reconcileStaleSubtasks() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (feature has commits), got %d", fixes)
	}

	// Subtask should remain DONE.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask to stay done, got %s", updated.Status)
	}
}

func TestReconcileStaleSubtasks_NoCommits(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stale-no-commits"
	createFeatureWorktree(t, bareRepo, featureName)
	// No commits added to feature branch — it matches main.

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stale-parent-no-commits",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "stale-agent-no",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stale-sub-no-commits",
		Description:     "subtask done but no feature changes",
		Status:          model.StatusDone,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStaleSubtasks()
	if err != nil {
		t.Fatalf("reconcileStaleSubtasks() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix, got %d", fixes)
	}

	// DONE is terminal: no-diff reconciliation must not reopen the child.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask status done, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared")
	}
	if got := updated.Context["reconcile_reason"]; got != "subtask was done but feature branch has no changes; terminal status preserved" {
		t.Errorf("unexpected reconcile_reason: %v", got)
	}
}

func TestReconcileStaleSubtasks_NoParentBranch(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stale-parent-no-branch",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "", // empty — should be skipped
	}
	db.Create(&parent)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "stale-agent-nb",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stale-sub-no-branch",
		Description:     "subtask under branchless parent",
		Status:          model.StatusDone,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStaleSubtasks()
	if err != nil {
		t.Fatalf("reconcileStaleSubtasks() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (parent has no branch), got %d", fixes)
	}

	// Subtask should remain DONE.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask to stay done, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// reconcileOrphanWorktrees
// ---------------------------------------------------------------------------

func TestReconcileOrphanWorktrees_AllActive(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-wt-active"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create an agent worktree via the manager.
	agentWT, err := orch.worktree.CreateAgentWorktree(featureName)
	if err != nil {
		t.Fatalf("CreateAgentWorktree: %v", err)
	}

	// Create a parent task referencing the feature.
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "orphan-wt-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Create a WORKING agent that corresponds to the worktree.
	ag := model.Agent{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "active-wt-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: agentWT.Branch,
	}
	db.Create(&ag)

	fixes, err := orch.reconcileOrphanWorktrees()
	if err != nil {
		t.Fatalf("reconcileOrphanWorktrees() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (all worktrees have active agents), got %d", fixes)
	}
}

func TestReconcileOrphanWorktrees_StaleDir(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-wt-stale"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create an agent worktree — no corresponding agent in DB.
	agentWT, err := orch.worktree.CreateAgentWorktree(featureName)
	if err != nil {
		t.Fatalf("CreateAgentWorktree: %v", err)
	}

	// Verify the worktree directory exists.
	if _, statErr := os.Stat(agentWT.Path); statErr != nil {
		t.Fatalf("agent worktree should exist: %v", statErr)
	}

	// Create a parent task referencing the feature.
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "orphan-wt-stale-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)
	// No agent record — so the worktree is orphaned.

	fixes, err := orch.reconcileOrphanWorktrees()
	if err != nil {
		t.Fatalf("reconcileOrphanWorktrees() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for stale worktree, got %d", fixes)
	}
}

// ---------------------------------------------------------------------------
// reconcileStuckAgents
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// reconcileOrphanWorktrees — orphan worktree guard
// ---------------------------------------------------------------------------

// createAgentWorktree creates a worktree at feature/<featureName>/agent-<agentSuffix>
// on the given branch. If addCommit is true, an extra commit is added ahead of the
// feature branch. Unlike createAgentBranch, this does NOT merge the agent branch
// into the feature — it leaves it "orphan" for reconciliation testing.
func createAgentWorktree(t *testing.T, bareRepoPath, featureName, agentSuffix, branchName string, addCommit bool) string {
	t.Helper()
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-"+agentSuffix)
	featureBranch := "feature/" + featureName

	// Create branch from the feature branch.
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

	return agentDir
}

func TestReconcileOrphanWorktrees_RemovesEmptyAgentWorktree(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-wt-empty"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create an agent worktree with no extra commits (same as feature branch).
	agentBranch := "worktree-agent-empty1"
	createAgentWorktree(t, bareRepo, featureName, "empty1", agentBranch, false)

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "orphan-wt-parent",
		Description:    "parent with orphan agent worktree",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	fixes, err := orch.reconcileOrphanWorktrees()
	if err != nil {
		t.Fatalf("reconcileOrphanWorktrees() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix (orphan empty agent worktree removed), got %d", fixes)
	}

	// Verify the agent branch was deleted.
	_, branchErr := testutil.RunGit([]string{"rev-parse", "--verify", agentBranch}, bareRepo)
	if branchErr == nil {
		t.Errorf("expected agent branch %q to be deleted, but it still exists", agentBranch)
	}
}

func TestReconcileOrphanWorktrees_SkipsNonAgentBranch(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-wt-noagent"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create a worktree in the agent directory structure, but with a
	// non-agent branch name. This simulates the bug where
	// ListAgentWorktrees resolves HEAD to the default branch.
	nonAgentBranch := "rogue-branch"
	rogueDir := createAgentWorktree(t, bareRepo, featureName, "rogue", nonAgentBranch, false)

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "orphan-wt-noagent-parent",
		Description:    "parent with rogue non-agent worktree",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	fixes, err := orch.reconcileOrphanWorktrees()
	if err != nil {
		t.Fatalf("reconcileOrphanWorktrees() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (non-agent branch must be skipped), got %d", fixes)
	}

	// Verify the non-agent branch still exists.
	_, branchErr := testutil.RunGit([]string{"rev-parse", "--verify", nonAgentBranch}, bareRepo)
	if branchErr != nil {
		t.Errorf("non-agent branch %q was deleted — reconciler must not remove non-agent branches", nonAgentBranch)
	}

	// Verify the worktree directory still exists.
	if _, err := os.Stat(rogueDir); os.IsNotExist(err) {
		t.Errorf("rogue worktree directory was removed — reconciler must not remove non-agent worktrees")
	}
}

func TestReconcileOrphanWorktrees_PreservesAgentWithCommits(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-wt-commits"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create an agent worktree WITH commits ahead of the feature branch.
	agentBranch := "worktree-agent-haswork"
	agentDir := createAgentWorktree(t, bareRepo, featureName, "haswork", agentBranch, true)

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "orphan-wt-commits-parent",
		Description:    "parent with agent that has real work",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	fixes, err := orch.reconcileOrphanWorktrees()
	if err != nil {
		t.Fatalf("reconcileOrphanWorktrees() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (agent has commits), got %d", fixes)
	}

	// Verify the agent branch still exists.
	_, branchErr := testutil.RunGit([]string{"rev-parse", "--verify", agentBranch}, bareRepo)
	if branchErr != nil {
		t.Errorf("agent branch %q was deleted — should be preserved (has commits)", agentBranch)
	}

	// Verify the worktree directory still exists.
	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		t.Errorf("agent worktree directory was removed — should be preserved (has commits)")
	}
}

// ---------------------------------------------------------------------------
// Integration: end-to-end protection against master/main deletion
// ---------------------------------------------------------------------------

// TestReconcile_ProtectsMainWorktreeFromOrphanCleanup reproduces the exact
// observed failure: an agent worktree whose branch was already deleted causes
// reconcileOrphanWorktrees to misidentify and destroy the default branch.
//
// Scenario:
//  1. Bare repo with a main worktree checked out
//  2. Feature with an agent worktree
//  3. Agent branch deleted (simulating prior cleanup that left the directory)
//  4. DB records: parent task with worktree_branch, no WORKING agents
//  5. Full Reconcile() runs
//
// Asserts: main worktree dir exists, main branch exists, HEAD → main, no error.
func TestReconcile_ProtectsMainWorktreeFromOrphanCleanup(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	// 1. Create a main worktree (simulates production layout where the
	//    default branch has a checked-out worktree in the bare repo).
	mainWorktreeDir := filepath.Join(bareRepo, "main")
	runGitCmd(t, bareRepo, "worktree", "add", mainWorktreeDir, "main")

	// 2. Create a feature with an agent worktree.
	featureName := "e2e-master-protect"
	createFeatureWorktree(t, bareRepo, featureName)
	agentBranch := "worktree-agent-deadbeef"
	agentDir := createAgentWorktree(t, bareRepo, featureName, "deadbeef", agentBranch, false)

	// 3. Delete the agent branch (simulating a previous cleanup that removed
	//    the branch but left the worktree directory orphaned).
	//    First detach HEAD in the agent worktree so the branch can be deleted.
	headCommit := runGitCmd(t, agentDir, "rev-parse", "HEAD")
	runGitCmd(t, agentDir, "checkout", "--detach", headCommit)
	runGitCmd(t, bareRepo, "branch", "-D", agentBranch)

	// 4. Create DB records: parent task referencing the feature, no working agents.
	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "e2e-protect-parent",
		Description:    "parent task for e2e master protection test",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// 5. Run the full Reconcile() — this is the code path that previously
	//    destroyed the master worktree.
	fixes, err := orch.Reconcile()

	// 6. Verify: no error.
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	// The orphaned agent worktree has a detached HEAD with no agent branch,
	// so ListAgentWorktrees should skip it entirely — expect 0 fixes from
	// orphan cleanup.
	_ = fixes

	// Verify: main worktree directory still exists on disk.
	if _, statErr := os.Stat(mainWorktreeDir); os.IsNotExist(statErr) {
		t.Fatal("main worktree directory was deleted — this is the critical bug")
	}

	// Verify: main branch still exists.
	_, branchErr := testutil.RunGit([]string{"rev-parse", "--verify", "refs/heads/main"}, bareRepo)
	if branchErr != nil {
		t.Fatal("main branch was deleted from bare repo — this is the critical bug")
	}

	// Verify: bare repo HEAD still points to the default branch.
	headRef := runGitCmd(t, bareRepo, "symbolic-ref", "--short", "HEAD")
	if headRef != "main" {
		t.Errorf("expected HEAD to point to main, got %q", headRef)
	}
}

// ---------------------------------------------------------------------------
// reconcileStuckAgents — top-level task recovery (classifying, planning, test_writing)
// ---------------------------------------------------------------------------
// These tests verify that reconcileStuckAgents detects dead agents on
// top-level tasks (parent_task_id IS NULL) across all actionable statuses.
// They should FAIL against the current implementation which only queries
// StatusInProgress subtasks with parent_task_id IS NOT NULL.

// ---------------------------------------------------------------------------
// reconcileOrphanedTaskAssignments — idle/dead agent with stale task assignment
// ---------------------------------------------------------------------------

func TestReconcileOrphanedTaskAssignments_IdleAgent(t *testing.T) {
	// An agent completed (IDLE) but the task's assigned_agent_id was not
	// cleared — this is the exact bug that stranded task 5936f2aa.
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentClassifier,
		Name:      "idle-orphan",
		Status:    model.AgentIdle, // completed, now idle
	}
	db.Create(&ag)

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "orphaned-classifying",
		Description:     "task stuck with idle agent",
		Status:          model.StatusClassifying,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	fixes, err := orch.reconcileOrphanedTaskAssignments()
	if err != nil {
		t.Fatalf("reconcileOrphanedTaskAssignments() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for idle agent, got %d", fixes)
	}

	// Task should have assigned_agent_id cleared.
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned_agent_id to be cleared")
	}
	// Status should remain classifying so processClassifyingTasks re-dispatches.
	if updated.Status != model.StatusClassifying {
		t.Errorf("expected status classifying, got %s", updated.Status)
	}
}

func TestReconcileOrphanedTaskAssignments_DeadAgent(t *testing.T) {
	// Agent died but task still references it as assigned.
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentPlanner,
		Name:      "dead-orphan",
		Status:    model.AgentDead,
	}
	db.Create(&ag)

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "orphaned-planning",
		Description:     "task stuck with dead agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	fixes, err := orch.reconcileOrphanedTaskAssignments()
	if err != nil {
		t.Fatalf("reconcileOrphanedTaskAssignments() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead agent, got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned_agent_id to be cleared")
	}
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status planning, got %s", updated.Status)
	}
}

func TestReconcileOrphanedTaskAssignments_MissingAgent(t *testing.T) {
	// Agent record doesn't exist in DB at all.
	orch, db, _ := setupReconcileTest(t)

	missingAgentID := uuid.New() // no agent record created

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "orphaned-missing-agent",
		Description:     "task stuck with nonexistent agent",
		Status:          model.StatusClassifying,
		AssignedAgentID: &missingAgentID,
	}
	db.Create(&task)

	fixes, err := orch.reconcileOrphanedTaskAssignments()
	if err != nil {
		t.Fatalf("reconcileOrphanedTaskAssignments() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for missing agent, got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned_agent_id to be cleared")
	}
}

func TestReconcileOrphanedTaskAssignments_SkipsWorkingAgent(t *testing.T) {
	// A WORKING agent should NOT have its assignment cleared — that's handled
	// by reconcileStuckAgents.
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentClassifier,
		Name:      "working-agent",
		Status:    model.AgentWorking,
	}
	db.Create(&ag)

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "task-with-working-agent",
		Description:     "should not be touched",
		Status:          model.StatusClassifying,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	fixes, err := orch.reconcileOrphanedTaskAssignments()
	if err != nil {
		t.Fatalf("reconcileOrphanedTaskAssignments() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes for working agent, got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID == nil || *updated.AssignedAgentID != agentID {
		t.Error("expected assigned_agent_id to remain set for working agent")
	}
}

func TestReconcileOrphanedTaskAssignments_SkipsBlockedAgent(t *testing.T) {
	// A BLOCKED agent is legitimately waiting — assignment should not be cleared.
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "blocked-agent",
		Status:    model.AgentBlocked,
	}
	db.Create(&ag)

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "task-with-blocked-agent",
		Description:     "should not be touched",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	fixes, err := orch.reconcileOrphanedTaskAssignments()
	if err != nil {
		t.Fatalf("reconcileOrphanedTaskAssignments() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes for blocked agent, got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID == nil || *updated.AssignedAgentID != agentID {
		t.Error("expected assigned_agent_id to remain set for blocked agent")
	}
}

func TestReconcileOrphanedTaskAssignments_MultipleStatuses(t *testing.T) {
	// Verify orphaned assignments are cleared across classifying, planning,
	// test_writing, and in_progress statuses in one pass.
	orch, db, _ := setupReconcileTest(t)

	createOrphaned := func(status model.TaskStatus, agentStatus model.AgentStatus, title string) {
		agentID := uuid.New()
		db.Create(&model.Agent{
			ID:        agentID,
			ProjectID: orch.projectID,
			AgentType: model.AgentClassifier,
			Name:      title + "-agent",
			Status:    agentStatus,
		})
		db.Create(&model.Task{
			ID:              uuid.New(),
			ProjectID:       orch.projectID,
			Title:           title,
			Description:     "orphaned",
			Status:          status,
			AssignedAgentID: &agentID,
		})
	}

	createOrphaned(model.StatusClassifying, model.AgentIdle, "cls-orphan")
	createOrphaned(model.StatusPlanning, model.AgentDead, "plan-orphan")
	createOrphaned(model.StatusTestWriting, model.AgentIdle, "tw-orphan")
	createOrphaned(model.StatusInProgress, model.AgentDead, "ip-orphan")

	fixes, err := orch.reconcileOrphanedTaskAssignments()
	if err != nil {
		t.Fatalf("reconcileOrphanedTaskAssignments() error: %v", err)
	}
	if fixes != 4 {
		t.Errorf("expected 4 fixes across all statuses, got %d", fixes)
	}
}

// ---------------------------------------------------------------------------
// cleanupOrphanedAssignments — startup cleanup
// ---------------------------------------------------------------------------

func TestCleanupOrphanedAssignments_ClearsIdleAgent(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentClassifier,
		Name:      "startup-idle",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "startup-cleanup-test",
		Description:     "task with stale assignment at startup",
		Status:          model.StatusClassifying,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	orch.cleanupOrphanedAssignments()

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID != nil {
		t.Error("expected startup cleanup to clear assigned_agent_id for idle agent")
	}
}

func TestCleanupOrphanedAssignments_ClearsMissingAgent(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	missingAgentID := uuid.New()

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "startup-missing-agent",
		Description:     "task referencing deleted agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &missingAgentID,
	}
	db.Create(&task)

	orch.cleanupOrphanedAssignments()

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID != nil {
		t.Error("expected startup cleanup to clear assigned_agent_id for missing agent")
	}
}

func TestCleanupOrphanedAssignments_SkipsTerminalStatuses(t *testing.T) {
	// Tasks in terminal statuses (done, failed) should not be affected.
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "startup-terminal",
		Status:    model.AgentIdle,
	}
	db.Create(&ag)

	taskID := uuid.New()
	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "done-task-with-agent",
		Description:     "should not be touched",
		Status:          model.StatusDone,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	orch.cleanupOrphanedAssignments()

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.AssignedAgentID == nil {
		t.Error("startup cleanup should not touch tasks in terminal statuses")
	}
}

// ---------------------------------------------------------------------------
// reconcileStuckAgents — multiple statuses (moved below new tests)
// ---------------------------------------------------------------------------
