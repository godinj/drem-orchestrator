package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
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
	results            []stubMergeResult
	calls              int
	forceTargetAdvance bool
	advanceTarget      func(*model.Task)
}

type stubMergeResult struct {
	result *MergeResult
	err    error
}

func (s *stubMerger) Dispatch(_ context.Context, task *model.Task) (*MergeResult, error) {
	idx := s.calls
	if idx >= len(s.results) {
		idx = len(s.results) - 1
	}
	s.calls++
	selected := s.results[idx]
	if s.advanceTarget != nil && selected.result != nil && (selected.result.Success || s.forceTargetAdvance) {
		s.advanceTarget(task)
	}
	return selected.result, selected.err
}

// ---------------------------------------------------------------------------
// Test setup
// ---------------------------------------------------------------------------

func setupMergeTest(t *testing.T, merger *stubMerger) (*Orchestrator, *gorm.DB, uuid.UUID) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	events := make(chan Event, 100)
	bareRepo := testutil.SetupBareRepo(t)
	defaultBranch, err := testutil.RunGit([]string{"symbolic-ref", "--short", "HEAD"}, bareRepo)
	if err != nil {
		t.Fatalf("detect default branch: %v", err)
	}
	featureDir := filepath.Join(t.TempDir(), "feature-test-branch")
	if _, err := testutil.RunGit([]string{"worktree", "add", "-b", "feature/test-branch", featureDir, defaultBranch}, bareRepo); err != nil {
		t.Fatalf("create feature worktree: %v", err)
	}
	testutil.CommitFile(t, featureDir, "merge-feature.txt", "authorized feature\n", "authorized feature")
	merger.advanceTarget = func(task *model.Task) {
		if _, err := testutil.RunGit([]string{"update-ref", "refs/heads/" + defaultBranch, task.WorktreeBranch}, bareRepo); err != nil {
			t.Fatalf("advance authoritative target ref: %v", err)
		}
	}
	for i := range merger.results {
		if merger.results[i].result != nil && merger.results[i].result.Success && !validObjectID(merger.results[i].result.MergeCommit) {
			merger.results[i].result.MergeCommit = strings.Repeat("c", 40)
		}
	}

	project := model.Project{
		ID:            projectID,
		Name:          "merge-test-" + projectID.String()[:8],
		BareRepoPath:  bareRepo,
		DefaultBranch: defaultBranch,
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	o := &Orchestrator{
		db:              db,
		projectID:       projectID,
		mergeDispatcher: merger,
		worktree:        &FakeWorktreeManager{BarePath: bareRepo, Default: defaultBranch, Features: map[string]string{"test-branch": featureDir}},
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
	var project model.Project
	if err := db.First(&project, "id = ?", projectID).Error; err != nil {
		t.Fatalf("load project: %v", err)
	}
	commitSHA, err := testutil.RunGit([]string{"rev-parse", task.WorktreeBranch}, project.BareRepoPath)
	if err != nil {
		t.Fatalf("resolve feature sha: %v", err)
	}
	baseSHA, err := testutil.RunGit([]string{"rev-parse", project.DefaultBranch}, project.BareRepoPath)
	if err != nil {
		t.Fatalf("resolve base sha: %v", err)
	}
	artifactID, verificationID := uuid.New(), uuid.New()
	if err := db.Create(&model.DeliveryArtifact{
		ID: artifactID, TaskID: task.ID, ArtifactVersion: 1, Branch: task.WorktreeBranch,
		CommitSHA: commitSHA, BaseBranch: project.DefaultBranch, BaseSHA: baseSHA,
		PreliminaryEvidence: model.JSONField{"commands": []string{"go test ./..."}}, CreatorActor: "test", CreatorSource: "test",
	}).Error; err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if err := db.Create(&model.VerificationRecord{
		ID: verificationID, TaskID: task.ID, DeliveryArtifactID: artifactID, ArtifactVersion: 1,
		CommitSHA: commitSHA, VerifierActor: "test", EnvironmentFingerprint: "test",
		CommandEvidence: model.JSONField{"commands": []string{"go test ./..."}}, Result: model.VerificationPassed,
		IdempotencyKey: "verify-" + task.ID.String(), RequestHash: "test",
	}).Error; err != nil {
		t.Fatalf("create verification: %v", err)
	}
	if err := db.Create(&model.IntegrationAuthorization{
		ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifactID, VerificationRecordID: verificationID,
		ArtifactVersion: 1, CommitSHA: commitSHA, BaseSHA: baseSHA, Actor: "test", Source: "test",
		IdempotencyKey: "integrate-" + task.ID.String(), RequestHash: "test",
	}).Error; err != nil {
		t.Fatalf("create integration authorization: %v", err)
	}
	return task
}

func seedAuthorizedMergeEvidence(t *testing.T, o *Orchestrator, task *model.Task) {
	t.Helper()
	worktreePath := o.resolveIntegrationWorktree(task)
	if worktreePath == "" {
		t.Fatal("delivery worktree unavailable")
	}
	commitSHA, err := testutil.RunGit([]string{"rev-parse", task.WorktreeBranch}, worktreePath)
	if err != nil {
		t.Fatalf("resolve delivery commit: %v", err)
	}
	baseBranch := o.worktree.DefaultBranchName()
	baseSHA, err := testutil.RunGit([]string{"rev-parse", baseBranch}, worktreePath)
	if err != nil {
		t.Fatalf("resolve delivery base: %v", err)
	}
	artifactID, verificationID := uuid.New(), uuid.New()
	requireNoTestDBError(t, o.db.Create(&model.DeliveryArtifact{
		ID: artifactID, TaskID: task.ID, ArtifactVersion: 1, Branch: task.WorktreeBranch,
		CommitSHA: commitSHA, BaseBranch: baseBranch, BaseSHA: baseSHA,
		PreliminaryEvidence: model.JSONField{"commands": []string{"go test ./..."}}, CreatorActor: "test", CreatorSource: "test",
	}).Error)
	requireNoTestDBError(t, o.db.Create(&model.VerificationRecord{
		ID: verificationID, TaskID: task.ID, DeliveryArtifactID: artifactID, ArtifactVersion: 1,
		CommitSHA: commitSHA, VerifierActor: "test", EnvironmentFingerprint: "test",
		CommandEvidence: model.JSONField{"commands": []string{"go test ./..."}}, Result: model.VerificationPassed,
		IdempotencyKey: "verify-" + task.ID.String(), RequestHash: "test",
	}).Error)
	requireNoTestDBError(t, o.db.Create(&model.IntegrationAuthorization{
		ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifactID, VerificationRecordID: verificationID,
		ArtifactVersion: 1, CommitSHA: commitSHA, BaseSHA: baseSHA, Actor: "test", Source: "test",
		IdempotencyKey: "integrate-" + task.ID.String(), RequestHash: "test",
	}).Error)
}

func requireNoTestDBError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed authorized delivery evidence: %v", err)
	}
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

