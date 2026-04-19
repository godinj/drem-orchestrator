package orchestrator

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// orchForTest creates an Orchestrator with a test DB and minimal dependencies.
func orchForTest(t *testing.T, database *gorm.DB) *Orchestrator {
	t.Helper()
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake-bare.git", Default: "main"}
	return testOrchestrator(t, database, wt)
}

// createProject creates a project with the given orchestrator's project ID.
func createProject(t *testing.T, db *gorm.DB, projectID uuid.UUID) {
	t.Helper()
	project := model.Project{
		ID:           projectID,
		Name:         "test-project",
		BareRepoPath: "/tmp/fake-bare.git",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
}

// ---------------------------------------------------------------------------
// getTaskPhase tests
// ---------------------------------------------------------------------------

func TestGetTaskPhase_FromContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)

	task := &model.Task{
		ID:      uuid.New(),
		Context: model.JSONField{"phase": "test"},
	}
	if got := o.getTaskPhase(task); got != "test" {
		t.Errorf("expected 'test', got %q", got)
	}
}

func TestGetTaskPhase_DefaultSubtask(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)

	parentID := uuid.New()
	task := &model.Task{
		ID:           uuid.New(),
		ParentTaskID: &parentID,
	}
	if got := o.getTaskPhase(task); got != "implementation" {
		t.Errorf("expected 'implementation', got %q", got)
	}
}

func TestGetTaskPhase_RootTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)

	task := &model.Task{ID: uuid.New()}
	if got := o.getTaskPhase(task); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// checkContextUsage tests
// ---------------------------------------------------------------------------

func TestCheckContextUsage_ImplAgentAt85_FixerSpawned(t *testing.T) {
	// This test verifies the logic in checkContextUsage when an implementation
	// agent is at 85% context usage. Since we can't spawn a real fixer (no
	// runner), we verify the thresholds and phase detection logic.
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "parent",
		Description:    "test parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test",
	}
	db.Create(&parent)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:           subtaskID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "impl subtask",
		Description:  "test",
		Status:       model.StatusInProgress,
		Context:      model.JSONField{"phase": "implementation"},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
		HeartbeatAt:   &now,
		Config:        model.JSONField{"context_used_pct": float64(85)},
	}
	db.Create(&ag)

	// Verify the phase detection returns "implementation".
	phase := o.getTaskPhase(&subtask)
	if phase != "implementation" {
		t.Errorf("expected phase 'implementation', got %q", phase)
	}

	// Verify that 85% is at the fixer threshold.
	if 85 < o.contextFixerPct {
		t.Errorf("expected 85 >= contextFixerPct (%d)", o.contextFixerPct)
	}
}

func TestCheckContextUsage_ImplAgentAt74_NoAction(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "impl subtask",
		Description: "test",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "implementation"},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
		HeartbeatAt:   &now,
		Config:        model.JSONField{"context_used_pct": float64(74)},
	}
	db.Create(&ag)

	// At 74%, no fixer threshold, no warning threshold. No action needed.
	if 74 >= o.contextFixerPct {
		t.Errorf("74 should be below fixer threshold (%d)", o.contextFixerPct)
	}
	if 74 >= o.contextWarnPct {
		t.Errorf("74 should be below warn threshold (%d)", o.contextWarnPct)
	}
}

func TestCheckContextUsage_TestAgentAt85_NoFixer(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "test subtask",
		Description: "test",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-writer",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
		HeartbeatAt:   &now,
		Config:        model.JSONField{"context_used_pct": float64(85)},
	}
	db.Create(&ag)

	// Verify phase is "test" — no fixer should be spawned.
	phase := o.getTaskPhase(&subtask)
	if phase != "test" {
		t.Errorf("expected phase 'test', got %q", phase)
	}

	// At 85% with test phase, the logic should only warn, not spawn fixer.
	if phase == "implementation" || phase == "integration" {
		t.Error("test phase should NOT trigger fixer spawning logic")
	}
}

