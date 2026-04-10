package orchestrator

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// setupDedupSchedulingTest creates a real bare git repo with an integration
// worktree and returns an Orchestrator wired to it. The runner has
// maxConcurrent=0 so CanSpawn always returns false (no real agents spawned).
func setupDedupSchedulingTest(t *testing.T) (*Orchestrator, *gorm.DB, uuid.UUID, string) {
	t.Helper()

	bareRepo := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bareRepo)

	// Create a feature group directory and integration worktree.
	featureName := "dedup-test"
	featureDir := filepath.Join(bareRepo, "feature", featureName)
	integrationDir := filepath.Join(featureDir, "integration")
	os.MkdirAll(featureDir, 0o755)
	testutil.AddWorktree(t, bareRepo, "integration", integrationDir)

	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "dedup-scheduling-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: defaultBranch,
	}
	db.Create(&project)

	events := make(chan Event, 100)
	wtMgr := &worktree.Manager{BareRepoPath: bareRepo, DefaultBranch: defaultBranch}

	// Runner with maxConcurrent=0 so CanSpawn returns false without panicking.
	runner := agent.NewRunner(db, nil, wtMgr, "/bin/false", "", 0, nil)

	orch := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wtMgr,
		runner:    runner,
		events:    events,
		logger:    slog.Default().With("component", "dedup-scheduling-test"),
	}

	return orch, db, projectID, integrationDir
}

// TestDedupScheduling_FastTrackedWhenWorkExists verifies that scheduleSubtasks
// marks a subtask as DONE when its estimated_files overlap with files already
// changed on the integration branch and a commit message matches its title.
func TestDedupScheduling_FastTrackedWhenWorkExists(t *testing.T) {
	o, db, projectID, integrationDir := setupDedupSchedulingTest(t)

	// Simulate prior work: commit a file on the integration branch whose
	// path matches the subtask's estimated_files and whose commit message
	// matches the subtask title.
	subtaskTitle := "Implement dedup helper"
	os.MkdirAll(filepath.Join(integrationDir, "internal", "orchestrator"), 0o755)
	testutil.CommitFile(t, integrationDir,
		"internal/orchestrator/dedup.go",
		"package orchestrator\n// dedup implementation\n",
		subtaskTitle)

	// Create parent task (IN_PROGRESS with a WorktreeBranch).
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Content-aware subtask dedup",
		Description:    "parent feature task",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/dedup-test",
	}
	db.Create(&parent)

	// Create subtask (BACKLOG) with estimated_files that overlap the committed file.
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        subtaskTitle,
		Description:  "Write the dedup helper function",
		Status:       model.StatusBacklog,
		Context: model.JSONField{
			"estimated_files": []any{"internal/orchestrator/dedup.go"},
			"agent_type":      "coder",
		},
	}
	db.Create(&sub)

	// Run scheduleSubtasks — the dedup check should detect existing work
	// and fast-track the subtask to DONE without spawning an agent.
	if err := o.scheduleSubtasks(&parent); err != nil {
		t.Fatalf("scheduleSubtasks: %v", err)
	}

	// Verify subtask was fast-tracked to DONE.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected subtask status %q (fast-tracked), got %q",
			model.StatusDone, updated.Status)
	}

	// Verify no agent was assigned (dedup skipped spawning).
	if updated.AssignedAgentID != nil {
		t.Errorf("expected no agent assigned (dedup fast-track), got %v",
			updated.AssignedAgentID)
	}
}

// TestDedupScheduling_NotFastTrackedWhenNoWorkExists verifies that
// scheduleSubtasks does NOT fast-track a subtask whose estimated_files
// do not overlap with files changed on the integration branch.
func TestDedupScheduling_NotFastTrackedWhenNoWorkExists(t *testing.T) {
	o, db, projectID, _ := setupDedupSchedulingTest(t)

	// No prior work committed for this subtask's files.
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Content-aware subtask dedup",
		Description:    "parent feature task",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/dedup-test",
	}
	db.Create(&parent)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "Add scoring module",
		Description:  "Create the scoring system",
		Status:       model.StatusBacklog,
		Context: model.JSONField{
			"estimated_files": []any{"internal/scoring/score.go", "internal/scoring/score_test.go"},
			"agent_type":      "coder",
		},
	}
	db.Create(&sub)

	if err := o.scheduleSubtasks(&parent); err != nil {
		t.Fatalf("scheduleSubtasks: %v", err)
	}

	// Subtask should NOT be fast-tracked. It remains BACKLOG because
	// runner.CanSpawn returns false (maxConcurrent=0).
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status == model.StatusDone {
		t.Errorf("expected subtask NOT to be fast-tracked to done, but it was")
	}
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask to remain %q, got %q",
			model.StatusBacklog, updated.Status)
	}
}

// TestDedupScheduling_NoEstimatedFilesSkipsDedupCheck verifies that subtasks
// with nil/empty estimated_files in Context skip the dedup check entirely
// and proceed to normal scheduling.
func TestDedupScheduling_NoEstimatedFilesSkipsDedupCheck(t *testing.T) {
	o, db, projectID, integrationDir := setupDedupSchedulingTest(t)

	// Commit work on the integration branch (but subtask has no estimated_files
	// to match against, so dedup should be skipped).
	os.MkdirAll(filepath.Join(integrationDir, "internal", "orchestrator"), 0o755)
	testutil.CommitFile(t, integrationDir,
		"internal/orchestrator/dedup.go",
		"package orchestrator\n// dedup\n",
		"Implement dedup helper")

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Content-aware subtask dedup",
		Description:    "parent feature task",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/dedup-test",
	}
	db.Create(&parent)

	// Subtask with NO estimated_files — dedup check should be skipped.
	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parentID,
		Title:        "Implement dedup helper",
		Description:  "Write the dedup function",
		Status:       model.StatusBacklog,
		Context: model.JSONField{
			"agent_type": "coder",
			// No "estimated_files" key.
		},
	}
	db.Create(&sub)

	if err := o.scheduleSubtasks(&parent); err != nil {
		t.Fatalf("scheduleSubtasks: %v", err)
	}

	// Without estimated_files, the dedup check is skipped. The subtask
	// should NOT be fast-tracked even though matching work exists.
	// It remains BACKLOG because runner.CanSpawn returns false.
	var updated model.Task
	db.First(&updated, "id = ?", sub.ID)
	if updated.Status == model.StatusDone {
		t.Errorf("expected subtask NOT to be fast-tracked (no estimated_files), but status is %q",
			updated.Status)
	}
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected subtask to remain %q, got %q",
			model.StatusBacklog, updated.Status)
	}
}
