package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Stub dispatcher for controlling merge results in tests
// ---------------------------------------------------------------------------

// stubMerger implements MergeDispatcher, returning preconfigured results.
// results is consumed in order; after exhaustion it returns the last result.
type stubMerger struct {
	results []stubMergeResult
	calls   int
}

type stubMergeResult struct {
	result *MergeResult
	err    error
}

func (s *stubMerger) Dispatch(_ context.Context, _ *model.Task) (*MergeResult, error) {
	idx := s.calls
	if idx >= len(s.results) {
		idx = len(s.results) - 1
	}
	s.calls++
	return s.results[idx].result, s.results[idx].err
}

// ---------------------------------------------------------------------------
// Test setup
// ---------------------------------------------------------------------------

func setupMergeTest(t *testing.T, merger *stubMerger) (*Orchestrator, *gorm.DB, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)

	project := model.Project{
		ID:           projectID,
		Name:         "merge-test-" + projectID.String()[:8],
		BareRepoPath: "/tmp/fake-bare-repo",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	o := &Orchestrator{
		db:              db,
		projectID:       projectID,
		mergeDispatcher: merger,
		worktree:        &FakeWorktreeManager{BarePath: "/tmp/fake-bare-repo", Default: "main"},
		events:          events,
		logger:          slog.Default().With("component", "merge-test"),
	}
	return o, db, projectID
}

func createMergingTask(t *testing.T, db *gorm.DB, projectID uuid.UUID, category model.TaskCategory) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "merge-test-task",
		Description:    "task for merge execution test",
		Status:         model.StatusMerging,
		Category:       category,
		WorktreeBranch: "feature/test-branch",
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("create merging task: %v", err)
	}
	return task
}

// ---------------------------------------------------------------------------
// RetryPolicy tests
// ---------------------------------------------------------------------------

func TestRetryPolicy_DefaultValues(t *testing.T) {
	p := DefaultMergeRetryPolicy()
	if p.MaxRetries != MaxMergeRetries {
		t.Errorf("DefaultMergeRetryPolicy().MaxRetries = %d, want %d", p.MaxRetries, MaxMergeRetries)
	}
	if p.BaseDelay != mergeRetryBaseDelay {
		t.Errorf("DefaultMergeRetryPolicy().BaseDelay = %v, want %v", p.BaseDelay, mergeRetryBaseDelay)
	}
}