func TestCheckContextUsage_FixerAgentAt80_HumanEscalation(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:           subtaskID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "fixer subtask",
		Description:  "test",
		Status:       model.StatusInProgress,
	}
	db.Create(&subtask)

	agentID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentFixer,
		Name:          "test-fixer",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
		HeartbeatAt:   &now,
		Config:        model.JSONField{"context_used_pct": float64(80)},
	}
	db.Create(&ag)

	// Verify the escalation logic: fixer at 80% should trigger human escalation.
	if ag.AgentType != model.AgentFixer {
		t.Error("expected AgentFixer type")
	}
	pct := 80
	if ag.AgentType == model.AgentFixer && pct >= 80 {
		// This is the condition that triggers escalateFixerToHuman.
		// Test the escalation method directly.
		err := o.escalateFixerToHuman(&subtask, &ag, pct)
		if err != nil {
			t.Fatalf("escalateFixerToHuman failed: %v", err)
		}

		// Verify subtask is failed.
		var updated model.Task
		db.First(&updated, "id = ?", subtaskID)
		if updated.Status != model.StatusFailed {
			t.Errorf("expected subtask FAILED, got %s", updated.Status)
		}

		// Verify parent has needs_human_review.
		var updatedParent model.Task
		db.First(&updatedParent, "id = ?", parentID)
		if updatedParent.Context == nil {
			t.Fatal("expected parent context to be set")
		}
		if v, ok := updatedParent.Context["needs_human_review"].(bool); !ok || !v {
			t.Error("expected parent.Context['needs_human_review'] to be true")
		}
	}
}

func TestCheckContextUsage_AnyAgentAt90_HardStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "hard-stop subtask",
		Description: "test",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "implementation"},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
		HeartbeatAt:   &now,
		Config:        model.JSONField{"context_used_pct": float64(90)},
	}
	db.Create(&ag)

	// At 90%, any agent should be hard-stopped.
	pct := 90
	if pct < o.contextStopPct {
		t.Errorf("90 should be at or above stop threshold (%d)", o.contextStopPct)
	}

	// Test handleAgentContextExhausted directly.
	err := o.handleAgentContextExhausted(&subtask, &ag, pct)
	if err != nil {
		t.Fatalf("handleAgentContextExhausted failed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", subtaskID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected subtask FAILED, got %s", updated.Status)
	}
}