func TestExecuteMerge_RecoversCompletionWhenTelemetryIsLostAfterPush(t *testing.T) {
	merger := &stubMerger{
		results:            []stubMergeResult{{result: &MergeResult{Success: false, FailureReason: "unknown"}}},
		forceTargetAdvance: true,
	}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}
	var updated model.Task
	requireNoTestDBError(t, db.First(&updated, "id = ?", task.ID).Error)
	if updated.Status != model.StatusDone {
		t.Fatalf("status = %q, want done", updated.Status)
	}
	var completion model.MergeCompletion
	requireNoTestDBError(t, db.First(&completion, "task_id = ?", task.ID).Error)
	if completion.Source != "authoritative_target_ref" {
		t.Fatalf("completion source = %q", completion.Source)
	}
	var intent model.MergeIntent
	requireNoTestDBError(t, db.First(&intent, "id = ?", completion.MergeIntentID).Error)
	if completion.MergeCommitSHA != intent.ArtifactCommitSHA {
		t.Fatalf("merge SHA = %s, want target SHA %s", completion.MergeCommitSHA, intent.ArtifactCommitSHA)
	}
}

func TestExecuteMerge_RecoversAlreadyAdvancedTargetWithoutDispatch(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{{result: &MergeResult{Success: false}}}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)
	merger.advanceTarget(task)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}
	if merger.calls != 0 {
		t.Fatalf("dispatch calls = %d, want 0", merger.calls)
	}
	var updated model.Task
	requireNoTestDBError(t, db.First(&updated, "id = ?", task.ID).Error)
	if updated.Status != model.StatusDone {
		t.Fatalf("status = %q, want done", updated.Status)
	}
}

