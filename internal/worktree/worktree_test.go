package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestMergeBranch_EnrichedResult_OnConflict(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree (simulates integration branch)
	targetDir := filepath.Join(dir, "target")
	testutil.AddWorktree(t,bareRepo, "feature/target", targetDir)

	// Create source worktree (simulates agent branch)
	sourceDir := filepath.Join(dir, "source")
	testutil.AddWorktree(t,bareRepo, "agent-source", sourceDir)

	// Make conflicting changes to the same file in both worktrees
	testutil.CommitFile(t,targetDir, "conflict.txt", "target content\n", "target change")
	testutil.CommitFile(t,sourceDir, "conflict.txt", "source content\n", "source change")

	// Attempt merge
	mgr := NewManager(bareRepo, "main")
	result, err := mgr.MergeBranch("agent-source", targetDir)
	if err != nil {
		t.Fatalf("MergeBranch returned error: %v", err)
	}

	if result.Success {
		t.Fatal("expected merge to fail with conflicts")
	}

	// Verify enriched fields are populated
	if result.GitStderr == "" {
		t.Error("GitStderr should be non-empty on failed merge")
	}
	if result.GitCommand == "" {
		t.Error("GitCommand should be non-empty on failed merge")
	}
	if !strings.Contains(result.GitCommand, "merge") {
		t.Errorf("GitCommand should contain 'merge', got: %s", result.GitCommand)
	}

	// Verify conflicts list
	if len(result.Conflicts) == 0 {
		t.Error("Conflicts should contain at least one file")
	}

	found := false
	for _, c := range result.Conflicts {
		if c == "conflict.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Conflicts should contain 'conflict.txt', got: %v", result.Conflicts)
	}

	// Verify the merge was aborted and the worktree is clean
	clean, err := IsClean(targetDir)
	if err != nil {
		t.Fatalf("check clean: %v", err)
	}
	if !clean {
		t.Error("target worktree should be clean after merge abort")
	}
}

func TestMergeBranch_SuccessfulMerge(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	testutil.AddWorktree(t,bareRepo, "feature/target", targetDir)

	// Create source worktree with non-overlapping changes
	sourceDir := filepath.Join(dir, "source")
	testutil.AddWorktree(t,bareRepo, "agent-source", sourceDir)

	testutil.CommitFile(t,sourceDir, "new-file.txt", "new content\n", "add new file")

	// Merge should succeed
	mgr := NewManager(bareRepo, "main")
	result, err := mgr.MergeBranch("agent-source", targetDir)
	if err != nil {
		t.Fatalf("MergeBranch returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected merge to succeed, got conflicts: %v, stderr: %s", result.Conflicts, result.GitStderr)
	}

	if result.MergeCommit == "" {
		t.Error("MergeCommit should be populated on success")
	}
	if result.SourceBranch != "agent-source" {
		t.Errorf("SourceBranch = %q, want %q", result.SourceBranch, "agent-source")
	}
	if result.TargetBranch != "feature/target" {
		t.Errorf("TargetBranch = %q, want %q", result.TargetBranch, "feature/target")
	}

	// GitStderr and GitCommand should be empty on success
	if result.GitStderr != "" {
		t.Errorf("GitStderr should be empty on success, got: %s", result.GitStderr)
	}
	if result.GitCommand != "" {
		t.Errorf("GitCommand should be empty on success, got: %s", result.GitCommand)
	}
}

func TestMergeBranch_PreMergeFetchHappyPath(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	testutil.AddWorktree(t,bareRepo, "feature/target", targetDir)

	// Create source worktree with a commit
	sourceDir := filepath.Join(dir, "source")
	testutil.AddWorktree(t,bareRepo, "agent-source", sourceDir)
	testutil.CommitFile(t,sourceDir, "file.txt", "content\n", "add file")

	// The ref "agent-source" should be visible (same bare repo) so
	// the pre-merge fetch step is skipped and the merge succeeds.
	mgr := NewManager(bareRepo, "main")
	result, err := mgr.MergeBranch("agent-source", targetDir)
	if err != nil {
		t.Fatalf("MergeBranch returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected merge to succeed, got conflicts: %v", result.Conflicts)
	}
}

