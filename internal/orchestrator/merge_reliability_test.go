package orchestrator

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/memory"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// §4.3 Diagnostic Logging — structured merge_failed event
// ---------------------------------------------------------------------------

// TestMergeFailedEvent_EmitsStructuredDiagnostics verifies that when a merge
// fails with conflicts, a structured "merge_failed" event is emitted containing
// the full diagnostic details: agent_branch, feature_branch, conflicts,
// git_stderr, git_command, merge_base, feature_head, agent_head.
func TestMergeFailedEvent_EmitsStructuredDiagnostics(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "merge-diag"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create agent branch (don't merge into feature — leave diverged)
	agentBranch := "worktree-agent-conflict"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-conflict")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")

	// Create conflicting changes on the same file
	if err := os.WriteFile(filepath.Join(featureDir, "shared.txt"), []byte("feature content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, featureDir, "add", "shared.txt")
	runGitCmd(t, featureDir, "commit", "-m", "feature change")

	if err := os.WriteFile(filepath.Join(agentDir, "shared.txt"), []byte("agent content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", "shared.txt")
	runGitCmd(t, agentDir, "commit", "-m", "agent change")

	// Set up orchestrator with a real merger
	db := testutil.NewTestDB(t)
	host := NewHostManager(bareRepoPath, "main")
	wt := host.AsInterface()
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{ID: projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	runner := agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/usr/bin/false", "", 4, nil)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		memory:    memory.NewManager(db),
		events:    events,
		logger:    slog.Default().With("component", "test-merge-diag"),
	}

	// Create parent task
	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent-diag",
		Description:    "parent for diagnostic test",
		Status:         model.StatusInProgress,
		WorktreeBranch: featureBranch,
	}
	db.Create(&parent)

	// Create subtask
	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   agentDir,
		WorktreeBranch: agentBranch,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       projectID,
		ParentTaskID:    &parentID,
		Title:           "coder-subtask",
		Description:     "do coding",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	// Process the agent completion — merge should fail with conflicts
	_ = o.processAgentResult(agent.Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	})

	// Drain events and look for the merge_failed event
	var mergeFailed *Event
	for {
		select {
		case evt := <-events:
			if evt.Type == "merge_failed" {
				mergeFailed = &evt
			}
		default:
			goto done
		}
	}
done:

	if mergeFailed == nil {
		t.Fatal("expected merge_failed event to be emitted")
	}

	payload, ok := mergeFailed.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any payload, got %T", mergeFailed.Payload)
	}

	// Verify all expected fields are present
	requiredFields := []string{
		"task_id", "agent_id", "agent_branch", "feature_branch",
		"conflicts", "git_stderr", "git_command",
		"merge_base", "feature_head", "agent_head",
	}
	for _, field := range requiredFields {
		if _, exists := payload[field]; !exists {
			t.Errorf("merge_failed event missing field: %s", field)
		}
	}

	// Verify specific field values
	if ab, ok := payload["agent_branch"].(string); !ok || ab != agentBranch {
		t.Errorf("agent_branch = %v, want %q", payload["agent_branch"], agentBranch)
	}
	if fb, ok := payload["feature_branch"].(string); !ok || fb != featureBranch {
		t.Errorf("feature_branch = %v, want %q", payload["feature_branch"], featureBranch)
	}

	// merge_base, feature_head, agent_head should be non-empty SHA strings
	for _, field := range []string{"merge_base", "feature_head", "agent_head"} {
		val, ok := payload[field].(string)
		if !ok || len(val) < 7 {
			t.Errorf("%s should be a git SHA, got: %v", field, payload[field])
		}
	}

	// Conflicts should include shared.txt
	if conflicts, ok := payload["conflicts"].([]string); ok {
		found := false
		for _, c := range conflicts {
			if c == "shared.txt" {
				found = true
			}
		}
		if !found {
			t.Errorf("conflicts should include shared.txt, got: %v", conflicts)
		}
	}

	// git_command should mention rebase (since rebase-before-merge fires first)
	if cmd, ok := payload["git_command"].(string); ok {
		if cmd == "" {
			t.Error("git_command should be non-empty on merge failure")
		}
	}
}

// ---------------------------------------------------------------------------
// §4.6 Already-Merged Check — onAgentFailed fast-tracks to done
// ---------------------------------------------------------------------------