func TestExecuteMerge_DoesNotRecoverUnrelatedTargetAdvance(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{{result: &MergeResult{Success: true}}}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)
	bareRepo := o.worktree.BareRepo()
	baseSHA, err := testutil.RunGit([]string{"rev-parse", o.worktree.DefaultBranchName()}, bareRepo)
	if err != nil {
		t.Fatalf("resolve target base: %v", err)
	}
	treeSHA, err := testutil.RunGit([]string{"rev-parse", baseSHA + "^{tree}"}, bareRepo)
	if err != nil {
		t.Fatalf("resolve target tree: %v", err)
	}
	unrelatedSHA, err := testutil.RunGit([]string{"commit-tree", treeSHA, "-p", baseSHA, "-m", "unrelated target advance"}, bareRepo)
	if err != nil {
		t.Fatalf("create unrelated target commit: %v", err)
	}
	if _, err := testutil.RunGit([]string{"update-ref", "refs/heads/" + o.worktree.DefaultBranchName(), unrelatedSHA}, bareRepo); err != nil {
		t.Fatalf("advance target ref: %v", err)
	}

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}
	if merger.calls != 0 {
		t.Fatalf("dispatch calls = %d, want 0", merger.calls)
	}
	var updated model.Task
	requireNoTestDBError(t, db.First(&updated, "id = ?", task.ID).Error)
	if updated.Status != model.StatusInProgress {
		t.Fatalf("status = %q, want in_progress", updated.Status)
	}
	var completions int64
	requireNoTestDBError(t, db.Model(&model.MergeCompletion{}).Where("task_id = ?", task.ID).Count(&completions).Error)
	if completions != 0 {
		t.Fatalf("completion count = %d, want 0", completions)
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
	if got, _ := updated.Context[contextKeyTerminalMergerFailureReason].(string); got != terminalMergerFailureAttemptsExhausted {
		t.Errorf("terminal merger failure reason = %q, want %q", got, terminalMergerFailureAttemptsExhausted)
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

	if got, _ := updated.Context[contextKeyTerminalMergerFailureReason].(string); got != terminalMergerFailureConflict {
		t.Errorf("terminal merger failure reason = %q, want %q", got, terminalMergerFailureConflict)
	}

	// Should NOT have any retry attempts
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 0 {
		t.Errorf("attempt count with conflicts = %d, want 0 (no retry for real conflicts)",
			state.AttemptCount())
	}
}

func TestExecuteMerge_ConflictFirstAttemptSpawnsResolver(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, _ := newContainerSessionRig(t, "merge-conflict-resolver")
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{
			Success:       false,
			FailureReason: "conflict",
			Conflicts:     []string{"main.go", "internal/thing.go"},
		}, err: nil},
	}}
	o.mergeDispatcher = merger
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "merge conflict resolver",
		Description:    "task for resolver spawn",
		Status:         model.StatusMerging,
		Category:       model.CategoryStandard,
		WorktreeBranch: "feature/merge-conflict-resolver",
	}
	if err := o.db.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	seedAuthorizedMergeEvidence(t, o, task)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	if err := o.db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updated.Status != model.StatusMerging {
		t.Fatalf("status = %q, want %q", updated.Status, model.StatusMerging)
	}
	if _, ok := updated.Context[contextKeyTerminalMergerFailureReason]; ok {
		t.Fatal("terminal merger failure reason should not be set on first resolver attempt")
	}
	if got := mergeConflictResolverAttemptCount(&updated); got != 1 {
		t.Fatalf("resolver attempt count = %d, want 1", got)
	}
	if got, _ := updated.Context[contextKeyMergeConflictResolverState].(string); got != "running" {
		t.Fatalf("resolver state = %q, want running", got)
	}
	if got, _ := updated.Context[contextKeyMergeConflictResolverAgentID].(string); got == "" {
		t.Fatal("resolver agent id not recorded")
	}
	if len(fake.spawnCalls) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(fake.spawnCalls))
	}
	if fake.spawnCalls[0].AgentType != string(model.AgentFixer) {
		t.Fatalf("spawn agent type = %q, want fixer", fake.spawnCalls[0].AgentType)
	}
}

