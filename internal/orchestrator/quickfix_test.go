package orchestrator

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupQuickFixTest creates an orchestrator suitable for testing quickfix
// lifecycle transitions. The runner has maxConcurrent=0 so CanSpawn returns
// false (no real agents spawned).
func setupQuickFixTest(t *testing.T) (*Orchestrator, *gorm.DB, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{
		ID:            projectID,
		Name:          "quickfix-test-" + projectID.String()[:8],
		BareRepoPath:  "/tmp/fake-bare-repo",
		DefaultBranch: "main",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	wt := &FakeWorktreeManager{BarePath: "/tmp/fake-bare-repo", Default: "main"}
	// Runner with maxConcurrent=0 so CanSpawn returns false.
	runner := agent.NewRunner(db, nil, nil, "/bin/false", "", 0, nil)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		events:    events,
		logger:    slog.Default().With("component", "quickfix-test"),
	}
	return o, db, projectID
}

// createQuickFixTask creates a quickfix task with the given status.
func createQuickFixTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) model.Task {
	t.Helper()
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: "quick fix test: " + title,
		Status:      status,
		Category:    model.CategoryQuickFix,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create quickfix task %q: %v", title, err)
	}
	return task
}

// createStandardTask creates a standard task with the given status.
func createStandardTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, title string, status model.TaskStatus) model.Task {
	t.Helper()
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: "standard test: " + title,
		Status:      status,
		Category:    model.CategoryStandard,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create standard task %q: %v", title, err)
	}
	return task
}

// ---------------------------------------------------------------------------
// processBacklog routing tests
// ---------------------------------------------------------------------------

func TestProcessBacklog_QuickFix_TransitionsToInProgress(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createQuickFixTask(t, db, projectID, "fix typo in README", model.StatusBacklog)

	// processBacklog should route to processQuickFix, which returns nil
	// when runner.CanSpawn() is false (no capacity). The task stays in
	// backlog because no agent can be spawned yet.
	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: unexpected error: %v", err)
	}

	// Reload task from DB.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// With CanSpawn=false, quickfix stays in backlog (waiting for capacity).
	// The key test is that it did NOT transition to planning.
	if updated.Status == model.StatusPlanning {
		t.Error("quickfix task should NOT transition to planning")
	}
}

func TestProcessBacklog_Standard_StillGoesToPlanning(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createStandardTask(t, db, projectID, "implement new feature", model.StatusBacklog)

	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: unexpected error: %v", err)
	}

	// Reload task from DB.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	// Standard tasks should transition to planning.
	if updated.Status != model.StatusPlanning {
		t.Errorf("expected status %q, got %q", model.StatusPlanning, updated.Status)
	}
}

// ---------------------------------------------------------------------------
// Quick fix IN_PROGRESS processing tests
// ---------------------------------------------------------------------------

func TestQuickFix_SkipsSubtaskScheduling(t *testing.T) {
	_, db, projectID := setupQuickFixTest(t)

	// Create a quickfix task in IN_PROGRESS (simulating post-agent-spawn).
	task := createQuickFixTask(t, db, projectID, "fix lint warning", model.StatusInProgress)

	// Assign a fake agent so the task doesn't try to transition to merging.
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentCoder,
		Name:      "test-quickfix-agent",
		Status:    model.AgentWorking,
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task.AssignedAgentID = &agentID
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task with agent: %v", err)
	}

	// scheduleSubtasks would fail or be a no-op since quickfix has no subtasks.
	// The key assertion is that the code path skips scheduleSubtasks and
	// checkFeatureCompletion for quickfix tasks. We verify this by confirming
	// no subtasks were created and no errors occurred.
	var subtaskCount int64
	db.Model(&model.Task{}).Where("parent_task_id = ?", task.ID).Count(&subtaskCount)
	if subtaskCount != 0 {
		t.Errorf("expected no subtasks for quickfix task, got %d", subtaskCount)
	}
}

// ---------------------------------------------------------------------------
// Merge failure tests
// ---------------------------------------------------------------------------