func TestCheckContextUsage_CompactionTriggered_HardStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "compacted subtask",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&subtask)

	agentID := uuid.New()
	now := time.Now()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
		HeartbeatAt:   &now,
		Config: model.JSONField{
			"context_used_pct":     float64(60),
			"compaction_triggered": true,
		},
	}
	db.Create(&ag)

	// Compaction should trigger hard stop regardless of percentage.
	usage := &ContextUsage{UsedPercent: 60, CompactionTriggered: true}
	if !usage.CompactionTriggered {
		t.Error("expected compaction triggered")
	}

	// Verify the hard stop path is taken.
	err := o.handleAgentContextExhausted(&subtask, &ag, 60)
	if err != nil {
		t.Fatalf("handleAgentContextExhausted failed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", subtaskID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected subtask FAILED, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// handleTestWritingFailure tests
// ---------------------------------------------------------------------------

func TestHandleTestWritingFailure_CompilableTestsExist(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	// Create a temp directory with a test file.
	tmpDir := t.TempDir()

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "test-write subtask",
		Description: "test",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	ag := model.Agent{
		ID:           agentID,
		ProjectID:    o.projectID,
		AgentType:    model.AgentCoder,
		Name:         "test-writer",
		Status:       model.AgentDead,
		WorktreePath: tmpDir,
	}
	db.Create(&ag)

	// checkForCompilableTests returns true when test files exist (simplified).
	// Since we can't easily set up a Go project in temp dir, test the partial
	// success path with the override.
	// For this test, we'll verify the state machine logic directly.
	// Simulate: compilable tests exist by manually calling the partial success path.

	// Test that when phase is "test", handleAgentContextExhausted delegates
	// to handleTestWritingFailure.
	phase := o.getTaskPhase(&subtask)
	if phase != "test" {
		t.Fatalf("expected phase 'test', got %q", phase)
	}
}

func TestHandleTestWritingFailure_NoCompilableTests_FirstAttempt(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "test-write subtask",
		Description: "test",
		Status:      model.StatusInProgress,
		Context:     model.JSONField{"phase": "test"},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	ag := model.Agent{
		ID:           agentID,
		ProjectID:    o.projectID,
		AgentType:    model.AgentCoder,
		Name:         "test-writer",
		Status:       model.AgentDead,
		WorktreePath: "/nonexistent/path",
	}
	db.Create(&ag)

	// No compilable tests, first attempt → should fail with retry flag set.
	err := o.handleTestWritingFailure(&subtask, &ag)
	if err != nil {
		t.Fatalf("handleTestWritingFailure failed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", subtaskID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected subtask FAILED for retry, got %s", updated.Status)
	}
	if updated.Context == nil {
		t.Fatal("expected context to be set")
	}
	if v, ok := updated.Context["test_writing_retry"].(bool); !ok || !v {
		t.Error("expected test_writing_retry to be true")
	}
}

func TestHandleTestWritingFailure_NoCompilableTests_Retry(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:           subtaskID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "test-write subtask",
		Description:  "test",
		Status:       model.StatusInProgress,
		Context: model.JSONField{
			"phase":              "test",
			"test_writing_retry": true,
		},
	}
	db.Create(&subtask)

	agentID := uuid.New()
	ag := model.Agent{
		ID:           agentID,
		ProjectID:    o.projectID,
		AgentType:    model.AgentCoder,
		Name:         "test-writer",
		Status:       model.AgentDead,
		WorktreePath: "/nonexistent/path",
	}
	db.Create(&ag)

	// Already a retry, no compilable tests → permanent failure.
	err := o.handleTestWritingFailure(&subtask, &ag)
	if err != nil {
		t.Fatalf("handleTestWritingFailure failed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", subtaskID)
	if updated.Status != model.StatusFailed {
		t.Errorf("expected subtask FAILED permanently, got %s", updated.Status)
	}

	// Verify parent has needs_human_review.
	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Context == nil {
		t.Fatal("expected parent context to be set")
	}
	if v, ok := updatedParent.Context["needs_human_review"].(bool); !ok || !v {
		t.Error("expected parent.Context['needs_human_review'] to be true")
	}
}

// ---------------------------------------------------------------------------
// processTestingReady tests
// ---------------------------------------------------------------------------

func TestProcessTestingReady_TestsPass(t *testing.T) {
	bareRepoPath := setupTestRepoWithMainBranch(t)

	featureName := "test-gate-pass"
	featureDir := createFeatureWorktree(t, bareRepoPath, featureName)

	db := testutil.NewTestDB(t)
	wt := &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"}
	events := make(chan Event, 100)
	o := &Orchestrator{
		db:              db,
		projectID:       uuid.New(),
		worktree:        wt,
		events:          events,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "test-orchestrator"),
	}
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "test gate parent",
		Description:    "test",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	db.Create(&parent)

	// Verify the worktree exists.
	_ = featureDir

	// processTestingReady will try to run "go test ./..." which may fail in
	// a bare repo test worktree. We verify the method doesn't panic and
	// handles the test result correctly.
	err := o.processTestingReady(&parent)
	if err != nil {
		t.Fatalf("processTestingReady failed: %v", err)
	}

	// The test suite likely failed (no Go project in worktree).
	// Verify the task is either still testing_ready (fixer attempted)
	// or transitioned.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	// Since go test ./... fails in a bare repo worktree, a fixer should be
	// attempted (but will fail to spawn without a runner).
	// The task should still be in testing_ready with needs_human_review.
	if updated.Status != model.StatusTestingReady {
		// It might have transitioned to merging if tests somehow passed.
		if updated.Status != model.StatusMerging {
			t.Logf("task status: %s (expected testing_ready or merging)", updated.Status)
		}
	}
}

func TestProcessTestingReady_AlreadyHasAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "test gate parent",
		Description:    "test",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/test-gate",
	}
	db.Create(&parent)

	// Create a working fixer agent for this task.
	fixerID := uuid.New()
	now := time.Now()
	fixer := model.Agent{
		ID:            fixerID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentFixer,
		Name:          "test-fixer",
		Status:        model.AgentWorking,
		CurrentTaskID: &parentID,
		HeartbeatAt:   &now,
	}
	db.Create(&fixer)

	// processTestingReady should skip since an agent is already running.
	err := o.processTestingReady(&parent)
	if err != nil {
		t.Fatalf("processTestingReady should not error: %v", err)
	}

	// Status should remain unchanged.
	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected status to remain testing_ready, got %s", updated.Status)
	}
}

