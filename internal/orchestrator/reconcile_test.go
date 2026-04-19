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
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
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

	wt := worktree.NewManager(bareRepo, "main")
	merger := merge.NewOrchestrator(wt, db)
	orch := testOrchestrator(t, db, wt)
	orch.projectID = project.ID
	orch.merger = merger
	// Always provide a runner so reconcileStuckAgents doesn't panic.
	// The runner's running map is empty, simulating no active agents.
	orch.runner = agent.NewRunner(db, nil, wt, "/nonexistent/claude", "", 0, nil)

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

	// Subtask should fast-track to DONE (matches onAgentCompleted behavior).
	var updated model.Task
	db.First(&updated, "id = ?", subID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask to reach done, got %s", updated.Status)
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

	// Subtask should fast-track to DONE (matches onAgentCompleted behavior).
	var updated model.Task
	db.First(&updated, "id = ?", subID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask status done, got %s", updated.Status)
	}
}

func TestReconcileOrphanedSubtasks_FastTracksToDone(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "orphan-gate"
	createFeatureWorktree(t, bareRepo, featureName)

	// Create agent branch with a commit and merge it into the feature branch.
	agentBranch := createAgentBranch(t, bareRepo, featureName, "worktree-agent-gate", true)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "orphan-gate-parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "gate-agent",
		Status:         model.AgentIdle,
		WorktreeBranch: agentBranch,
	}
	db.Create(&ag)

	subID := uuid.New()
	sub := model.Task{
		ID:              subID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "orphan-gate-sub",
		Description:     "subtask that must not bypass quality gate",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	_, err := orch.reconcileOrphanedSubtasks()
	if err != nil {
		t.Fatalf("reconcileOrphanedSubtasks() error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", subID)

	// Subtask should fast-track to done (matches onAgentCompleted / scheduleSubtasks).
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask to reach done, got %s", updated.Status)
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
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

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
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

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

	// Backdate CreatedAt past the grace period so recovery can act on it.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

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

// TestRecoverStuckAgents_GracePeriod_SkipsNewAgent verifies that a freshly-
// spawned agent with a stale idle signal file is NOT recovered during the
// grace period. This is the race condition fix: a new agent in a worktree
// that has a leftover agent-idle file from a previous agent should not be
// immediately treated as stuck.
func TestRecoverStuckAgents_GracePeriod_SkipsNewAgent(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// Create the worktree directory structure with a stale idle signal file
	// (simulating a leftover from a previous agent).
	worktreeDir := t.TempDir()
	claudeDir := filepath.Join(worktreeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idleSignal := filepath.Join(claudeDir, "agent-idle")
	if err := os.WriteFile(idleSignal, []byte("idle"), 0o644); err != nil {
		t.Fatal(err)
	}

	agentID := uuid.New()
	taskID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentClassifier,
		Name:          "freshly-spawned-classifier",
		Status:        model.AgentWorking,
		WorktreePath:  worktreeDir,
		CurrentTaskID: &taskID,
		HeartbeatAt:   &now,
		// CreatedAt is set by GORM to time.Now() — within the grace period.
	}
	db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "fresh-task",
		Description:     "task assigned to freshly spawned agent",
		Status:          model.StatusClassifying,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	// recoverStuckAgents should skip this agent because it was just created
	// (within the grace period), even though the idle signal file exists.
	orch.recoverStuckAgents()

	// Agent should still be WORKING — not recovered.
	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentWorking {
		t.Errorf("expected freshly-spawned agent to stay WORKING during grace period, got %s", updatedAgent.Status)
	}

	// Task should still be CLASSIFYING — not parked.
	var updatedTask model.Task
	db.First(&updatedTask, "id = ?", taskID)
	if updatedTask.Status != model.StatusClassifying {
		t.Errorf("expected task to stay CLASSIFYING during grace period, got %s", updatedTask.Status)
	}
	if updatedTask.Context != nil {
		if _, parked := updatedTask.Context["human_triage"]; parked {
			t.Error("task should NOT be parked for triage during grace period")
		}
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
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

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
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

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

func TestReconcileStuckAgents_TopLevel_Classifying(t *testing.T) {
	orch, db, _ := setupReconcileTest(t)

	// Top-level task in classifying status with a dead classifier agent.
	// Classifier agents work in the bare repo — no worktree branch.
	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentClassifier,
		Name:          "dead-classifier",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
	}
	db.Create(&ag)
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "top-level-classifying",
		Description:     "task stuck in classifying with dead agent",
		Status:          model.StatusClassifying,
		AssignedAgentID: &agentID,
		// No ParentTaskID — this is a top-level task.
		// No WorktreeBranch — classifier operates in bare repo.
	}
	db.Create(&task)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead classifier on top-level task, got %d", fixes)
	}

	// Task should keep classifying status (not reset to backlog) so that
	// processClassifyingTasks re-dispatches a new classifier agent.
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusClassifying {
		t.Errorf("expected task to remain in classifying status, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned_agent_id to be cleared")
	}
	// retry_count should be incremented.
	if updated.Context == nil {
		t.Fatal("expected Context to be set with retry_count")
	}
	if v, ok := updated.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1, got %v", updated.Context["retry_count"])
	}

	// Agent should be marked dead.
	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAgent.Status)
	}
}