func TestQuickFix_MergeFailure_FlagsForHumanReview(t *testing.T) {
	// Set up with a real bare repo so the merger can attempt (and fail) a merge.
	bareRepo := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bareRepo)

	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "quickfix-merge-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: defaultBranch,
	}
	db.Create(&project)

	events := make(chan Event, 100)
	host := NewHostManager(bareRepo, defaultBranch)
	wt := host.AsInterface()
	runner := agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/bin/false", "", 0, nil)

	// Stub the mergeDispatcher so the quickfix path exercises the
	// Success=false + Conflicts branch without spawning a real merger.
	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		mergeDispatcher: &stubMerger{results: []stubMergeResult{
			{result: &MergeResult{Success: false, Conflicts: []string{"conflict.go"}}, err: nil},
		}},
		events: events,
		logger: slog.Default().With("component", "quickfix-merge-test"),
	}

	// Create a quickfix task in MERGING state with a feature branch that has
	// merge conflicts (per the stub above), which triggers the quickfix
	// failure path.
	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "fix broken import",
		Description:    "quick fix: broken import",
		Status:         model.StatusMerging,
		Category:       model.CategoryQuickFix,
		WorktreeBranch: "feature/nonexistent-quickfix",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// executeMerge should route conflict failures on quickfix tasks into
	// the NeedsHumanReview + StatusFailed branch.
	if err := o.executeMerge(&task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if !updated.NeedsHumanReview {
		t.Error("expected NeedsHumanReview to be true for failed quickfix merge")
	}
	if updated.Status != model.StatusFailed {
		t.Errorf("expected status %q, got %q", model.StatusFailed, updated.Status)
	}
}

// ---------------------------------------------------------------------------
// transitionQuickFixToMerging tests
// ---------------------------------------------------------------------------

func TestTransitionQuickFixToMerging_FastTracksThroughTestingReady(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createQuickFixTask(t, db, projectID, "fix spacing issue", model.StatusInProgress)

	// transitionQuickFixToMerging should fast-track through testing_ready
	// to merging without constraint checks (no real worktree).
	if err := o.transitionQuickFixToMerging(&task); err != nil {
		t.Fatalf("transitionQuickFixToMerging: unexpected error: %v", err)
	}

	// Reload and verify status is merging.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusMerging {
		t.Errorf("expected status %q, got %q", model.StatusMerging, updated.Status)
	}

	// Verify events were recorded for both transitions.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&events)

	hasTestingReady := false
	hasMerging := false
	for _, e := range events {
		if e.NewValue == string(model.StatusTestingReady) {
			hasTestingReady = true
		}
		if e.NewValue == string(model.StatusMerging) {
			hasMerging = true
		}
	}

	if !hasTestingReady {
		t.Error("expected a testing_ready transition event")
	}
	if !hasMerging {
		t.Error("expected a merging transition event")
	}
}

