package gitref_test

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// sha40 matches exactly 40 lowercase hex characters — the format git
// returns for full commit SHAs.
var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// pushBranch creates a new branch in a bare repo by adding a worktree for
// it (which registers the branch in the bare repo's ref database) and
// committing a file through testutil.CommitFile. Because `git worktree add`
// shares the bare repo's object database, the commit automatically
// advances refs/heads/<branch> — no explicit push is needed.
func pushBranch(t *testing.T, bareRepo, branch, filename, content string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	testutil.AddWorktree(t, bareRepo, branch, work)
	testutil.CommitFile(t, work, filename, content, "add "+filename)
}

func TestBranchExists_TrueWhenPushed(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)
	pushBranch(t, bare, "feature/exists", "hello.txt", "hello\n")

	ok, err := gitref.BranchExists(ctx, bare, "feature/exists")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestBranchExists_FalseForUnknownBranch(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	ok, err := gitref.BranchExists(ctx, bare, "feature/does-not-exist")
	require.NoError(t, err, "missing branch must not surface as an error")
	require.False(t, ok)
}

func TestBranchExists_ErrorsOnMissingRepo(t *testing.T) {
	ctx := context.Background()

	ok, err := gitref.BranchExists(ctx, "/definitely/not/a/repo.git", "main")
	require.Error(t, err, "missing bare repo should be an error, not a false result")
	require.False(t, ok)
}

func TestBranchExists_RejectsEmptyArgs(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	_, err := gitref.BranchExists(ctx, "", "main")
	require.Error(t, err)

	_, err = gitref.BranchExists(ctx, bare, "")
	require.Error(t, err)
}

func TestHeadCommit_ReturnsSHA(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)
	pushBranch(t, bare, "feature/head", "payload.txt", "payload\n")

	sha, err := gitref.HeadCommit(ctx, bare, "feature/head")
	require.NoError(t, err)
	require.True(t, sha40.MatchString(sha),
		"HeadCommit must return a 40-character hex SHA, got %q", sha)
}

func TestHeadCommit_UnknownBranchErrors(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	_, err := gitref.HeadCommit(ctx, bare, "feature/missing")
	require.Error(t, err)
}

func TestDefaultBranch_ReturnsMainOrMaster(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	branch, err := gitref.DefaultBranch(ctx, bare)
	require.NoError(t, err)
	require.Contains(t, []string{"main", "master"}, branch,
		"default branch should be 'main' or 'master', got %q", branch)

	// Cross-check: the default branch must also pass BranchExists.
	exists, err := gitref.BranchExists(ctx, bare, branch)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestDefaultBranch_RejectsEmptyRepo(t *testing.T) {
	ctx := context.Background()

	_, err := gitref.DefaultBranch(ctx, "")
	require.Error(t, err)
}
