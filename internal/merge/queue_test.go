package merge

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ---------------------------------------------------------------------------
// Mock types
// ---------------------------------------------------------------------------

type mockMerger struct {
	mergeFeatureFn func(task *model.Task) (*worktree.MergeResult, error)
	mergeAgentFn   func(agentBranch, featureWorktree string) (*worktree.MergeResult, error)
}

func (m *mockMerger) MergeFeatureIntoMain(task *model.Task) (*worktree.MergeResult, error) {
	return m.mergeFeatureFn(task)
}

func (m *mockMerger) MergeAgentIntoFeature(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
	return m.mergeAgentFn(agentBranch, featureWorktree)
}

type mockRebaser struct {
	findWorktreeFn func(branch string) (string, error)
	mainPathFn     func() (string, error)
	rebaseFn       func(featureWorktree, mainWorktree string) (*worktree.RebaseResult, error)
}

func (m *mockRebaser) FindWorktreeByBranch(branch string) (string, error) {
	return m.findWorktreeFn(branch)
}

func (m *mockRebaser) MainWorktreePath() (string, error) {
	return m.mainPathFn()
}

func (m *mockRebaser) RebaseBranchOnto(featureWorktree, mainWorktree string) (*worktree.RebaseResult, error) {
	return m.rebaseFn(featureWorktree, mainWorktree)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func successResult(src, tgt string) *worktree.MergeResult {
	return &worktree.MergeResult{
		Success:      true,
		SourceBranch: src,
		TargetBranch: tgt,
		MergeCommit:  "abc123",
	}
}

func failResult(src, tgt string, conflicts []string) *worktree.MergeResult {
	return &worktree.MergeResult{
		Success:      false,
		SourceBranch: src,
		TargetBranch: tgt,
		Conflicts:    conflicts,
	}
}

func newTask(branch string) *model.Task {
	return &model.Task{WorktreeBranch: branch}
}

func defaultRebaser() *mockRebaser {
	return &mockRebaser{
		findWorktreeFn: func(branch string) (string, error) {
			return "/tmp/wt/" + branch, nil
		},
		mainPathFn: func() (string, error) {
			return "/tmp/wt/main", nil
		},
		rebaseFn: func(_, _ string) (*worktree.RebaseResult, error) {
			return &worktree.RebaseResult{Success: true}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMergeQueue_Serialization(t *testing.T) {
	// Two concurrent MergeFeatureIntoMain calls must not overlap.
	var running atomic.Int32
	var maxConcurrent atomic.Int32

	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			cur := running.Add(1)
			for {
				old := maxConcurrent.Load()
				if cur <= old {
					break
				}
				if maxConcurrent.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
			return successResult(task.WorktreeBranch, "master"), nil
		},
	}

	q := NewMergeQueue(merger, defaultRebaser())

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			q.MergeFeatureIntoMain(newTask(fmt.Sprintf("feature/%d", n)))
		}(i)
	}
	wg.Wait()

	if maxConcurrent.Load() > 1 {
		t.Fatalf("expected serialized execution, but saw %d concurrent calls", maxConcurrent.Load())
	}
}

func TestMergeQueue_RebaseBeforeMerge(t *testing.T) {
	var rebaseCalled atomic.Bool
	var rebaseBeforeMerge atomic.Bool

	rebaser := defaultRebaser()
	rebaser.rebaseFn = func(featureWt, mainWt string) (*worktree.RebaseResult, error) {
		rebaseCalled.Store(true)
		return &worktree.RebaseResult{Success: true}, nil
	}

	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			// At the point merge is called, rebase must have already happened.
			if rebaseCalled.Load() {
				rebaseBeforeMerge.Store(true)
			}
			return successResult(task.WorktreeBranch, "master"), nil
		},
	}

	q := NewMergeQueue(merger, rebaser)
	result, err := q.MergeFeatureIntoMain(newTask("feature/abc"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rebaseCalled.Load() {
		t.Fatal("expected rebase to be called before merge")
	}
	if !rebaseBeforeMerge.Load() {
		t.Fatal("expected rebase to complete before merge was invoked")
	}
	if !result.Success {
		t.Fatal("expected successful merge result")
	}
}

func TestMergeQueue_RebaseConflictReturnsMergeResult(t *testing.T) {
	rebaser := defaultRebaser()
	rebaser.rebaseFn = func(_, _ string) (*worktree.RebaseResult, error) {
		return &worktree.RebaseResult{
			Success:   false,
			Conflicts: []string{"README.md", "ARCHITECTURE.md"},
			GitStderr: "CONFLICT (content): Merge conflict in README.md",
		}, nil
	}

	mergerCalled := false
	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			mergerCalled = true
			return successResult(task.WorktreeBranch, "master"), nil
		},
	}

	q := NewMergeQueue(merger, rebaser)
	result, err := q.MergeFeatureIntoMain(newTask("feature/conflicting"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failed result when rebase has conflicts")
	}
	if len(result.Conflicts) != 2 {
		t.Fatalf("expected 2 conflicts, got %d", len(result.Conflicts))
	}
	if result.Conflicts[0] != "README.md" || result.Conflicts[1] != "ARCHITECTURE.md" {
		t.Fatalf("unexpected conflicts: %v", result.Conflicts)
	}
	if mergerCalled {
		t.Fatal("underlying merger should not be called when rebase fails")
	}
}