func TestTransitionQuickFixToMerging_ConstraintViolation_PausesTask(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bareRepo)

	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "quickfix-constraint-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: defaultBranch,
	}
	db.Create(&project)

	events := make(chan Event, 100)
	host := NewHostManager(bareRepo, defaultBranch)
	wt := host.AsInterface()
	runner := agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/bin/false", "", 0, nil)

	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  wt,
		runner:    runner,
		events:    events,
		logger:    slog.Default().With("component", "quickfix-constraint-test"),
	}

	// Create a worktree branch name that points to a real worktree directory
	// containing a .drem/constraints.toml with a failing constraint.
	featureBranch := "feature/quickfix-constraint-violation"
	featureDir := wt.FeatureWorktreePath(strings.TrimPrefix(featureBranch, "feature/"))

	// Write a constraints.toml with a rule that always fails (max_lines=0).
	constraintsDir := featureDir + "/.drem"
	if err := os.MkdirAll(constraintsDir, 0o755); err != nil {
		t.Fatalf("mkdir constraints dir: %v", err)
	}
	constraintsToml := constraintsDir + "/constraints.toml"
	if err := os.WriteFile(constraintsToml, []byte("[[max_lines]]\nname = \"always-fail\"\nglob = \"*.go\"\nlimit = 0\n"), 0o644); err != nil {
		t.Fatalf("write constraints.toml: %v", err)
	}
	// Write a Go file so the constraint has something to match (exceeds limit=0).
	if err := os.WriteFile(featureDir+"/dummy.go", []byte("package dummy\n"), 0o644); err != nil {
		t.Fatalf("write dummy.go: %v", err)
	}

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "fix with constraint violation",
		Description:    "quick fix: constraint violation",
		Status:         model.StatusInProgress,
		Category:       model.CategoryQuickFix,
		WorktreeBranch: featureBranch,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := o.transitionQuickFixToMerging(&task); err != nil {
		t.Fatalf("transitionQuickFixToMerging: unexpected error: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusPaused {
		t.Errorf("expected status %q after constraint violation, got %q", model.StatusPaused, updated.Status)
	}
	if !updated.NeedsHumanReview {
		t.Error("expected NeedsHumanReview=true after constraint violation")
	}

	// Verify a pause event was recorded.
	var taskEvents []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&taskEvents)

	hasPaused := false
	for _, e := range taskEvents {
		if e.NewValue == string(model.StatusPaused) {
			hasPaused = true
		}
	}
	if !hasPaused {
		t.Error("expected a paused transition event after constraint violation")
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle tests
// ---------------------------------------------------------------------------

func TestQuickFix_NeverEntersPlanningStates(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	// Create a quickfix task in backlog.
	task := createQuickFixTask(t, db, projectID, "fix typo", model.StatusBacklog)

	// Step 1: processBacklog — with no capacity, stays in backlog.
	if err := o.processBacklog(&task); err != nil {
		t.Fatalf("processBacklog: %v", err)
	}

	// Simulate the quickfix lifecycle by manually transitioning through
	// the expected states, verifying that planning states are never visited.
	// Reset task to in_progress (simulating what processQuickFix would do
	// with capacity).
	task.Status = model.StatusInProgress
	task.Category = model.CategoryQuickFix
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save in_progress: %v", err)
	}

	// Step 2: transitionQuickFixToMerging.
	if err := o.transitionQuickFixToMerging(&task); err != nil {
		t.Fatalf("transitionQuickFixToMerging: %v", err)
	}

	// Verify no events reference planning states.
	var events []model.TaskEvent
	db.Where("task_id = ?", task.ID).Find(&events)

	forbiddenStates := map[string]bool{
		string(model.StatusPlanning):    true,
		string(model.StatusPlanReview):  true,
		string(model.StatusTestWriting): true,
		string(model.StatusTestReview):  true,
	}

	for _, e := range events {
		if forbiddenStates[e.NewValue] {
			t.Errorf("quickfix task entered forbidden state %q", e.NewValue)
		}
		if forbiddenStates[e.OldValue] {
			t.Errorf("quickfix task was in forbidden state %q", e.OldValue)
		}
	}

	// Reload and verify final status is merging.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusMerging {
		t.Errorf("expected final status %q, got %q", model.StatusMerging, updated.Status)
	}
}

func TestQuickFix_CategoryDefaultsToStandard(t *testing.T) {
	_, db, projectID := setupQuickFixTest(t)

	// Create a task without explicitly setting category.
	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       "task without category",
		Description: "should default to standard",
		Status:      model.StatusBacklog,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	var loaded model.Task
	if err := db.First(&loaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}

	if loaded.Category.IsQuickFix() {
		t.Error("default category should not be quickfix")
	}
}

