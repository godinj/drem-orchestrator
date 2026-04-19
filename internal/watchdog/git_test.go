package watchdog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestIsDirtyCleanThenDirty(t *testing.T) {
	clone, _ := setupRepoClone(t, "feat-isdirty")

	dirty, err := isDirty(context.Background(), clone)
	require.NoError(t, err)
	require.False(t, dirty, "freshly cloned tree must be clean")

	require.NoError(t, os.WriteFile(filepath.Join(clone, "new.txt"), []byte("x"), 0o644))

	dirty, err = isDirty(context.Background(), clone)
	require.NoError(t, err)
	require.True(t, dirty, "untracked file must register as dirty")
}

func TestCommitAllCreatesCommitForUntrackedFile(t *testing.T) {
	clone, _ := setupRepoClone(t, "feat-commitall")

	require.NoError(t, os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a"), 0o644))

	committed, err := commitAll(context.Background(), clone, "[watchdog] wip test")
	require.NoError(t, err)
	require.True(t, committed, "an untracked file must produce a commit")

	dirty, err := isDirty(context.Background(), clone)
	require.NoError(t, err)
	require.False(t, dirty, "tree must be clean after commitAll")
}

func TestCommitAllNoopOnCleanTree(t *testing.T) {
	clone, _ := setupRepoClone(t, "feat-commitnoop")

	committed, err := commitAll(context.Background(), clone, "[watchdog] wip test")
	require.NoError(t, err)
	require.False(t, committed, "commitAll on clean tree must not create a commit")
}

func TestPushBranchUpdatesBareRepo(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-push")

	// Commit a local change.
	require.NoError(t, os.WriteFile(filepath.Join(clone, "p.txt"), []byte("p"), 0o644))
	committed, err := commitAll(context.Background(), clone, "[watchdog] wip push")
	require.NoError(t, err)
	require.True(t, committed)

	// Push via the helper and verify the bare repo's ref moved.
	require.NoError(t, pushBranch(context.Background(), clone, "origin", "feat-push"))

	bareSHA, err := testutil.RunGit([]string{"rev-parse", "refs/heads/feat-push"}, bare)
	require.NoError(t, err)
	localSHA, err := testutil.RunGit([]string{"rev-parse", "HEAD"}, clone)
	require.NoError(t, err)
	require.Equal(t, localSHA, bareSHA, "bare repo ref must match local HEAD after push")
}

func TestPushBranchFailsOnUnknownRemote(t *testing.T) {
	clone, _ := setupRepoClone(t, "feat-pushfail")

	err := pushBranch(context.Background(), clone, "nonexistent-remote", "feat-pushfail")
	require.Error(t, err, "pushing to an unknown remote must return an error")
}