func TestReconcileStuckAgents_TopLevel_Planning(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	// Top-level task in planning status with a dead planner agent.
	// Planner agents do use worktrees.
	featureName := "stuck-planning"
	createFeatureWorktree(t, bareRepo, featureName)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentPlanner,
		Name:           "dead-planner",
		Status:         model.AgentWorking,
		WorktreeBranch: "",
		CurrentTaskID:  &taskID,
	}
	db.Create(&ag)
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "top-level-planning",
		Description:     "task stuck in planning with dead agent",
		Status:          model.StatusPlanning,
		AssignedAgentID: &agentID,
		WorktreeBranch:  "feature/" + featureName,
		// No ParentTaskID — top-level task.
	}
	db.Create(&task)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead planner on top-level task, got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected task to remain in planning status, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned_agent_id to be cleared")
	}
	if updated.Context == nil {
		t.Fatal("expected Context to be set with retry_count")
	}
	if v, ok := updated.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1, got %v", updated.Context["retry_count"])
	}

	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAgent.Status)
	}
}

func TestReconcileStuckAgents_Subtask_TestWriting(t *testing.T) {
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-testwrite"
	createFeatureWorktree(t, bareRepo, featureName)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "stuck-testwrite-parent",
		Description:    "test parent",
		Status:         model.StatusTestWriting,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "dead-testwriter",
		Status:         model.AgentWorking,
		WorktreeBranch: "",
		CurrentTaskID:  &taskID,
	}
	db.Create(&ag)
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	sub := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "stuck-testwrite-sub",
		Description:     "subtask stuck in test_writing with dead agent",
		Status:          model.StatusTestWriting,
		AssignedAgentID: &agentID,
	}
	db.Create(&sub)

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 1 {
		t.Errorf("expected 1 fix for dead agent on test_writing subtask, got %d", fixes)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusTestWriting {
		t.Errorf("expected subtask to remain in test_writing status, got %s", updated.Status)
	}
	if updated.AssignedAgentID != nil {
		t.Error("expected assigned_agent_id to be cleared")
	}
	if updated.Context == nil {
		t.Fatal("expected Context to be set with retry_count")
	}
	if v, ok := updated.Context["retry_count"].(float64); !ok || int(v) != 1 {
		t.Errorf("expected retry_count=1, got %v", updated.Context["retry_count"])
	}

	var updatedAgent model.Agent
	db.First(&updatedAgent, "id = ?", agentID)
	if updatedAgent.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAgent.Status)
	}
}