func TestRetryPolicy_Delay_ExponentialBackoff(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5, BaseDelay: 10 * time.Second, MaxDelay: 5 * time.Minute}

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 10 * time.Second},  // base * 2^0
		{2, 20 * time.Second},  // base * 2^1
		{3, 40 * time.Second},  // base * 2^2
		{4, 80 * time.Second},  // base * 2^3
		{5, 160 * time.Second}, // base * 2^4
	}

	for _, tc := range tests {
		got := p.Delay(tc.attempt)
		if got != tc.want {
			t.Errorf("Delay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestRetryPolicy_Delay_CappedAtMaxDelay(t *testing.T) {
	p := RetryPolicy{MaxRetries: 10, BaseDelay: 10 * time.Second, MaxDelay: 30 * time.Second}

	// Attempt 3: 40s would exceed 30s cap
	got := p.Delay(3)
	if got != 30*time.Second {
		t.Errorf("Delay(3) with 30s cap = %v, want 30s", got)
	}
}

func TestRetryPolicy_Exhausted(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5}

	tests := []struct {
		attempt int
		want    bool
	}{
		{0, false},
		{1, false},
		{4, false},
		{5, true},
		{6, true},
	}

	for _, tc := range tests {
		got := p.Exhausted(tc.attempt)
		if got != tc.want {
			t.Errorf("Exhausted(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// MergeAttemptState tests
// ---------------------------------------------------------------------------

func TestMergeAttemptState_LoadFromEmptyContext(t *testing.T) {
	task := &model.Task{Context: nil}
	state := LoadMergeAttemptState(task)
	if state.AttemptCount() != 0 {
		t.Errorf("AttemptCount from nil context = %d, want 0", state.AttemptCount())
	}
}

func TestMergeAttemptState_LoadFromExistingContext(t *testing.T) {
	task := &model.Task{
		Context: model.JSONField{contextKeyMergeAttemptCount: float64(3)},
	}
	state := LoadMergeAttemptState(task)
	if state.AttemptCount() != 3 {
		t.Errorf("AttemptCount from context = %d, want 3", state.AttemptCount())
	}
}

func TestMergeAttemptState_Increment(t *testing.T) {
	state := MergeAttemptState{attemptCount: 0}
	state.Increment()
	if state.AttemptCount() != 1 {
		t.Errorf("AttemptCount after increment = %d, want 1", state.AttemptCount())
	}
	state.Increment()
	if state.AttemptCount() != 2 {
		t.Errorf("AttemptCount after second increment = %d, want 2", state.AttemptCount())
	}
}

func TestMergeAttemptState_Save(t *testing.T) {
	task := &model.Task{Context: nil}
	state := MergeAttemptState{attemptCount: 4}
	state.Save(task)

	if task.Context == nil {
		t.Fatal("Save did not initialize Context map")
	}
	raw, ok := task.Context[contextKeyMergeAttemptCount]
	if !ok {
		t.Fatal("Save did not write merge_attempt_count to Context")
	}
	// Context stores as float64 when marshaled through JSON
	count, ok := raw.(int)
	if !ok {
		t.Fatalf("merge_attempt_count type = %T, want int", raw)
	}
	if count != 4 {
		t.Errorf("merge_attempt_count = %d, want 4", count)
	}
}

// ---------------------------------------------------------------------------
// executeMerge retry behavior tests
// ---------------------------------------------------------------------------

func TestExecuteMerge_SuccessOnFirstAttempt(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: true, MergeCommit: "abc123"}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("status = %q, want %q", updated.Status, model.StatusDone)
	}
}

func TestExecuteMerge_TransientFailure_StaysInMerging(t *testing.T) {
	// A transient failure (Success=false, no Conflicts) should NOT immediately
	// fail the task. It should stay in MERGING and increment the attempt count.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusMerging {
		t.Errorf("status after transient failure = %q, want %q (should stay in merging for retry)",
			updated.Status, model.StatusMerging)
	}

	// Verify attempt count was incremented
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 1 {
		t.Errorf("merge_attempt_count = %d, want 1", state.AttemptCount())
	}
}

func TestExecuteMerge_RetriesUpToMaxThenFails(t *testing.T) {
	// Simulate MaxMergeRetries transient failures via the tick loop calling
	// executeMerge repeatedly on a task stuck in MERGING.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	// Simulate tick loop: call executeMerge MaxMergeRetries times
	for i := 0; i < MaxMergeRetries; i++ {
		if err := o.executeMerge(task); err != nil {
			t.Fatalf("executeMerge attempt %d: %v", i+1, err)
		}
		// Reload task to get updated context
		db.First(task, "id = ?", task.ID)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("status after %d retries = %q, want %q",
			MaxMergeRetries, updated.Status, model.StatusFailed)
	}

	// Verify failure reason includes attempt count
	reason, _ := updated.Context["failure_reason"].(string)
	if reason == "" {
		t.Error("failure_reason not set in context")
	}
	wantSubstr := fmt.Sprintf("%d", MaxMergeRetries)
	if len(reason) > 0 && !containsSubstring(reason, wantSubstr) {
		t.Errorf("failure_reason %q should mention attempt count %s", reason, wantSubstr)
	}
}

func TestExecuteMerge_ExponentialBackoff_TrackedViaAttemptCount(t *testing.T) {
	// Each tick-based call to executeMerge should check whether enough time
	// has elapsed (based on the retry policy delay for the current attempt).
	// This test verifies that merge_attempt_count is incremented on each
	// retry and that the backoff delay grows.
	policy := DefaultMergeRetryPolicy()

	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	// First call: attempt 1
	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}
	db.First(task, "id = ?", task.ID)
	state := LoadMergeAttemptState(task)
	if state.AttemptCount() != 1 {
		t.Errorf("attempt count after first call = %d, want 1", state.AttemptCount())
	}

	// Verify increasing delays
	delay1 := policy.Delay(1)
	delay2 := policy.Delay(2)
	if delay2 <= delay1 {
		t.Errorf("Delay(2)=%v should be > Delay(1)=%v for exponential backoff", delay2, delay1)
	}
}

func TestExecuteMerge_ConflictFailsImmediately(t *testing.T) {
	// Real merge conflicts (non-empty Conflicts) should fail immediately,
	// bypassing retry logic entirely.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{
			Success:   false,
			Conflicts: []string{".gitignore", "main.go"},
		}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("status with conflicts = %q, want %q (should fail immediately)",
			updated.Status, model.StatusFailed)
	}

	// Should NOT have any retry attempts
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 0 {
		t.Errorf("attempt count with conflicts = %d, want 0 (no retry for real conflicts)",
			state.AttemptCount())
	}
}