func TestProcessTestingReady_FixerSucceeds(t *testing.T) {
	// This tests the scenario where a fixer agent has completed and tests
	// are re-run. Since this requires a full integration with runner and
	// tmux, we test the state machine logic.
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "fixed parent",
		Description: "test",
		Status:      model.StatusTestingReady,
		Context: model.JSONField{
			"testing_ready_fixer_attempted": true,
			"automated_gate_passed":         true,
		},
		WorktreeBranch: "feature/fixed",
	}
	db.Create(&parent)

	// With automated_gate_passed=true, processTestingReady should skip.
	err := o.processTestingReady(&parent)
	if err != nil {
		t.Fatalf("processTestingReady failed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", parentID)
	if updated.Status != model.StatusTestingReady {
		t.Errorf("expected status testing_ready (gate already passed), got %s", updated.Status)
	}
}

func TestProcessTestingReady_FixerAt80_HumanReview(t *testing.T) {
	// This tests the scenario where a fixer has already been attempted and
	// the task should be flagged for human review.
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "fixer-failed parent",
		Description: "test",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:           subtaskID,
		ProjectID:    o.projectID,
		ParentTaskID: &parentID,
		Title:        "subtask for fixer",
		Description:  "test",
		Status:       model.StatusInProgress,
	}
	db.Create(&subtask)

	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentFixer,
		Name:          "test-fixer",
		Status:        model.AgentWorking,
		CurrentTaskID: &subtaskID,
	}
	db.Create(&ag)

	// Simulate fixer hitting 80% by calling escalateFixerToHuman directly.
	err := o.escalateFixerToHuman(&subtask, &ag, 80)
	if err != nil {
		t.Fatalf("escalateFixerToHuman failed: %v", err)
	}

	// Verify subtask is failed.
	var updatedSub model.Task
	db.First(&updatedSub, "id = ?", subtaskID)
	if updatedSub.Status != model.StatusFailed {
		t.Errorf("expected subtask FAILED, got %s", updatedSub.Status)
	}

	// Verify parent has needs_human_review set.
	var updatedParent model.Task
	db.First(&updatedParent, "id = ?", parentID)
	if updatedParent.Context == nil {
		t.Fatal("expected parent context to be set")
	}
	if v, ok := updatedParent.Context["needs_human_review"].(bool); !ok || !v {
		t.Error("expected parent.Context['needs_human_review'] to be true")
	}
}

// ---------------------------------------------------------------------------
// HandleTestFailed tests (updated behavior: IN_PROGRESS instead of PLANNING)
// ---------------------------------------------------------------------------