func TestReconcileStuckAgents_TopLevel_Classifying_PreservesStatus(t *testing.T) {
	// Verify that recovery does NOT reset a classifying task to backlog.
	// The task must stay classifying so processClassifyingTasks picks it up.
	orch, db, _ := setupReconcileTest(t)

	agentID := uuid.New()
	taskID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentClassifier,
		Name:          "dead-cls-preserve",
		Status:        model.AgentWorking,
		CurrentTaskID: &taskID,
	}
	db.Create(&ag)
	// Backdate past grace period so reconciliation can act.
	db.Model(&ag).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))

	task := model.Task{
		ID:              taskID,
		ProjectID:       orch.projectID,
		Title:           "preserve-classifying",
		Description:     "must not become backlog",
		Status:          model.StatusClassifying,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	_, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)

	// Critical: status must be classifying, NOT backlog.
	if updated.Status == model.StatusBacklog {
		t.Fatal("top-level classifying task was incorrectly reset to backlog — should remain classifying")
	}
	if updated.Status != model.StatusClassifying {
		t.Errorf("expected classifying, got %s", updated.Status)
	}
}

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

func TestReconcileStuckAgents_MultipleStatuses(t *testing.T) {
	// Verify that a single reconcile pass catches dead agents across
	// classifying, planning, and in_progress statuses simultaneously.
	orch, db, bareRepo := setupReconcileTest(t)

	featureName := "stuck-multi"
	createFeatureWorktree(t, bareRepo, featureName)

	// Backdate helper — shifts agent CreatedAt past the grace period.
	backdateAgent := func(id uuid.UUID) {
		db.Model(&model.Agent{}).Where("id = ?", id).Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod))
	}

	// 1. Top-level classifying task with dead classifier.
	clsAgentID := uuid.New()
	clsTaskID := uuid.New()
	db.Create(&model.Agent{
		ID:            clsAgentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentClassifier,
		Name:          "dead-cls-multi",
		Status:        model.AgentWorking,
		CurrentTaskID: &clsTaskID,
	})
	backdateAgent(clsAgentID)
	db.Create(&model.Task{
		ID:              clsTaskID,
		ProjectID:       orch.projectID,
		Title:           "multi-classifying",
		Description:     "classifying stuck",
		Status:          model.StatusClassifying,
		AssignedAgentID: &clsAgentID,
	})

	// 2. In-progress subtask with dead coder (existing behavior).
	parentID := uuid.New()
	db.Create(&model.Task{
		ID:             parentID,
		ProjectID:      orch.projectID,
		Title:          "multi-parent",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	})
	ipAgentID := uuid.New()
	ipTaskID := uuid.New()
	db.Create(&model.Agent{
		ID:             ipAgentID,
		ProjectID:      orch.projectID,
		AgentType:      model.AgentCoder,
		Name:           "dead-coder-multi",
		Status:         model.AgentWorking,
		WorktreeBranch: "",
		CurrentTaskID:  &ipTaskID,
	})
	backdateAgent(ipAgentID)
	db.Create(&model.Task{
		ID:              ipTaskID,
		ProjectID:       orch.projectID,
		ParentTaskID:    &parentID,
		Title:           "multi-inprogress-sub",
		Description:     "in_progress stuck",
		Status:          model.StatusInProgress,
		AssignedAgentID: &ipAgentID,
	})

	// 3. Planning task with dead planner.
	planAgentID := uuid.New()
	planTaskID := uuid.New()
	db.Create(&model.Agent{
		ID:            planAgentID,
		ProjectID:     orch.projectID,
		AgentType:     model.AgentPlanner,
		Name:          "dead-planner-multi",
		Status:        model.AgentWorking,
		CurrentTaskID: &planTaskID,
	})
	backdateAgent(planAgentID)
	db.Create(&model.Task{
		ID:              planTaskID,
		ProjectID:       orch.projectID,
		Title:           "multi-planning",
		Description:     "planning stuck",
		Status:          model.StatusPlanning,
		AssignedAgentID: &planAgentID,
		WorktreeBranch:  "feature/" + featureName,
	})

	fixes, err := orch.reconcileStuckAgents()
	if err != nil {
		t.Fatalf("reconcileStuckAgents() error: %v", err)
	}
	if fixes != 3 {
		t.Errorf("expected 3 fixes (classifying + in_progress + planning), got %d", fixes)
	}
}
