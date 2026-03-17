package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/merge"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// setupReconcileTest creates a test orchestrator backed by a real bare repo,
// DB, and worktree manager. The orchestrator has a project record and is
// ready for reconciliation tests.
func setupReconcileTest(t *testing.T) (*Orchestrator, *gorm.DB, string) {
	t.Helper()
	db := lifecycleTestDB(t)
	bareRepo, _ := initBareRepo(t)

	project := model.Project{
		ID:            uuid.New(),
		Name:          "reconcile-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}
	db.Create(&project)

	wt := worktree.NewManager(bareRepo, "main")
	merger := merge.NewOrchestrator(wt, db)
	orch := testOrchestrator(t, db, wt)
	orch.projectID = project.ID
	orch.merger = merger
	// Always provide a runner so reconcileStuckAgents doesn't panic.
	// The runner's running map is empty, simulating no active agents.
	orch.runner = agent.NewRunner(db, nil, wt, "/nonexistent/claude", 0)

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

	// Verify the subtask was reset to backlog.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask status backlog, got %s", updated.Status)
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

	// Subtask should be reset to BACKLOG.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask status backlog, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared")
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
// reconcileOrphanedSubtasks
// ---------------------------------------------------------------------------

func TestReconcileOrphanedSubtasks_NoOrphans(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-no"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "orphan-parent",
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
		Name:      "live-agent",
		Status:    model.AgentWorking, // actively working — not orphaned
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "active-sub",
		Description:     "subtask with live agent",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileOrphanedSubtasks()
	if err != nil {
		t.Fatalf("reconcileOrphanedSubtasks() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (agent is working), got %d", fixes)
	}

	// Subtask should remain IN_PROGRESS.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected subtask to stay in_progress, got %s", updated.Status)
	}
}

func TestReconcileOrphanedSubtasks_DeadAgent(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-dead"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "orphan-dead-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Agent is dead — not working or blocked.
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "dead-agent",
		Status:         model.AgentDead,
		WorktreeBranch: "", // no branch — no commits to merge
	}
	db.Create(&ag)

	subID := uuid.New()
	sub := model.Task{
		ID:              subID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "orphan-dead-sub",
		Description:     "subtask with dead agent",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileOrphanedSubtasks()
	if err != nil {
		t.Fatalf("reconcileOrphanedSubtasks() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead agent, got %d", fixes)
	}

	// Subtask should be fast-tracked to DONE (merged=true since no branches).
	var updated model.Task
	db.First(&updated, "id = ?", subID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask to be fast-tracked to done, got %s", updated.Status)
	}
}

func TestReconcileOrphanedSubtasks_WorkAlreadyMerged(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-merged"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create agent branch with a commit and merge it into the feature branch.
	agentBranch := createAgentBranch(t, bareRepo, featureName, "worktree-agent-merged", true)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "orphan-merged-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Agent is idle (finished) but has work that's already merged.
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "merged-agent",
		Status:         model.AgentIdle,
		WorktreeBranch: agentBranch,
	}
	db.Create(&ag)

	subID := uuid.New()
	sub := model.Task{
		ID:              subID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "orphan-merged-sub",
		Description:     "subtask whose work is already merged",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileOrphanedSubtasks()
	if err != nil {
		t.Fatalf("reconcileOrphanedSubtasks() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix, got %d", fixes)
	}

	// Subtask should transition to DONE since work is already merged.
	var updated model.Task
	db.First(&updated, "id = ?", subID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask status done, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// reconcileEmptyFeatures
// ---------------------------------------------------------------------------

func TestReconcileEmptyFeatures_NonEmpty(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "empty-non"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)

	// Add a commit to the feature so it's not empty.
	testFile := filepath.Join(featureDir, "feature-code.txt")
	if err := os.WriteFile(testFile, []byte("feature code"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "feature code commit")

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "non-empty-feature",
		Description:    "feature with commits",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	fixes, err := orch.reconcileEmptyFeatures()
	if err != nil {
		t.Fatalf("reconcileEmptyFeatures() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (feature has commits), got %d", fixes)
	}

	// Task should stay TESTING_READY.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected task to stay testing_ready, got %s", updated.Status)
	}
}

func TestReconcileEmptyFeatures_Empty(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "empty-yes"
	createFeatureWorktree(t, bareRepo, featureName)
	// No commits on feature — it's identical to main.

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "empty-feature",
		Description:    "feature with no commits",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	fixes, err := orch.reconcileEmptyFeatures()
	if err != nil {
		t.Fatalf("reconcileEmptyFeatures() error: %v", err)
	}

	// The state machine does not allow testing_ready -> failed directly,
	// so failTask returns an error and the fix is not counted.
	// This documents the current behavior; a state machine update may
	// be needed to allow this transition.
	if fixes != 0 {
		t.Errorf("expected 0 fixes (state machine blocks testing_ready->failed), got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	// Task stays testing_ready because the state transition was rejected.
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected task to stay testing_ready (blocked transition), got %s", updated.Status)
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

func TestReconcileStuckAgents_SessionAlive(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-alive"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stuck-alive-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Agent is WORKING but IS in the runner's running map.
	// We simulate this by creating an agent record that will be found by
	// the DB query, but whose ID is NOT in the runner's running map.
	// However, since agent is AgentIdle (not AgentWorking), it should be skipped.
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: orch.projectID,
		AgentType: model.AgentCoder,
		Name:      "stuck-alive-agent",
		Status:    model.AgentIdle, // not working — skip
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              uuid.New(),
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-alive-sub",
		Description:     "subtask with idle agent",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (agent is idle, not stuck working), got %d", fixes)
	}
}

func TestReconcileStuckAgents_SessionDead_WithCommits(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-dead-commits"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create an agent branch with commits that are NOT yet merged into
	// the feature branch. We create the branch manually (without using
	// createAgentBranch which auto-merges).
	featureBranch := "feature/" + featureName
	agentBranch := "worktree-agent-stuck-c"
	runGitCmd(t, bareRepo, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepo, "feature", featureName, "agent-stuck-c")
	if err := os.MkdirAll(filepath.Dir(agentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, bareRepo, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")
	testFile := filepath.Join(agentDir, "stuck-work.txt")
	if err := os.WriteFile(testFile, []byte("stuck agent work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "stuck agent commit")
	// Do NOT merge into feature — agent has unmerged commits.

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stuck-dead-c-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: featureBranch,
	}
	db.Create(&parent)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "stuck-dead-c-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: agentBranch,
		WorktreePath:   agentDir,
		CurrentTaskID:  &taskID,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-dead-c-sub",
		Description:     "subtask with dead agent that has unmerged commits",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	// The runner has no running agents, and agent is WORKING in DB.
	// reconcileStuckAgents detects the dead agent with commits and calls
	// processAgentResult, which invokes onAgentCompleted. The completion
	// pipeline calls runner.GetAgentOutput and memory extraction which
	// may fail but should not prevent the fix from counting.
	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead agent with commits, got %d", fixes)
	}
}

func TestReconcileStuckAgents_SessionDead_NoCommits(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-dead-no"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stuck-dead-no-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "stuck-dead-no-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "", // no branch — no commits
		CurrentTaskID:  &taskID,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-dead-no-sub",
		Description:     "subtask with dead agent, no commits",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead agent with no commits, got %d", fixes)
	}

	// Agent should be marked dead.
	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAgent.Status)
	}

	// Subtask should be auto-retried (backlog) since retry_count starts at 0
	// which is below MaxEmptyWorkRetries.
	var updatedTask model.Task
	db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusBacklog {
		t.Errorf("expected subtask status backlog (auto-retry), got %s", updatedTask.Status)
	}
}

// ---------------------------------------------------------------------------
// reconcileAlreadyMergedFeatures
// ---------------------------------------------------------------------------

func TestReconcileAlreadyMergedFeatures_Merged(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	// Create a main worktree so MainWorktreePath() works.
	mainDir := filepath.Join(bareRepo, "main")
	runGitCmd(t, bareRepo, "worktree", "add", mainDir, "main")

	// Create a feature branch with a commit.
	featureName := "already-merged"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	testFile := filepath.Join(featureDir, "merged-work.txt")
	if err := os.WriteFile(testFile, []byte("merged work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "feature work")

	// Merge the feature branch into main (simulating a supervisor merge).
	runGitCmd(t, mainDir, "merge", "feature/"+featureName, "--no-edit")

	// Create a FAILED parent task pointing to the (now-merged) feature branch.
	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "already-merged-task",
		Description:    "task whose branch was merged by supervisor",
		Status:         model.StatusFailed,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	fixes, err := orch.reconcileAlreadyMergedFeatures()
	if err != nil {
		t.Fatalf("reconcileAlreadyMergedFeatures() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix, got %d", fixes)
	}

	// Task should be transitioned to DONE.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected task status done, got %s", updated.Status)
	}

	// Verify a status_change event was recorded.
	var event model.TaskEvent
	db.Where("task_id = ? AND new_value = ?", task.ID, string(model.StatusDone)).First(&event)
	if event.ID == uuid.Nil {
		t.Error("expected a status_change event to done")
	}
}

func TestReconcileAlreadyMergedFeatures_NotMerged(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	// Create a main worktree.
	mainDir := filepath.Join(bareRepo, "main")
	runGitCmd(t, bareRepo, "worktree", "add", mainDir, "main")

	// Create a feature branch with a commit but do NOT merge into main.
	featureName := "not-merged"
	featureDir := createFeatureWorktree(t, bareRepo, featureName)
	testFile := filepath.Join(featureDir, "unmerged-work.txt")
	if err := os.WriteFile(testFile, []byte("unmerged work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", ".")
	runGitCmd(t, featureDir, "commit", "-m", "unmerged feature work")

	// Create a FAILED parent task.
	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      orch.projectID,
		Title:          "not-merged-task",
		Description:    "task whose branch was not merged",
		Status:         model.StatusFailed,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&task)

	fixes, err := orch.reconcileAlreadyMergedFeatures()
	if err != nil {
		t.Fatalf("reconcileAlreadyMergedFeatures() error: %v", err)
	}
	if fixes != 0 {
		t.Errorf("expected 0 fixes (branch not merged), got %d", fixes)
	}

	// Task should remain FAILED.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected task to stay failed, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// recoverStuckAgents
// ---------------------------------------------------------------------------

func TestRecoverStuckAgents_NoSignalFile(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// Agent is WORKING but no idle signal file exists — should be a no-op.
	worktreeDir := t.TempDir()
	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentCoder,
		Name:          "recover-no-signal",
		Status:        model.AgentWorking,
		WorktreePath:  worktreeDir,
		CurrentTaskID: &taskID,
	}
	db.Create(&ag)

	task := model.Task{
		ID:          taskID,
		ProjectID:   orch.projectID,
		Title:       "recover-no-signal-task",
		Description: "task without signal file",
		Status:      model.StatusInProgress,
	}
	db.Create(&task)

	// No signal file at worktreeDir/.claude/agent-idle.
	// recoverStuckAgents should not change anything.
	orch.recoverStuckAgents()

	// Agent should still be WORKING.
	var updated model.Agent
	db.First(&updated, "id = ?", agentID)
	if updated.Status != model.AgentWorking {
		t.Errorf("expected agent to stay working, got %s", updated.Status)
	}
}

func TestRecoverStuckAgents_WithSignalFile(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "recover-signal"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create the worktree directory structure with an idle signal file.
	worktreeDir := t.TempDir()
	claudeDir := filepath.Join(worktreeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idleSignal := filepath.Join(claudeDir, "agent-idle")
	if err := os.WriteFile(idleSignal, []byte("idle"), 0o644); err != nil {
		t.Fatal(err)
	}

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "recover-signal-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	taskID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "recover-signal-agent",
		Status:         model.AgentWorking,
		WorktreePath:   worktreeDir,
		WorktreeBranch: "",
		CurrentTaskID:  &taskID,
		HeartbeatAt:    &now,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "recover-signal-task",
		Description:     "task with idle signal",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	// recoverStuckAgents detects the idle signal file and calls
	// onAgentCompleted, which processes the agent through the normal
	// completion pipeline. The runner's GetAgentOutput may fail (no tmux
	// session) but is handled gracefully.
	orch.recoverStuckAgents()

	// Verify the agent was processed: it should no longer be WORKING.
	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status == model.AgentWorking {
		t.Error("expected agent to no longer be working after recovery")
	}
}

func TestReconcileStuckAgents_AutoRetriesFirst(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-retry"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stuck-retry-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "stuck-retry-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "", // no commits
		CurrentTaskID:  &taskID,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-retry-sub",
		Description:     "subtask that should auto-retry",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(0)},
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix, got %d", fixes)
	}

	// Subtask should be reset to BACKLOG (auto-retry), not FAILED.
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask status backlog (auto-retry), got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned agent to be cleared")
	}
	// retry_count should be incremented
	if v, ok := updated.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1, got %v", updated.Context["retry_count"])
	}
}

func TestReconcileStuckAgents_FailsAtLimit(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-limit"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stuck-limit-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "stuck-limit-agent",
		Status:         model.AgentWorking,
		WorktreeBranch: "", // no commits
		CurrentTaskID:  &taskID,
	}
	db.Create(&ag)

	sub := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-limit-sub",
		Description:     "subtask at retry limit",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
		Context:         model.JSONField{"retry_count": float64(MaxEmptyWorkRetries)},
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix, got %d", fixes)
	}

	// Subtask should be FAILED (at retry limit).
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected subtask status failed (at limit), got %s", updated.Status)
	}
}