func TestMergeQueue_AgentMergePassesThrough(t *testing.T) {
	var passedAgent, passedFeature string
	merger := &mockMerger{
		mergeAgentFn: func(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
			passedAgent = agentBranch
			passedFeature = featureWorktree
			return successResult(agentBranch, "feature/x"), nil
		},
	}

	q := NewMergeQueue(merger, defaultRebaser())
	result, err := q.MergeAgentIntoFeature("agent-123", "/tmp/wt/feature/x")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if passedAgent != "agent-123" {
		t.Fatalf("expected agent branch 'agent-123', got %q", passedAgent)
	}
	if passedFeature != "/tmp/wt/feature/x" {
		t.Fatalf("expected feature worktree '/tmp/wt/feature/x', got %q", passedFeature)
	}
	if !result.Success {
		t.Fatal("expected successful result")
	}
}

func TestMergeQueue_AgentMergeNotSerialized(t *testing.T) {
	// Agent merges should NOT be serialized — they can run concurrently.
	var running atomic.Int32
	var maxConcurrent atomic.Int32

	merger := &mockMerger{
		mergeAgentFn: func(agentBranch, featureWorktree string) (*worktree.MergeResult, error) {
			cur := running.Add(1)
			for {
				old := maxConcurrent.Load()
				if cur <= old {
					break
				}
				if maxConcurrent.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
			return successResult(agentBranch, "feature/x"), nil
		},
	}

	q := NewMergeQueue(merger, defaultRebaser())

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			q.MergeAgentIntoFeature(fmt.Sprintf("agent-%d", n), "/tmp/wt/feature/x")
		}(i)
	}
	wg.Wait()

	if maxConcurrent.Load() < 2 {
		t.Fatal("expected agent merges to run concurrently, but they appear serialized")
	}
}

func TestMergeQueue_PropagatesSuccess(t *testing.T) {
	expected := &worktree.MergeResult{
		Success:      true,
		SourceBranch: "feature/good",
		TargetBranch: "master",
		MergeCommit:  "deadbeef",
	}

	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			return expected, nil
		},
	}

	q := NewMergeQueue(merger, defaultRebaser())
	result, err := q.MergeFeatureIntoMain(newTask("feature/good"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SourceBranch != expected.SourceBranch {
		t.Fatalf("expected source %q, got %q", expected.SourceBranch, result.SourceBranch)
	}
	if result.MergeCommit != expected.MergeCommit {
		t.Fatalf("expected commit %q, got %q", expected.MergeCommit, result.MergeCommit)
	}
}

func TestMergeQueue_PropagatesFailure(t *testing.T) {
	expected := failResult("feature/bad", "master", []string{"conflict.go"})

	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			return expected, nil
		},
	}

	q := NewMergeQueue(merger, defaultRebaser())
	result, err := q.MergeFeatureIntoMain(newTask("feature/bad"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failed result to be propagated")
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0] != "conflict.go" {
		t.Fatalf("expected conflicts [conflict.go], got %v", result.Conflicts)
	}
}

func TestMergeQueue_PropagatesMergerError(t *testing.T) {
	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			return nil, fmt.Errorf("disk full")
		},
	}

	q := NewMergeQueue(merger, defaultRebaser())
	_, err := q.MergeFeatureIntoMain(newTask("feature/err"))

	if err == nil {
		t.Fatal("expected error to be propagated from underlying merger")
	}
	if err.Error() != "disk full" {
		t.Fatalf("expected 'disk full' error, got %q", err.Error())
	}
}

func TestMergeQueue_RebaseSuccessThenMergeSuccess(t *testing.T) {
	// End-to-end: rebase succeeds, merge succeeds, result returned.
	rebaseArgs := struct {
		featureWt string
		mainWt    string
	}{}

	rebaser := defaultRebaser()
	rebaser.rebaseFn = func(featureWt, mainWt string) (*worktree.RebaseResult, error) {
		rebaseArgs.featureWt = featureWt
		rebaseArgs.mainWt = mainWt
		return &worktree.RebaseResult{Success: true}, nil
	}
	rebaser.findWorktreeFn = func(branch string) (string, error) {
		return "/tmp/wt/" + branch, nil
	}
	rebaser.mainPathFn = func() (string, error) {
		return "/tmp/wt/main", nil
	}

	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			return successResult(task.WorktreeBranch, "master"), nil
		},
	}

	q := NewMergeQueue(merger, rebaser)
	result, err := q.MergeFeatureIntoMain(newTask("feature/e2e"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected successful result")
	}
	if rebaseArgs.featureWt != "/tmp/wt/feature/e2e" {
		t.Fatalf("expected feature worktree '/tmp/wt/feature/e2e', got %q", rebaseArgs.featureWt)
	}
	if rebaseArgs.mainWt != "/tmp/wt/main" {
		t.Fatalf("expected main worktree '/tmp/wt/main', got %q", rebaseArgs.mainWt)
	}
}

func TestMergeQueue_FeatureWorktreeNotFound_FallsBackToDirectMerge(t *testing.T) {
	// When the feature worktree can't be found for rebase, fall back to
	// direct merge without rebase (preserves current behavior).
	rebaseCalled := false
	rebaser := defaultRebaser()
	rebaser.findWorktreeFn = func(branch string) (string, error) {
		return "", fmt.Errorf("worktree not found for branch %q", branch)
	}
	rebaser.rebaseFn = func(_, _ string) (*worktree.RebaseResult, error) {
		rebaseCalled = true
		return &worktree.RebaseResult{Success: true}, nil
	}

	mergerCalled := false
	merger := &mockMerger{
		mergeFeatureFn: func(task *model.Task) (*worktree.MergeResult, error) {
			mergerCalled = true
			return successResult(task.WorktreeBranch, "master"), nil
		},
	}

	q := NewMergeQueue(merger, rebaser)
	result, err := q.MergeFeatureIntoMain(newTask("feature/missing-wt"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rebaseCalled {
		t.Fatal("rebase should not be called when feature worktree is not found")
	}
	if !mergerCalled {
		t.Fatal("expected direct merge fallback when worktree not found")
	}
	if !result.Success {
		t.Fatal("expected successful merge result")
	}
}