func TestMergeBranch_UnresolvableBranch(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	testutil.AddWorktree(t,bareRepo, "feature/target", targetDir)

	// Attempt to merge a branch that doesn't exist
	mgr := NewManager(bareRepo, "main")
	_, err := mgr.MergeBranch("nonexistent-branch", targetDir)
	if err == nil {
		t.Fatal("expected error for nonexistent branch")
	}
	if !strings.Contains(err.Error(), "not resolvable after fetch") {
		t.Errorf("error should mention 'not resolvable after fetch', got: %v", err)
	}
}

func TestRebaseBranch_CleanRebase(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree (simulates integration branch)
	targetDir := filepath.Join(dir, "target")
	testutil.AddWorktree(t,bareRepo, "feature/target", targetDir)

	// Create source worktree (simulates agent branch)
	sourceDir := filepath.Join(dir, "source")
	testutil.AddWorktree(t,bareRepo, "agent-source", sourceDir)

	// Make non-overlapping changes
	testutil.CommitFile(t,targetDir, "target-file.txt", "target content\n", "target change")
	testutil.CommitFile(t,sourceDir, "source-file.txt", "source content\n", "source change")

	// Rebase should succeed
	result, err := RebaseBranch(sourceDir, targetDir)
	if err != nil {
		t.Fatalf("RebaseBranch returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("expected rebase to succeed, got stderr: %s", result.GitStderr)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got: %v", result.Conflicts)
	}

	// Verify the source worktree now has both files
	if _, err := os.Stat(filepath.Join(sourceDir, "target-file.txt")); os.IsNotExist(err) {
		t.Error("source worktree should have target-file.txt after rebase")
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "source-file.txt")); os.IsNotExist(err) {
		t.Error("source worktree should have source-file.txt after rebase")
	}
}

func TestRebaseBranch_Conflict(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	testutil.AddWorktree(t,bareRepo, "feature/target", targetDir)

	// Create source worktree
	sourceDir := filepath.Join(dir, "source")
	testutil.AddWorktree(t,bareRepo, "agent-source", sourceDir)

	// Make conflicting changes to the same file
	testutil.CommitFile(t,targetDir, "shared.txt", "target line\n", "target change")
	testutil.CommitFile(t,sourceDir, "shared.txt", "source line\n", "source change")

	// Record the source HEAD before rebase to verify rollback
	headBefore, err := RunGit([]string{"rev-parse", "HEAD"}, sourceDir)
	if err != nil {
		t.Fatalf("get HEAD before rebase: %v", err)
	}

	result, err := RebaseBranch(sourceDir, targetDir)
	if err != nil {
		t.Fatalf("RebaseBranch returned error: %v", err)
	}

	if result.Success {
		t.Fatal("expected rebase to fail with conflicts")
	}

	// Verify conflicts
	if len(result.Conflicts) == 0 {
		t.Error("expected at least one conflict")
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

	if result.GitStderr == "" {
		t.Error("GitStderr should be non-empty on conflict")
	}

	// Verify the rebase was aborted - source worktree should be clean
	clean, err := IsClean(sourceDir)
	if err != nil {
		t.Fatalf("check clean: %v", err)
	}
	if !clean {
		t.Error("source worktree should be clean after rebase abort")
	}

	// Verify HEAD is unchanged (rebase was aborted)
	headAfter, err := RunGit([]string{"rev-parse", "HEAD"}, sourceDir)
	if err != nil {
		t.Fatalf("get HEAD after rebase: %v", err)
	}
	if headAfter != headBefore {
		t.Errorf("HEAD should be unchanged after aborted rebase: before=%s, after=%s", headBefore, headAfter)
	}
}

func TestFindWorktreeByBranch_Found(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create a worktree with a known branch
	wtDir := filepath.Join(dir, "my-worktree")
	testutil.AddWorktree(t,bareRepo, "feature/test-find", wtDir)

	mgr := NewManager(bareRepo, "main")
	path, err := mgr.FindWorktreeByBranch("feature/test-find")
	if err != nil {
		t.Fatalf("FindWorktreeByBranch returned error: %v", err)
	}

	if path != wtDir {
		t.Errorf("path = %q, want %q", path, wtDir)
	}
}

func TestFindWorktreeByBranch_NotFound(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)

	mgr := NewManager(bareRepo, "main")
	_, err := mgr.FindWorktreeByBranch("nonexistent-branch")
	if err == nil {
		t.Fatal("expected error for nonexistent branch")
	}
	if !strings.Contains(err.Error(), "no worktree found for branch") {
		t.Errorf("error should mention 'no worktree found for branch', got: %v", err)
	}
}

