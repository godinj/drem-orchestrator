package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupWorktree creates a bare repo + clone for gitexec tests. Returns the
// worktree path where commands can be executed.
func setupWorktree(t *testing.T) (string, string) {
	t.Helper()
	bare := testutil.SetupBareRepo(t)
	clone := filepath.Join(t.TempDir(), "work")
	_, err := testutil.RunGit([]string{"clone", bare, clone}, "")
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"config", "user.email", "test@test.com"}, clone)
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"config", "user.name", "Test"}, clone)
	require.NoError(t, err)
	return bare, clone
}

func TestRunGit_Success(t *testing.T) {
	_, wt := setupWorktree(t)
	out, err := RunGit(context.Background(), wt, "rev-parse", "--is-inside-work-tree")
	require.NoError(t, err)
	require.Equal(t, "true", out)
}

func TestRunGit_Failure(t *testing.T) {
	_, wt := setupWorktree(t)
	_, err := RunGit(context.Background(), wt, "show", "nonexistent-ref")
	require.Error(t, err)
	var gErr *GitError
	require.ErrorAs(t, err, &gErr)
	require.NotZero(t, gErr.ReturnCode)
}

func TestGetChangedFiles(t *testing.T) {
	_, wt := setupWorktree(t)
	base, err := RunGit(context.Background(), wt, "rev-parse", "HEAD")
	require.NoError(t, err)

	testutil.CommitFile(t, wt, "a.txt", "hello", "add a.txt")
	testutil.CommitFile(t, wt, "b.txt", "world", "add b.txt")

	files, err := GetChangedFiles(context.Background(), wt, base)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.txt", "b.txt"}, files)
}

func TestGetChangedFiles_Empty(t *testing.T) {
	_, wt := setupWorktree(t)
	head, err := RunGit(context.Background(), wt, "rev-parse", "HEAD")
	require.NoError(t, err)
	files, err := GetChangedFiles(context.Background(), wt, head)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestIsClean(t *testing.T) {
	_, wt := setupWorktree(t)
	clean, err := IsClean(context.Background(), wt)
	require.NoError(t, err)
	require.True(t, clean)

	// Write an untracked file — still dirty.
	require.NoError(t, os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x"), 0o644))
	clean, err = IsClean(context.Background(), wt)
	require.NoError(t, err)
	require.False(t, clean)
}

func TestIsClean_IgnoresClaude(t *testing.T) {
	_, wt := setupWorktree(t)
	claudeDir := filepath.Join(wt, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{}"), 0o644))

	clean, err := IsClean(context.Background(), wt)
	require.NoError(t, err)
	require.True(t, clean, "untracked .claude/ entries should not mark as dirty")
}

func TestCommitUnstagedChanges_Nothing(t *testing.T) {
	_, wt := setupWorktree(t)
	committed, err := CommitUnstagedChanges(context.Background(), wt, "noop")
	require.NoError(t, err)
	require.False(t, committed)
}

func TestCommitUnstagedChanges_WithChanges(t *testing.T) {
	_, wt := setupWorktree(t)
	// Create a tracked file, commit it, then modify.
	testutil.CommitFile(t, wt, "tracked.txt", "v1", "init")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("v2"), 0o644))

	committed, err := CommitUnstagedChanges(context.Background(), wt, "auto-commit")
	require.NoError(t, err)
	require.True(t, committed)

	// Verify a new commit exists.
	msg, err := RunGit(context.Background(), wt, "log", "-1", "--format=%s")
	require.NoError(t, err)
	require.Equal(t, "auto-commit", msg)
}

func TestUntrackEphemeralFiles_NoFiles(t *testing.T) {
	_, wt := setupWorktree(t)
	committed, err := UntrackEphemeralFiles(context.Background(), wt)
	require.NoError(t, err)
	require.False(t, committed)
}

func TestUntrackEphemeralFiles_TrackedPlan(t *testing.T) {
	_, wt := setupWorktree(t)
	// Track a plan.json file.
	testutil.CommitFile(t, wt, "plan.json", `{"subtasks":[]}`, "add plan")

	committed, err := UntrackEphemeralFiles(context.Background(), wt)
	require.NoError(t, err)
	require.True(t, committed)

	// plan.json should still exist on disk.
	_, statErr := os.Stat(filepath.Join(wt, "plan.json"))
	require.NoError(t, statErr)

	// But should no longer be tracked.
	out, _ := RunGit(context.Background(), wt, "ls-files", "plan.json")
	require.Empty(t, out)
}

func TestBranchHasNewCommits(t *testing.T) {
	bare, wt := setupWorktree(t)

	// Create a branch with a new commit.
	_, err := RunGit(context.Background(), wt, "checkout", "-b", "feature")
	require.NoError(t, err)
	testutil.CommitFile(t, wt, "new.txt", "x", "new commit")

	// Push so bare repo sees the branch.
	_, err = RunGit(context.Background(), wt, "push", "origin", "feature")
	require.NoError(t, err)

	// From main's perspective, feature has new commits.
	_, err = RunGit(context.Background(), wt, "checkout", "-")
	require.NoError(t, err)

	has, err := BranchHasNewCommits(context.Background(), wt, "feature")
	require.NoError(t, err)
	require.True(t, has)

	// From feature's perspective, HEAD..feature is empty.
	_, err = RunGit(context.Background(), wt, "checkout", "feature")
	require.NoError(t, err)
	has, err = BranchHasNewCommits(context.Background(), wt, "feature")
	require.NoError(t, err)
	require.False(t, has)

	_ = bare
}

func TestCommitInfo(t *testing.T) {
	_, wt := setupWorktree(t)
	testutil.CommitFile(t, wt, "info.txt", "body", "feat: add info")

	sha, err := RunGit(context.Background(), wt, "rev-parse", "HEAD")
	require.NoError(t, err)

	c, err := CommitInfo(context.Background(), wt, sha)
	require.NoError(t, err)
	require.Equal(t, sha, c.SHA)
	require.Equal(t, "feat: add info", c.Subject)
	require.Equal(t, "Test", c.Author)
	require.False(t, c.Committed.IsZero())
}