// TestOnAgentFailed_AlreadyMerged_FastTracksToDone verifies that when an
// agent fails but its work was already merged into the feature branch,
// the subtask is fast-tracked to done instead of being marked failed.
func TestOnAgentFailed_AlreadyMerged_FastTracksToDone(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "already-merged-failure"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	// Create agent branch and merge it into the feature (simulating already-merged)
	agentBranch := createAgentBranch(t, bareRepoPath, featureName, "worktree-agent-merged", true)

	db := testutil.NewTestDB(t)
	host := NewHostManager(bareRepoPath, "main")
	wt := host.AsInterface()
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{ID: projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	runner := agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/usr/bin/false", "", 4, nil)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		memory:    memory.NewManager(db),
		events:    events,
		logger:    slog.Default().With("component", "test-already-merged"),
	}

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent",
		Description:    "parent for already-merged test",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   featureDir,
		WorktreeBranch: agentBranch,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask-already-merged",
		Description:     "coding subtask",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	// Process a failed agent completion — work is already merged
	err := o.onAgentFailed(&ag, &task)
	if err != nil {
		t.Fatalf("onAgentFailed: %v", err)
	}

	// Task should be fast-tracked to done (not failed)
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusDone {
		t.Errorf("expected task status done (already merged), got %s", updated.Status)
	}
}

// TestOnAgentFailed_NotMerged_FailsNormally verifies that when an agent fails
// and its work is NOT merged, the subtask is marked failed as usual.
func TestOnAgentFailed_NotMerged_FailsNormally(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "not-merged-failure"
	createFeatureWorktree(t, bareRepoPath, featureName)

	// Create a diverged agent branch (NOT merged into feature)
	agentBranch := "worktree-agent-diverged"
	featureBranch := "feature/" + featureName
	runGitCmd(t, bareRepoPath, "branch", agentBranch, featureBranch)
	agentDir := filepath.Join(bareRepoPath, "feature", featureName, "agent-div")
	runGitCmd(t, bareRepoPath, "worktree", "add", agentDir, agentBranch)
	runGitCmd(t, agentDir, "config", "user.email", "test@test.com")
	runGitCmd(t, agentDir, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(agentDir, "diverged.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, agentDir, "add", ".")
	runGitCmd(t, agentDir, "commit", "-m", "diverged commit")

	db := testutil.NewTestDB(t)
	host := NewHostManager(bareRepoPath, "main")
	wt := host.AsInterface()
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{ID: projectID, Name: "test", BareRepoPath: bareRepoPath}
	db.Create(&project)

	runner := agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/usr/bin/false", "", 4, nil)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		memory:    memory.NewManager(db),
		events:    events,
		logger:    slog.Default().With("component", "test-not-merged"),
	}

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: featureBranch,
	}
	db.Create(&parent)

	taskID := uuid.New()
	agentID := uuid.New()
	ag := model.Agent{
		ID:             agentID,
		ProjectID:      projectID,
		AgentType:      model.AgentCoder,
		Name:           "test-coder",
		Status:         model.AgentWorking,
		CurrentTaskID:  &taskID,
		WorktreePath:   agentDir,
		WorktreeBranch: agentBranch,
	}
	db.Create(&ag)

	task := model.Task{
		ID:              taskID,
		ProjectID:       projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask-diverged",
		Description:     "coding subtask",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	db.Create(&task)

	err := o.onAgentFailed(&ag, &task)
	if err != nil {
		t.Fatalf("onAgentFailed: %v", err)
	}

	// Task should be failed (work NOT merged)
	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected task status failed, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// §4.7 Parent Failure Cascading — additional edge cases
// ---------------------------------------------------------------------------

// TestCheckFeatureCompletion_BacklogSubtask_StaysInProgress verifies that
// the parent stays in_progress when some subtasks are still in backlog.
func TestCheckFeatureCompletion_BacklogSubtask_StaysInProgress(t *testing.T) {
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

	// 1 done, 1 failed, 1 still in backlog
	sub1 := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parentID,
		Title: "done-sub", Description: "test", Status: model.StatusDone,
	}
	sub2 := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parentID,
		Title: "failed-sub", Description: "test", Status: model.StatusFailed,
	}
	sub3 := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parentID,
		Title: "backlog-sub", Description: "test", Status: model.StatusBacklog,
	}
	db.Create(&sub1)
	db.Create(&sub2)
	db.Create(&sub3)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress (backlog sub exists), got %s", updated.Status)
	}
}

// TestCheckFeatureCompletion_PlanningSubtask_StaysInProgress verifies that
// the parent stays in_progress when a subtask is still in planning.
func TestCheckFeatureCompletion_PlanningSubtask_StaysInProgress(t *testing.T) {
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

	sub1 := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parentID,
		Title: "done-sub", Description: "test", Status: model.StatusDone,
	}
	sub2 := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parentID,
		Title: "planning-sub", Description: "test", Status: model.StatusPlanning,
	}
	db.Create(&sub1)
	db.Create(&sub2)

	if err := o.checkFeatureCompletion(&parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected parent to stay in_progress (planning sub exists), got %s", updated.Status)
	}
}
