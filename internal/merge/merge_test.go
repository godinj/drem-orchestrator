package merge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// ---------------------------------------------------------------------------
// Git test helpers (same pattern as internal/worktree/worktree_test.go)
// ---------------------------------------------------------------------------

// setupBareRepo creates a bare git repo with an initial commit.
func setupBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bareRepo := filepath.Join(dir, "test.git")

	if _, err := worktree.RunGit([]string{"init", "--bare", bareRepo}, ""); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}

	// Clone, make initial commit, push
	cloneDir := filepath.Join(dir, "clone")
	if _, err := worktree.RunGit([]string{"clone", bareRepo, cloneDir}, ""); err != nil {
		t.Fatalf("clone bare repo: %v", err)
	}
	worktree.RunGit([]string{"config", "user.email", "test@test.com"}, cloneDir)
	worktree.RunGit([]string{"config", "user.name", "Test"}, cloneDir)

	initFile := filepath.Join(cloneDir, "README.md")
	if err := os.WriteFile(initFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write init file: %v", err)
	}
	worktree.RunGit([]string{"add", "."}, cloneDir)
	if _, err := worktree.RunGit([]string{"commit", "-m", "initial commit"}, cloneDir); err != nil {
		t.Fatalf("initial commit: %v", err)
	}
	if _, err := worktree.RunGit([]string{"push", "origin", "HEAD"}, cloneDir); err != nil {
		t.Fatalf("push initial commit: %v", err)
	}

	return bareRepo
}

// addWorktree creates a worktree with a new branch.
func addWorktree(t *testing.T, bareRepo, branch, dir string) string {
	t.Helper()
	if _, err := worktree.RunGit([]string{"worktree", "add", "-b", branch, dir}, bareRepo); err != nil {
		t.Fatalf("add worktree %s: %v", branch, err)
	}
	worktree.RunGit([]string{"config", "user.email", "test@test.com"}, dir)
	worktree.RunGit([]string{"config", "user.name", "Test"}, dir)
	return dir
}

// commitFile creates/overwrites a file and commits it.
func commitFile(t *testing.T, wt, filename, content, message string) {
	t.Helper()
	fpath := filepath.Join(wt, filename)
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", filename, err)
	}
	if _, err := worktree.RunGit([]string{"add", filename}, wt); err != nil {
		t.Fatalf("git add %s: %v", filename, err)
	}
	if _, err := worktree.RunGit([]string{"commit", "-m", message}, wt); err != nil {
		t.Fatalf("commit %s: %v", message, err)
	}
}

// ---------------------------------------------------------------------------
// Mock worktree client for retry tests
// ---------------------------------------------------------------------------

// mockWorktreeClient implements mergeWorktreeClient for testing retry logic.
type mockWorktreeClient struct {
	findWorktreeByBranchFn func(branch string) (string, error)
	mergeBranchFn          func(sourceBranch, targetWorktree string) (*worktree.MergeResult, error)
}

func (m *mockWorktreeClient) FindWorktreeByBranch(branch string) (string, error) {
	return m.findWorktreeByBranchFn(branch)
}

func (m *mockWorktreeClient) MergeBranch(sourceBranch, targetWorktree string) (*worktree.MergeResult, error) {
	return m.mergeBranchFn(sourceBranch, targetWorktree)
}

// ---------------------------------------------------------------------------
// Integration tests using real git repos
// ---------------------------------------------------------------------------

func TestMergeAgentIntoFeature_CleanRebaseAndMerge(t *testing.T) {
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree (integration branch)
	featureDir := filepath.Join(dir, "feature")
	addWorktree(t, bareRepo, "feature/test", featureDir)

	// Create agent worktree
	agentDir := filepath.Join(dir, "agent")
	addWorktree(t, bareRepo, "worktree-agent-abc", agentDir)

	// Feature gets a commit on a different file
	commitFile(t, featureDir, "feature-file.txt", "feature work\n", "feature commit")

	// Agent gets a commit on a non-overlapping file
	commitFile(t, agentDir, "agent-file.txt", "agent work\n", "agent commit")

	mgr := worktree.NewManager(bareRepo, "main")
	orch := NewOrchestrator(mgr, nil)

	result, err := orch.MergeAgentIntoFeature("worktree-agent-abc", featureDir)
	if err != nil {
		t.Fatalf("MergeAgentIntoFeature returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success, got conflicts=%v stderr=%s", result.Conflicts, result.GitStderr)
	}
	if result.MergeCommit == "" {
		t.Error("MergeCommit should be populated on success")
	}

	// Verify the feature worktree has both files
	if _, err := os.Stat(filepath.Join(featureDir, "feature-file.txt")); os.IsNotExist(err) {
		t.Error("feature worktree should have feature-file.txt")
	}
	if _, err := os.Stat(filepath.Join(featureDir, "agent-file.txt")); os.IsNotExist(err) {
		t.Error("feature worktree should have agent-file.txt after merge")
	}
}