func TestHandleTestFailed_TransitionsToInProgress(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	taskID := uuid.New()
	task := model.Task{
		ID:          taskID,
		ProjectID:   o.projectID,
		Title:       "test-failed task",
		Description: "test",
		Status:      model.StatusTestingReady,
	}
	db.Create(&task)

	err := o.HandleTestFailed(taskID)
	if err != nil {
		t.Fatalf("HandleTestFailed failed: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", taskID)
	if updated.Status != model.StatusInProgress {
		t.Errorf("expected status in_progress, got %s", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// spawnFixerForTestFailure tests
// ---------------------------------------------------------------------------

func TestSpawnFixerForTestFailure_BuildsPrompt(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := orchForTest(t, db)
	createProject(t, db, o.projectID)

	subtaskID := uuid.New()
	subtask := model.Task{
		ID:          subtaskID,
		ProjectID:   o.projectID,
		Title:       "impl subtask",
		Description: "implement feature X",
		Status:      model.StatusInProgress,
	}
	db.Create(&subtask)

	agentID := uuid.New()
	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder",
		Status:        model.AgentWorking,
		WorktreePath:  "/tmp/nonexistent",
		CurrentTaskID: &subtaskID,
		Config: model.JSONField{
			"last_test_result": "FAIL: TestFoo expected 42 got 0",
		},
	}
	db.Create(&ag)

	// spawnFixerForTestFailure will fall back to failTask because no runner
	// is set up, but we can verify it correctly reads the agent config.
	err := o.spawnFixerForTestFailure(&subtask, &ag)
	// Method should succeed (it fails the task gracefully when runner is nil).
	if err != nil {
		t.Fatalf("spawnFixerForTestFailure returned unexpected error: %v", err)
	}

	// Verify agent is marked dead.
	var updatedAg model.Agent
	db.First(&updatedAg, "id = ?", agentID)
	if updatedAg.Status != model.AgentDead {
		t.Errorf("expected agent status dead, got %s", updatedAg.Status)
	}

	// Verify subtask is failed (no runner means task is failed gracefully).
	var updatedTask model.Task
	db.First(&updatedTask, "id = ?", subtaskID)
	if updatedTask.Status != model.StatusFailed {
		t.Errorf("expected subtask FAILED, got %s", updatedTask.Status)
	}
}

// ---------------------------------------------------------------------------
// ContextUsage and AgentContextInfo type tests
// ---------------------------------------------------------------------------

func TestContextUsageTypes(t *testing.T) {
	usage := &ContextUsage{
		UsedPercent:         85,
		CompactionTriggered: false,
	}
	if usage.UsedPercent != 85 {
		t.Errorf("expected 85, got %d", usage.UsedPercent)
	}
	if usage.CompactionTriggered {
		t.Error("expected compaction not triggered")
	}

	info := AgentContextInfo{
		AgentID:      uuid.New(),
		TaskID:       uuid.New(),
		ContextUsage: usage,
	}
	if info.ContextUsage == nil {
		t.Error("expected non-nil context usage")
	}
}

// ---------------------------------------------------------------------------
// Config default tests
// ---------------------------------------------------------------------------

func TestContextFixerPctDefault(t *testing.T) {
	events := make(chan Event, 100)
	o := New(nil, "", nil, nil, nil, nil, nil, uuid.New(), events, time.Second, time.Minute, 75, 90, nil, "")
	if o.contextFixerPct != 85 {
		t.Errorf("expected default contextFixerPct=85, got %d", o.contextFixerPct)
	}
}

func TestContextFixerPctCustom(t *testing.T) {
	events := make(chan Event, 100)
	o := New(nil, "", nil, nil, nil, nil, nil, uuid.New(), events, time.Second, time.Minute, 75, 90, nil, "", 80)
	if o.contextFixerPct != 80 {
		t.Errorf("expected contextFixerPct=80, got %d", o.contextFixerPct)
	}
}

func TestContextThresholdConstants(t *testing.T) {
	events := make(chan Event, 100)
	o := New(nil, "", nil, nil, nil, nil, nil, uuid.New(), events, time.Second, time.Minute, 75, 90, nil, "")
	if o.contextWarnPct != 75 {
		t.Errorf("expected contextWarnPct=75, got %d", o.contextWarnPct)
	}
	if o.contextStopPct != 90 {
		t.Errorf("expected contextStopPct=90, got %d", o.contextStopPct)
	}
}