func TestExecuteMerge_ConflictBudgetExhaustedFailsTerminally(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, FailureReason: "conflict", Conflicts: []string{"main.go"}}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)
	task.Context = model.JSONField{contextKeyMergeConflictResolverAttemptCount: maxMergeConflictResolverAttempts}
	if err := db.Save(task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updated.Status != model.StatusFailed {
		t.Fatalf("status = %q, want %q", updated.Status, model.StatusFailed)
	}
	if got, _ := updated.Context[contextKeyTerminalMergerFailureReason].(string); got != terminalMergerFailureConflict {
		t.Fatalf("terminal merger failure reason = %q, want %q", got, terminalMergerFailureConflict)
	}
}

func TestExecuteMerge_ResolverSpawnFailureDoesNotConsumeBudget(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: false, FailureReason: "conflict", Conflicts: []string{"main.go"}}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	err := o.executeMerge(task)
	if err == nil {
		t.Fatal("executeMerge: expected resolver spawn error")
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if got := mergeConflictResolverAttemptCount(&updated); got != 0 {
		t.Fatalf("resolver attempt count = %d, want 0 when spawn fails", got)
	}
	if got, _ := updated.Context[contextKeyMergeConflictResolverState].(string); got != "spawn_failed" {
		t.Fatalf("resolver state = %q, want spawn_failed", got)
	}
	if _, ok := updated.Context[contextKeyTerminalMergerFailureReason]; ok {
		t.Fatal("terminal merger failure reason should not be set when resolver spawn fails")
	}
}

func TestExecuteMerge_ActiveResolverSkipsMergeDispatch(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{Success: true, MergeCommit: "should-not-run"}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)
	agentID := uuid.New()
	task.Context = model.JSONField{contextKeyMergeConflictResolverAgentID: agentID.String()}
	if err := db.Save(task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}
	ag := model.Agent{ID: agentID, ProjectID: projectID, AgentType: model.AgentFixer, Status: model.AgentWorking, CurrentTaskID: &task.ID}
	if err := db.Create(&ag).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}
	if merger.calls != 0 {
		t.Fatalf("merge dispatch calls = %d, want 0", merger.calls)
	}
}

