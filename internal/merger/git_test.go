package merger

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/stretchr/testify/require"
)

// These tests exercise the narrow git wrappers directly, against a real git
// binary and temp directories. They are deliberately white-box (same
// package) so we can call unexported helpers without exposing them.

func TestCloneBranch_FetchesRequestedBranch(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	workDir := filepath.Join(t.TempDir(), "clone")

	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bare)
	require.NoError(t, err)

	require.NoError(t, cloneBranch(context.Background(), bare, integration, workDir))
	require.FileExists(t, filepath.Join(workDir, "README.md"))
}

func TestFetchBranch_MakesRemoteBranchLocallyReachable(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bare)
	require.NoError(t, err)

	// Create a feature branch on the bare repo via a prep clone.
	prep := filepath.Join(t.TempDir(), "prep")
	_, err = testutil.RunGit([]string{"clone", bare, prep}, "")
	require.NoError(t, err)
	_, _ = testutil.RunGit([]string{"config", "user.email", "t@t.com"}, prep)
	_, _ = testutil.RunGit([]string{"config", "user.name", "T"}, prep)
	_, err = testutil.RunGit([]string{"checkout", "-b", "feature/x"}, prep)
	require.NoError(t, err)
	testutil.CommitFile(t, prep, "x.txt", "x\n", "add x")
	_, err = testutil.RunGit([]string{"push", "origin", "feature/x"}, prep)
	require.NoError(t, err)

	// Clone integration separately and then fetch the feature branch into
	// it as a local ref.
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, cloneBranch(context.Background(), bare, integration, work))
	require.NoError(t, fetchBranch(context.Background(), work, "feature/x"))

	_, gitErr := testutil.RunGit(
		[]string{"show-ref", "--verify", "refs/heads/feature/x"}, work)
	require.NoError(t, gitErr, "feature branch should be a local ref after fetch")
}

func TestConflictedFiles_DetectsPorcelainCodes(t *testing.T) {
	// Set up a tiny repo with a real conflict to parse.
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	_, err := testutil.RunGit([]string{"init"}, work)
	require.NoError(t, err)
	_, _ = testutil.RunGit([]string{"config", "user.email", "t@t.com"}, work)
	_, _ = testutil.RunGit([]string{"config", "user.name", "T"}, work)

	// Base commit.
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("base\n"), 0o644))
	_, err = testutil.RunGit([]string{"add", "a.txt"}, work)
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"commit", "-m", "base"}, work)
	require.NoError(t, err)

	// Branch A.
	_, err = testutil.RunGit([]string{"checkout", "-b", "a"}, work)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("a-line\n"), 0o644))
	_, err = testutil.RunGit([]string{"commit", "-am", "a"}, work)
	require.NoError(t, err)

	// Branch B from base, then try to merge A → conflict.
	_, err = testutil.RunGit([]string{"checkout", "-b", "b", "HEAD~1"}, work)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("b-line\n"), 0o644))
	_, err = testutil.RunGit([]string{"commit", "-am", "b"}, work)
	require.NoError(t, err)

	// Attempt merge.
	out := mergeBranch(context.Background(), work, "a", "merge a")
	require.Error(t, out.Err, "expected merge conflict")

	conflicts, err := conflictedFiles(context.Background(), work)
	require.NoError(t, err)
	require.Contains(t, conflicts, "a.txt")

	// Cleanly abort the merge for hygiene (not load-bearing, but verifies
	// the wrapper works).
	require.NoError(t, abortMerge(context.Background(), work))
}

func TestPushDelete_RemovesRemoteBranch(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bare)
	require.NoError(t, err)

	// Create a feature branch on origin.
	prep := filepath.Join(t.TempDir(), "prep")
	_, err = testutil.RunGit([]string{"clone", bare, prep}, "")
	require.NoError(t, err)
	_, _ = testutil.RunGit([]string{"config", "user.email", "t@t.com"}, prep)
	_, _ = testutil.RunGit([]string{"config", "user.name", "T"}, prep)
	_, err = testutil.RunGit([]string{"checkout", "-b", "feature/y"}, prep)
	require.NoError(t, err)
	testutil.CommitFile(t, prep, "y.txt", "y\n", "add y")
	_, err = testutil.RunGit([]string{"push", "origin", "feature/y"}, prep)
	require.NoError(t, err)

	// Clone fresh and push-delete.
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, cloneBranch(context.Background(), bare, integration, work))
	exists, err := remoteBranchExists(context.Background(), work, "feature/y")
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, pushDelete(context.Background(), work, "feature/y"))

	exists, err = remoteBranchExists(context.Background(), work, "feature/y")
	require.NoError(t, err)
	require.False(t, exists, "branch should be gone after push --delete")
}

// TestResetWorkDir_PreservesDirectoryInode covers Bug J: the merger
// container bind-mounts the feature worktree onto workDir directly, so
// workDir IS the mount point. The pre-Bug-J resetWorkDir called
// os.RemoveAll(workDir), which tried to unlinkat the mount point and
// failed with EBUSY. We verify here that resetWorkDir now clears
// workDir's CONTENTS while leaving the directory itself intact — the
// inode must survive so any outer bind mount is still valid.
func TestResetWorkDir_PreservesDirectoryInode(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	// Populate a mix of files and subdirectories, so we can prove the
	// full-tree clear semantic.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("a\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "sub", "deeper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "sub", "b.txt"), []byte("b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".git-like"), []byte("c\n"), 0o644))

	infoBefore, err := os.Stat(workDir)
	require.NoError(t, err)

	require.NoError(t, resetWorkDir(workDir))

	infoAfter, err := os.Stat(workDir)
	require.NoError(t, err, "workDir must still exist after reset")
	require.True(t, os.SameFile(infoBefore, infoAfter),
		"resetWorkDir must preserve the directory inode (Bug J: unlinking a mount point errors EBUSY)")

	entries, err := os.ReadDir(workDir)
	require.NoError(t, err)
	require.Empty(t, entries, "resetWorkDir must drop every child including dotfiles and subdirs")
}

// TestResetWorkDir_CreatesMissingDirectory covers the host-filesystem
// legacy path where workDir does not yet exist. resetWorkDir must
// materialize it (or its parent path) so cloneBranch has somewhere to
// land.
func TestResetWorkDir_CreatesMissingDirectory(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "deep", "nested", "work")

	require.NoError(t, resetWorkDir(workDir))

	info, err := os.Stat(workDir)
	require.NoError(t, err, "resetWorkDir must create workDir when it is missing")
	require.True(t, info.IsDir())
}

// TestCloneBranch_IntoExistingEmptyDir covers Bug J's clone-into-. shape
// change: cloneBranch must accept a pre-existing empty workDir (because
// resetWorkDir leaves the mount point intact) and populate it.
func TestCloneBranch_IntoExistingEmptyDir(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bare)
	require.NoError(t, err)

	// Simulate the containerized shape: workDir pre-exists (bind mount).
	workDir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	require.NoError(t, cloneBranch(context.Background(), bare, integration, workDir))
	require.FileExists(t, filepath.Join(workDir, "README.md"))
	require.DirExists(t, filepath.Join(workDir, ".git"))
}

func TestHeadSHA_MatchesRevParse(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	integration, err := testutil.RunGit(
		[]string{"symbolic-ref", "--short", "HEAD"}, bare)
	require.NoError(t, err)

	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, cloneBranch(context.Background(), bare, integration, work))

	got, err := headSHA(context.Background(), work)
	require.NoError(t, err)
	want, err := testutil.RunGit([]string{"rev-parse", "HEAD"}, work)
	require.NoError(t, err)
	require.Equal(t, want, got)
}