func TestExecuteMerge_SuccessOnRetry(t *testing.T) {
	// Merge fails transiently twice, then succeeds on the third attempt.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
		{result: &MergeResult{Success: true, MergeCommit: "def456"}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	// Simulate 3 ticks
	for i := 0; i < 3; i++ {
		if err := o.executeMerge(task); err != nil {
			t.Fatalf("executeMerge attempt %d: %v", i+1, err)
		}
		db.First(task, "id = ?", task.ID)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusDone {
		t.Errorf("status after retry success = %q, want %q", updated.Status, model.StatusDone)
	}
	if merger.calls != 3 {
		t.Errorf("merger called %d times, want 3", merger.calls)
	}
}

func TestExecuteMerge_QuickFixFailsImmediately(t *testing.T) {
	// Quick fix tasks should fail immediately on merge failure without retry.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryQuickFix)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusFailed {
		t.Errorf("quickfix status = %q, want %q (should fail immediately without retry)",
			updated.Status, model.StatusFailed)
	}

	// Should have no retry attempts
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 0 {
		t.Errorf("quickfix attempt count = %d, want 0", state.AttemptCount())
	}
}

func TestExecuteMerge_AttemptCountIncrementedEachTick(t *testing.T) {
	// Verify that merge_attempt_count in task.Context is incremented on each
	// tick-based retry attempt, providing visibility into retry progress.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	for want := 1; want <= 3; want++ {
		if err := o.executeMerge(task); err != nil {
			t.Fatalf("executeMerge tick %d: %v", want, err)
		}
		db.First(task, "id = ?", task.ID)
		state := LoadMergeAttemptState(task)
		if state.AttemptCount() != want {
			t.Errorf("tick %d: merge_attempt_count = %d, want %d",
				want, state.AttemptCount(), want)
		}
	}
}

func TestExecuteMerge_FailureReasonIncludesAttemptCount(t *testing.T) {
	// After max retries, the failure reason should be descriptive and include
	// the number of attempts made.
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, Conflicts: nil}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	for i := 0; i < MaxMergeRetries; i++ {
		if err := o.executeMerge(task); err != nil {
			t.Fatalf("executeMerge attempt %d: %v", i+1, err)
		}
		db.First(task, "id = ?", task.ID)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)

	reason, ok := updated.Context["failure_reason"].(string)
	if !ok || reason == "" {
		t.Fatal("failure_reason not set after max retries")
	}

	// Should mention attempts and be descriptive
	if !containsSubstring(reason, "5") {
		t.Errorf("failure_reason %q should include attempt count", reason)
	}
	if !containsSubstring(reason, "merge") {
		t.Errorf("failure_reason %q should mention merge", reason)
	}
}

func TestExecuteMerge_MergerError_ReturnsError(t *testing.T) {
	// If the merger returns a Go error (not a merge failure), executeMerge
	// should return the error directly without retry.
	merger := &stubMerger{results: []stubMergeResult{
		{result: nil, err: fmt.Errorf("git crashed")},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	err := o.executeMerge(task)
	if err == nil {
		t.Fatal("executeMerge should return error when merger errors")
	}
}

// containsSubstring and searchSubstring are defined in depth_wiring_test.go
