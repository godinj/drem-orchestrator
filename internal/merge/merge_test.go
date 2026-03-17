package merge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

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
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree (integration branch)
	featureDir := filepath.Join(dir, "feature")
	testutil.AddWorktree(t, bareRepo, "feature/test", featureDir)

	// Create agent worktree
	agentDir := filepath.Join(dir, "agent")
	testutil.AddWorktree(t, bareRepo, "worktree-agent-abc", agentDir)

	// Feature gets a commit on a different file
	testutil.CommitFile(t, featureDir, "feature-file.txt", "feature work\n", "feature commit")

	// Agent gets a commit on a non-overlapping file
	testutil.CommitFile(t, agentDir, "agent-file.txt", "agent work\n", "agent commit")

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
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree
	featureDir := filepath.Join(dir, "feature")
	testutil.AddWorktree(t, bareRepo, "feature/test", featureDir)

	// Create agent worktree
	agentDir := filepath.Join(dir, "agent")
	testutil.AddWorktree(t, bareRepo, "worktree-agent-abc", agentDir)

	// Both modify the same file — conflicting changes
	testutil.CommitFile(t, featureDir, "shared.txt", "feature content\n", "feature change")
	testutil.CommitFile(t, agentDir, "shared.txt", "agent content\n", "agent change")

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
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create feature worktree
	featureDir := filepath.Join(dir, "feature")
	testutil.AddWorktree(t, bareRepo, "feature/test", featureDir)

	// Create agent worktree and commit, then remove the worktree
	// but keep the branch (simulating worktree already cleaned up)
	agentDir := filepath.Join(dir, "agent")
	testutil.AddWorktree(t, bareRepo, "worktree-agent-abc", agentDir)
	testutil.CommitFile(t, agentDir, "agent-file.txt", "agent work\n", "agent commit")

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
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	featureDir := filepath.Join(dir, "feature")
	testutil.AddWorktree(t, bareRepo, "feature/test", featureDir)

	agentDir := filepath.Join(dir, "agent")
	testutil.AddWorktree(t, bareRepo, "worktree-agent-abc", agentDir)

	// Create conflicting changes
	testutil.CommitFile(t, featureDir, "shared.txt", "feature line\n", "feature change")
	testutil.CommitFile(t, agentDir, "shared.txt", "agent line\n", "agent change")

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

// ---------------------------------------------------------------------------
// Pure helper tests
// ---------------------------------------------------------------------------

func TestIntersect(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want []string
	}{
		{
			name: "overlap",
			a:    []string{"a", "b", "c"},
			b:    []string{"b", "c", "d"},
			want: []string{"b", "c"},
		},
		{
			name: "no overlap",
			a:    []string{"a", "b"},
			b:    []string{"c", "d"},
			want: nil,
		},
		{
			name: "empty first",
			a:    []string{},
			b:    []string{"a", "b"},
			want: nil,
		},
		{
			name: "empty second",
			a:    []string{"a", "b"},
			b:    []string{},
			want: nil,
		},
		{
			name: "both empty",
			a:    []string{},
			b:    []string{},
			want: nil,
		},
		{
			name: "single element match",
			a:    []string{"a"},
			b:    []string{"a"},
			want: []string{"a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Intersect(tc.a, tc.b)
			if len(got) != len(tc.want) {
				t.Fatalf("Intersect(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Intersect(%v, %v)[%d] = %q, want %q", tc.a, tc.b, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDetectBuildCommand(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "go project",
			files:    []string{"go.mod"},
			wantCmd:  "go",
			wantArgs: []string{"test", "./..."},
		},
		{
			name:     "makefile project",
			files:    []string{"Makefile"},
			wantCmd:  "make",
			wantArgs: []string{"test"},
		},
		{
			name:     "node project",
			files:    []string{"package.json"},
			wantCmd:  "npm",
			wantArgs: []string{"test"},
		},
		{
			name:     "empty directory",
			files:    nil,
			wantCmd:  "",
			wantArgs: nil,
		},
		{
			name:     "go takes priority over makefile",
			files:    []string{"go.mod", "Makefile"},
			wantCmd:  "go",
			wantArgs: []string{"test", "./..."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0o644); err != nil {
					t.Fatalf("create file %s: %v", f, err)
				}
			}
			cmd, args := DetectBuildCommand(dir)
			if cmd != tc.wantCmd {
				t.Errorf("DetectBuildCommand() cmd = %q, want %q", cmd, tc.wantCmd)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("DetectBuildCommand() args = %v, want %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("DetectBuildCommand() args[%d] = %q, want %q", i, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()

	// Create a regular file
	existingFile := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(existingFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "existing file",
			path: existingFile,
			want: true,
		},
		{
			name: "nonexistent file",
			path: filepath.Join(dir, "no-such-file.txt"),
			want: false,
		},
		{
			name: "directory returns false",
			path: dir,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FileExists(tc.path)
			if got != tc.want {
				t.Errorf("FileExists(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