func TestMergeAgentIntoFeature_RebaseConflict(t *testing.T) {
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree
	featureDir := filepath.Join(dir, "feature")
	addWorktree(t, bareRepo, "feature/test", featureDir)

	// Create agent worktree
	agentDir := filepath.Join(dir, "agent")
	addWorktree(t, bareRepo, "worktree-agent-abc", agentDir)

	// Both modify the same file — conflicting changes
	commitFile(t, featureDir, "shared.txt", "feature content\n", "feature change")
	commitFile(t, agentDir, "shared.txt", "agent content\n", "agent change")

	mgr := worktree.NewManager(bareRepo, "main")
	orch := NewOrchestrator(mgr, nil)

	result, err := orch.MergeAgentIntoFeature("worktree-agent-abc", featureDir)
	if err != nil {
		t.Fatalf("MergeAgentIntoFeature returned error: %v", err)
	}

	if result.Success {
		t.Fatal("expected failure due to rebase conflict")
	}

	// Should have conflicts populated
	if len(result.Conflicts) == 0 {
		t.Error("Conflicts should be populated")
	}

	found := false
	for _, c := range result.Conflicts {
		if c == "shared.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Conflicts should contain 'shared.txt', got: %v", result.Conflicts)
	}

	// GitCommand should indicate rebase
	if !strings.Contains(result.GitCommand, "rebase") {
		t.Errorf("GitCommand should contain 'rebase', got: %s", result.GitCommand)
	}

	// Feature worktree should be clean (no merge was attempted)
	clean, err := worktree.IsClean(featureDir)
	if err != nil {
		t.Fatalf("check clean: %v", err)
	}
	if !clean {
		t.Error("feature worktree should be clean after rebase conflict")
	}

	// Agent worktree should also be clean (rebase was aborted)
	clean, err = worktree.IsClean(agentDir)
	if err != nil {
		t.Fatalf("check clean: %v", err)
	}
	if !clean {
		t.Error("agent worktree should be clean after rebase abort")
	}
}

func TestMergeAgentIntoFeature_AgentWorktreeMissing(t *testing.T) {
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree
	featureDir := filepath.Join(dir, "feature")
	addWorktree(t, bareRepo, "feature/test", featureDir)

	// Create agent worktree and commit, then remove the worktree
	// but keep the branch (simulating worktree already cleaned up)
	agentDir := filepath.Join(dir, "agent")
	addWorktree(t, bareRepo, "worktree-agent-abc", agentDir)
	commitFile(t, agentDir, "agent-file.txt", "agent work\n", "agent commit")

	// Remove the worktree (but the branch and commits remain in the bare repo)
	worktree.RunGit([]string{"worktree", "remove", agentDir, "--force"}, bareRepo)

	mgr := worktree.NewManager(bareRepo, "main")
	orch := NewOrchestrator(mgr, nil)

	// Should fall back to direct merge (no rebase since worktree is gone)
	result, err := orch.MergeAgentIntoFeature("worktree-agent-abc", featureDir)
	if err != nil {
		t.Fatalf("MergeAgentIntoFeature returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected success when agent worktree is missing, got conflicts=%v stderr=%s",
			result.Conflicts, result.GitStderr)
	}

	// Verify the agent's file was merged
	if _, err := os.Stat(filepath.Join(featureDir, "agent-file.txt")); os.IsNotExist(err) {
		t.Error("feature worktree should have agent-file.txt after fallback merge")
	}
}

// ---------------------------------------------------------------------------
// Retry tests using mock worktree client
// ---------------------------------------------------------------------------

func TestMergeWithRetry_TransientFailureThenSuccess(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockWorktreeClient{
		// Agent worktree not found — skip rebase
		findWorktreeByBranchFn: func(branch string) (string, error) {
			return "", fmt.Errorf("no worktree found for branch %q", branch)
		},
		mergeBranchFn: func(sourceBranch, targetWorktree string) (*worktree.MergeResult, error) {
			n := int(attempts.Add(1))
			if n == 1 {
				// First attempt: transient failure (no conflicts)
				return &worktree.MergeResult{
					Success:      false,
					SourceBranch: sourceBranch,
					GitStderr:    "fatal: Unable to create '...lock': File exists.",
				}, nil
			}
			// Second attempt: success
			return &worktree.MergeResult{
				Success:      true,
				SourceBranch: sourceBranch,
				MergeCommit:  "abc123",
			}, nil
		},
	}

	result, err := mergeWithRebaseAndRetry(mock, "agent-branch", "/tmp/feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Success {
		t.Fatal("expected success after retry")
	}

	if int(attempts.Load()) != 2 {
		t.Errorf("expected 2 merge attempts, got %d", attempts.Load())
	}
}