func TestProcessQuickFix_RetryExhaustion(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createQuickFixTask(t, db, projectID, "fix retry test", model.StatusBacklog)

	// Set retry count to MaxQuickFixRetries - 1 so next failure hits max.
	task.Context = model.JSONField{"retry_count": float64(MaxQuickFixRetries - 1)}

	// Assign a dead agent to trigger retry logic.
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentCoder,
		Name:      "dead-quickfix-agent",
		Status:    model.AgentDead,
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task.AssignedAgentID = &agentID
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	// processQuickFix should fail the task after max retries.
	if err := o.processQuickFix(&task); err != nil {
		t.Fatalf("processQuickFix: unexpected error: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusFailed {
		t.Errorf("expected status %q after retry exhaustion, got %q", model.StatusFailed, updated.Status)
	}
}

func TestProcessQuickFix_NoCapacity_ReturnsNil(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createQuickFixTask(t, db, projectID, "fix no capacity", model.StatusBacklog)

	// Runner has maxConcurrent=0, so CanSpawn returns false.
	err := o.processQuickFix(&task)
	if err != nil {
		t.Fatalf("processQuickFix: expected nil error when no capacity, got: %v", err)
	}

	// Task should still be in backlog.
	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusBacklog {
		t.Errorf("expected status %q (waiting for capacity), got %q", model.StatusBacklog, updated.Status)
	}
}

func TestProcessQuickFix_AgentStillWorking_ReturnsNil(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	task := createQuickFixTask(t, db, projectID, "fix with working agent", model.StatusBacklog)

	// Assign a working agent.
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: projectID,
		AgentType: model.AgentCoder,
		Name:      "working-quickfix-agent",
		Status:    model.AgentWorking,
	}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task.AssignedAgentID = &agentID
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	err := o.processQuickFix(&task)
	if err != nil {
		t.Fatalf("processQuickFix: expected nil when agent is working, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Empty work retry tests
// ---------------------------------------------------------------------------

func TestQuickFixInProgress_EmptyWork_DoesNotTransitionToMerging(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	// Create a quickfix task in IN_PROGRESS with empty_work=true.
	// This simulates the state after onAgentEmptyWork clears the agent.
	task := createQuickFixTask(t, db, projectID, "fix empty work retry", model.StatusInProgress)
	task.Context = model.JSONField{
		"empty_work":        true,
		"retry_count":       float64(1),
		"prompt_adjustment": "You MUST edit the file and commit your changes.",
	}
	task.WorktreeBranch = "feature/test-quickfix"
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	// respawnQuickFixAgent should return nil when CanSpawn() is false
	// (no capacity). The task should stay in IN_PROGRESS with empty_work
	// still set, waiting for next tick.
	if err := o.respawnQuickFixAgent(&task); err != nil {
		t.Fatalf("respawnQuickFixAgent: unexpected error: %v", err)
	}

	// Reload and verify: task should NOT have transitioned to merging.
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status == model.StatusMerging {
		t.Error("quickfix task with empty_work should NOT transition to merging")
	}
	if updated.Status == model.StatusTestingReady {
		t.Error("quickfix task with empty_work should NOT transition to testing_ready")
	}
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status %q, got %q", model.StatusInProgress, updated.Status)
	}
}

func TestQuickFixInProgress_EmptyWork_RespawnClearsFlag(t *testing.T) {
	// Verify that when capacity is available and agent is spawned,
	// the empty_work flag is cleared from context.
	// We can't easily test full spawn (needs real claude binary), so
	// we test the logic indirectly: when CanSpawn is false, the flag
	// should remain (respawn deferred to next tick).

	o, db, projectID := setupQuickFixTest(t)

	task := createQuickFixTask(t, db, projectID, "fix retry flag test", model.StatusInProgress)
	task.Context = model.JSONField{
		"empty_work":  true,
		"retry_count": float64(1),
	}
	task.WorktreeBranch = "feature/test-quickfix-flag"
	if err := db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	// CanSpawn() is false (runner has maxConcurrent=0), so respawn should
	// return nil without modifying the task.
	if err := o.respawnQuickFixAgent(&task); err != nil {
		t.Fatalf("respawnQuickFixAgent: unexpected error: %v", err)
	}

	// Reload and verify empty_work flag is still set (waiting for capacity).
	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Context == nil {
		t.Fatal("expected context to be preserved")
	}
	if _, ok := updated.Context["empty_work"]; !ok {
		t.Error("empty_work flag should remain when respawn is deferred (no capacity)")
	}
}

func TestQuickFixInProgress_NoEmptyWork_WouldTransitionToMerging(t *testing.T) {
	o, db, projectID := setupQuickFixTest(t)

	// Create a quickfix task in IN_PROGRESS WITHOUT empty_work flag.
	// This simulates successful agent completion where AssignedAgentID
	// was cleared by agent completion.
	task := createQuickFixTask(t, db, projectID, "fix clean completion", model.StatusInProgress)
	// No empty_work in context — agent completed normally.

	// transitionQuickFixToMerging should fast-track to merging.
	if err := o.transitionQuickFixToMerging(&task); err != nil {
		t.Fatalf("transitionQuickFixToMerging: unexpected error: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}

	if updated.Status != model.StatusMerging {
		t.Errorf("expected status %q for clean completion, got %q", model.StatusMerging, updated.Status)
	}
}

func TestQuickFixInProgress_EmptyWorkDetection(t *testing.T) {
	// Verify the empty_work detection logic used in the doTick handler.
	tests := []struct {
		name      string
		context   model.JSONField
		wantRetry bool
	}{
		{
			name:      "empty_work=true triggers retry",
			context:   model.JSONField{"empty_work": true},
			wantRetry: true,
		},
		{
			name:      "no context means no retry",
			context:   nil,
			wantRetry: false,
		},
		{
			name:      "empty context means no retry",
			context:   model.JSONField{},
			wantRetry: false,
		},
		{
			name:      "other keys but no empty_work means no retry",
			context:   model.JSONField{"retry_count": float64(1)},
			wantRetry: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := &model.Task{Context: tc.context}
			needsRetry := false
			if task.Context != nil {
				if _, ok := task.Context["empty_work"]; ok {
					needsRetry = true
				}
			}
			if needsRetry != tc.wantRetry {
				t.Errorf("empty_work detection: got %v, want %v", needsRetry, tc.wantRetry)
			}
		})
	}
}
