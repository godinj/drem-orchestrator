package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupBareRepo creates a bare git repo with an initial commit in a temp dir.
// Returns the bare repo path and a cleanup function.
func setupBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bareRepo := filepath.Join(dir, "test.git")

	// Init bare repo
	if _, err := RunGit([]string{"init", "--bare", bareRepo}, ""); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}

	// Create a temporary clone to make an initial commit
	cloneDir := filepath.Join(dir, "clone")
	if _, err := RunGit([]string{"clone", bareRepo, cloneDir}, ""); err != nil {
		t.Fatalf("clone bare repo: %v", err)
	}

	// Configure git user for commits
	RunGit([]string{"config", "user.email", "test@test.com"}, cloneDir)
	RunGit([]string{"config", "user.name", "Test"}, cloneDir)

	// Create initial commit
	initFile := filepath.Join(cloneDir, "README.md")
	if err := os.WriteFile(initFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write init file: %v", err)
	}
	RunGit([]string{"add", "."}, cloneDir)
	if _, err := RunGit([]string{"commit", "-m", "initial commit"}, cloneDir); err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	// Push to bare repo
	if _, err := RunGit([]string{"push", "origin", "HEAD"}, cloneDir); err != nil {
		t.Fatalf("push initial commit: %v", err)
	}

	return bareRepo
}

// addWorktree creates a worktree from the bare repo with a new branch.
// Returns the worktree path.
func addWorktree(t *testing.T, bareRepo, branch, dir string) string {
	t.Helper()
	if _, err := RunGit([]string{"worktree", "add", "-b", branch, dir}, bareRepo); err != nil {
		t.Fatalf("add worktree %s: %v", branch, err)
	}
	// Configure git user in the worktree
	RunGit([]string{"config", "user.email", "test@test.com"}, dir)
	RunGit([]string{"config", "user.name", "Test"}, dir)
	return dir
}

// commitFile creates or overwrites a file and commits it in the given worktree.
func commitFile(t *testing.T, worktree, filename, content, message string) {
	t.Helper()
	fpath := filepath.Join(worktree, filename)
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", filename, err)
	}
	if _, err := RunGit([]string{"add", filename}, worktree); err != nil {
		t.Fatalf("git add %s: %v", filename, err)
	}
	if _, err := RunGit([]string{"commit", "-m", message}, worktree); err != nil {
		t.Fatalf("commit %s: %v", message, err)
	}
}

func TestMergeBranch_EnrichedResult_OnConflict(t *testing.T) {
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree (simulates integration branch)
	targetDir := filepath.Join(dir, "target")
	addWorktree(t, bareRepo, "feature/target", targetDir)

	// Create source worktree (simulates agent branch)
	sourceDir := filepath.Join(dir, "source")
	addWorktree(t, bareRepo, "agent-source", sourceDir)

	// Make conflicting changes to the same file in both worktrees
	commitFile(t, targetDir, "conflict.txt", "target content\n", "target change")
	commitFile(t, sourceDir, "conflict.txt", "source content\n", "source change")

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
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	addWorktree(t, bareRepo, "feature/target", targetDir)

	// Create source worktree with non-overlapping changes
	sourceDir := filepath.Join(dir, "source")
	addWorktree(t, bareRepo, "agent-source", sourceDir)

	commitFile(t, sourceDir, "new-file.txt", "new content\n", "add new file")

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
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	addWorktree(t, bareRepo, "feature/target", targetDir)

	// Create source worktree with a commit
	sourceDir := filepath.Join(dir, "source")
	addWorktree(t, bareRepo, "agent-source", sourceDir)
	commitFile(t, sourceDir, "file.txt", "content\n", "add file")

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
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	addWorktree(t, bareRepo, "feature/target", targetDir)

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
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree (simulates integration branch)
	targetDir := filepath.Join(dir, "target")
	addWorktree(t, bareRepo, "feature/target", targetDir)

	// Create source worktree (simulates agent branch)
	sourceDir := filepath.Join(dir, "source")
	addWorktree(t, bareRepo, "agent-source", sourceDir)

	// Make non-overlapping changes
	commitFile(t, targetDir, "target-file.txt", "target content\n", "target change")
	commitFile(t, sourceDir, "source-file.txt", "source content\n", "source change")

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
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create target worktree
	targetDir := filepath.Join(dir, "target")
	addWorktree(t, bareRepo, "feature/target", targetDir)

	// Create source worktree
	sourceDir := filepath.Join(dir, "source")
	addWorktree(t, bareRepo, "agent-source", sourceDir)

	// Make conflicting changes to the same file
	commitFile(t, targetDir, "shared.txt", "target line\n", "target change")
	commitFile(t, sourceDir, "shared.txt", "source line\n", "source change")

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
	bareRepo := setupBareRepo(t)
	dir := filepath.Dir(bareRepo)

	// Create a worktree with a known branch
	wtDir := filepath.Join(dir, "my-worktree")
	addWorktree(t, bareRepo, "feature/test-find", wtDir)

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
	bareRepo := setupBareRepo(t)

	mgr := NewManager(bareRepo, "main")
	_, err := mgr.FindWorktreeByBranch("nonexistent-branch")
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