func TestMergeWithRetry_RealConflictNoRetry(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockWorktreeClient{
		findWorktreeByBranchFn: func(branch string) (string, error) {
			return "", fmt.Errorf("no worktree found")
		},
		mergeBranchFn: func(sourceBranch, targetWorktree string) (*worktree.MergeResult, error) {
			attempts.Add(1)
			return &worktree.MergeResult{
				Success:      false,
				SourceBranch: sourceBranch,
				Conflicts:    []string{"file.go"},
				GitStderr:    "CONFLICT (content): Merge conflict in file.go",
				GitCommand:   "git merge agent-branch --no-edit",
			}, nil
		},
	}

	result, err := mergeWithRebaseAndRetry(mock, "agent-branch", "/tmp/feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Fatal("expected failure with conflicts")
	}

	if int(attempts.Load()) != 1 {
		t.Errorf("expected exactly 1 merge attempt (no retry for real conflicts), got %d", attempts.Load())
	}

	if len(result.Conflicts) == 0 {
		t.Error("Conflicts should be populated")
	}
}

func TestMergeWithRetry_MaxRetriesExhausted(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockWorktreeClient{
		findWorktreeByBranchFn: func(branch string) (string, error) {
			return "", fmt.Errorf("no worktree found")
		},
		mergeBranchFn: func(sourceBranch, targetWorktree string) (*worktree.MergeResult, error) {
			attempts.Add(1)
			// Every attempt: transient failure (no conflicts)
			return &worktree.MergeResult{
				Success:      false,
				SourceBranch: sourceBranch,
				GitStderr:    "fatal: Unable to create lock",
			}, nil
		},
	}

	result, err := mergeWithRebaseAndRetry(mock, "agent-branch", "/tmp/feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Fatal("expected failure after max retries")
	}

	if int(attempts.Load()) != maxMergeRetries {
		t.Errorf("expected %d merge attempts, got %d", maxMergeRetries, attempts.Load())
	}
}

func TestMergeWithRetry_HardErrorNoRetry(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockWorktreeClient{
		findWorktreeByBranchFn: func(branch string) (string, error) {
			return "", fmt.Errorf("no worktree found")
		},
		mergeBranchFn: func(sourceBranch, targetWorktree string) (*worktree.MergeResult, error) {
			attempts.Add(1)
			return nil, fmt.Errorf("branch not resolvable after fetch")
		},
	}

	_, err := mergeWithRebaseAndRetry(mock, "agent-branch", "/tmp/feature")
	if err == nil {
		t.Fatal("expected error for hard failure")
	}

	if !strings.Contains(err.Error(), "merge attempt 1") {
		t.Errorf("error should wrap with attempt info, got: %v", err)
	}

	if int(attempts.Load()) != 1 {
		t.Errorf("expected exactly 1 merge attempt on hard error, got %d", attempts.Load())
	}
}

func TestMergeWithRetry_RebaseConflictNoRetry(t *testing.T) {
	// When rebase has a real conflict, no merge or retry should be attempted.
	// This test uses a real git repo to exercise the full rebase path.
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	featureDir := filepath.Join(dir, "feature")
	addWorktree(t, bareRepo, "feature/test", featureDir)

	agentDir := filepath.Join(dir, "agent")
	addWorktree(t, bareRepo, "worktree-agent-abc", agentDir)

	// Create conflicting changes
	commitFile(t, featureDir, "shared.txt", "feature line\n", "feature change")
	commitFile(t, agentDir, "shared.txt", "agent line\n", "agent change")

	mgr := worktree.NewManager(bareRepo, "main")

	// Use mergeWithRebaseAndRetry directly with the real manager
	result, err := mergeWithRebaseAndRetry(mgr, "worktree-agent-abc", featureDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Fatal("expected failure due to rebase conflict")
	}

	// Should contain conflict info from the rebase
	if result.GitCommand != "git rebase" {
		t.Errorf("GitCommand should be 'git rebase', got: %s", result.GitCommand)
	}
}

func TestMergeWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	var attempts atomic.Int32

	mock := &mockWorktreeClient{
		findWorktreeByBranchFn: func(branch string) (string, error) {
			return "", fmt.Errorf("no worktree found")
		},
		mergeBranchFn: func(sourceBranch, targetWorktree string) (*worktree.MergeResult, error) {
			attempts.Add(1)
			return &worktree.MergeResult{
				Success:      true,
				SourceBranch: sourceBranch,
				MergeCommit:  "def456",
			}, nil
		},
	}

	result, err := mergeWithRebaseAndRetry(mock, "agent-branch", "/tmp/feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Success {
		t.Fatal("expected success")
	}

	if int(attempts.Load()) != 1 {
		t.Errorf("expected exactly 1 attempt on immediate success, got %d", attempts.Load())
	}
}