func TestMainWorktreePath_BareRepoIsMainWorktree(t *testing.T) {
	// Create a non-bare repo where the root itself is the working tree.
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	RunGit([]string{"init", repoDir}, "")
	RunGit([]string{"config", "user.email", "test@test.com"}, repoDir)
	RunGit([]string{"config", "user.name", "Test"}, repoDir)

	// Create an initial commit so the default branch exists.
	initFile := filepath.Join(repoDir, "README.md")
	os.WriteFile(initFile, []byte("# Test\n"), 0o644)
	RunGit([]string{"add", "."}, repoDir)
	RunGit([]string{"commit", "-m", "initial commit"}, repoDir)

	// Detect the default branch.
	branch, err := RunGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, repoDir)
	if err != nil {
		t.Fatalf("detect default branch: %v", err)
	}

	mgr := NewManager(repoDir, branch)
	got, err := mgr.MainWorktreePath()
	if err != nil {
		t.Fatalf("MainWorktreePath() returned error: %v", err)
	}
	if got != repoDir {
		t.Errorf("MainWorktreePath() = %q, want %q", got, repoDir)
	}
}

func TestMainWorktreePath_LinkedWorktree(t *testing.T) {
	bareRepo := testutil.InitBareRepoWithMainWorktree(t)
	defaultBranch := testutil.GetDefaultBranch(t, bareRepo)

	mgr := NewManager(bareRepo, defaultBranch)
	got, err := mgr.MainWorktreePath()
	if err != nil {
		t.Fatalf("MainWorktreePath() returned error: %v", err)
	}

	want := filepath.Join(bareRepo, defaultBranch)
	if got != want {
		t.Errorf("MainWorktreePath() = %q, want %q", got, want)
	}
}

func TestMainWorktreePath_NotFound(t *testing.T) {
	bareRepo := testutil.SetupBareRepo(t)

	mgr := NewManager(bareRepo, "nonexistent-branch")
	_, err := mgr.MainWorktreePath()
	if err == nil {
		t.Fatal("expected error for nonexistent branch")
	}
	if !strings.Contains(err.Error(), "no worktree found for branch") {
		t.Errorf("error should mention 'no worktree found for branch', got: %v", err)
	}
}

func TestParseRebaseConflicts(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		expected []string
	}{
		{
			name:     "single conflict",
			stderr:   "CONFLICT (content): Merge conflict in src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name: "multiple conflicts",
			stderr: `Auto-merging file1.go
CONFLICT (content): Merge conflict in file1.go
Auto-merging file2.go
CONFLICT (content): Merge conflict in file2.go`,
			expected: []string{"file1.go", "file2.go"},
		},
		{
			name:     "no conflicts",
			stderr:   "Successfully rebased and updated refs/heads/branch.",
			expected: nil,
		},
		{
			name:     "empty stderr",
			stderr:   "",
			expected: nil,
		},
		{
			name:     "add/add conflict",
			stderr:   "CONFLICT (add/add): Merge conflict in new-file.txt",
			expected: []string{"new-file.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRebaseConflicts(tt.stderr)
			if len(got) != len(tt.expected) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("conflict[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}