func TestOnFixerCompleted_MergeConflictResolverKeepsTaskMerging(t *testing.T) {
	o, db, projectID := setupMergeTest(t, &stubMerger{})
	task := createMergingTask(t, db, projectID, model.CategoryStandard)
	agentID := uuid.New()
	task.AssignedAgentID = &agentID
	task.Context = model.JSONField{
		contextKeyMergeConflictResolverAgentID: agentID.String(),
		contextKeyMergeConflictResolverState:   "running",
	}
	if err := db.Save(task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}
	ag := &model.Agent{ID: agentID, ProjectID: projectID, AgentType: model.AgentFixer, Status: model.AgentWorking, CurrentTaskID: &task.ID}
	if err := db.Create(ag).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	if err := o.onFixerCompleted(ag, task); err != nil {
		t.Fatalf("onFixerCompleted: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updated.Status != model.StatusMerging {
		t.Fatalf("status = %q, want %q", updated.Status, model.StatusMerging)
	}
	if updated.AssignedAgentID != nil {
		t.Fatalf("assigned agent should be cleared, got %s", updated.AssignedAgentID)
	}
	if got, _ := updated.Context[contextKeyMergeConflictResolverState].(string); got != "completed" {
		t.Fatalf("resolver state = %q, want completed", got)
	}
}

func TestSpawnFixerSession_AllowsMergingOnlyWithMergeConflictFiles(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, _ := newContainerSessionRig(t, "fixer-merging-conflict")
	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "fixer merging conflict",
		Description:    "spawn fixer from merging",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/fixer-merging-conflict",
	}
	if err := o.db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := o.SpawnFixerSession(task.ID); err == nil {
		t.Fatal("expected SpawnFixerSession to reject MERGING without merge_conflict_files")
	}

	task.Context = model.JSONField{contextKeyMergeConflictFiles: []string{"main.go"}}
	if err := o.db.Save(&task).Error; err != nil {
		t.Fatalf("save task: %v", err)
	}
	if _, err := o.SpawnFixerSession(task.ID); err != nil {
		t.Fatalf("SpawnFixerSession with merge_conflict_files: %v", err)
	}
	if len(fake.spawnCalls) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(fake.spawnCalls))
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

// TestExecuteMerge_TestsFailedFailsImmediately verifies that a drem-merger
// exit code 3 (tests_failed) routes straight to StatusFailed without
// consuming a retry attempt. Tests-failed cannot heal by retrying the
// same merge — the feature branch itself is bad and needs a code change.
func TestExecuteMerge_TestsFailedFailsImmediately(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{
			Success:       false,
			FailureReason: "tests_failed",
			ExitCode:      3,
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
		t.Errorf("status after tests_failed = %q, want %q (no retry)",
			updated.Status, model.StatusFailed)
	}
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 0 {
		t.Errorf("attempt count after tests_failed = %d, want 0 (no retry)",
			state.AttemptCount())
	}
}

// TestExecuteMerge_PushFailedRetries verifies that a drem-merger exit
// code 4 (push_failed) is treated as transient and flows through the
// existing retry branch — a remote that advanced during the merge
// typically heals on the next attempt.
func TestExecuteMerge_PushFailedRetries(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{
			Success:       false,
			FailureReason: "push_failed",
			ExitCode:      4,
		}, err: nil},
	}}
	o, db, projectID := setupMergeTest(t, merger)
	task := createMergingTask(t, db, projectID, model.CategoryStandard)

	if err := o.executeMerge(task); err != nil {
		t.Fatalf("executeMerge: %v", err)
	}

	var updated model.Task
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != model.StatusMerging {
		t.Errorf("status after push_failed = %q, want %q (should retry)",
			updated.Status, model.StatusMerging)
	}
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 1 {
		t.Errorf("attempt count after push_failed = %d, want 1", state.AttemptCount())
	}
}

// TestExecuteMerge_MiscExitFailsImmediately verifies that drem-merger
// exit code 1 (misc — structural/config error) routes straight to
// StatusFailed. Config errors never heal by retry; failing loud beats
// silently burning the retry budget.
func TestExecuteMerge_MiscExitFailsImmediately(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{
			Success:       false,
			FailureReason: "misc",
			ExitCode:      1,
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
		t.Errorf("status after misc exit = %q, want %q (no retry)",
			updated.Status, model.StatusFailed)
	}
	state := LoadMergeAttemptState(&updated)
	if state.AttemptCount() != 0 {
		t.Errorf("attempt count after misc exit = %d, want 0", state.AttemptCount())
	}
}

// TestExecuteMerge_UnknownExitFailsImmediately verifies that an exit
// code outside the documented {0,1,2,3,4} set (mapped to
// FailureReason "unknown") fails loudly instead of silently retrying.
func TestExecuteMerge_UnknownExitFailsImmediately(t *testing.T) {
	merger := &stubMerger{results: []stubMergeResult{
		{result: &MergeResult{
			Success:       false,
			FailureReason: "unknown",
			ExitCode:      99,
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
		t.Errorf("status after unknown exit = %q, want %q", updated.Status, model.StatusFailed)
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
