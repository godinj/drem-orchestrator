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

// TestEnsureBranch_CreatesWhenMissing is the happy-path primitive: a
// branch that does not exist gets created at the tip of the source
// branch, and its HeadCommit matches the source's HeadCommit.
func TestEnsureBranch_CreatesWhenMissing(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	defaultBranch, err := gitref.DefaultBranch(ctx, bare)
	require.NoError(t, err)

	// Precondition: target branch does not yet exist.
	exists, err := gitref.BranchExists(ctx, bare, "feature/ensured")
	require.NoError(t, err)
	require.False(t, exists)

	require.NoError(t, gitref.EnsureBranch(ctx, bare, "feature/ensured", defaultBranch))

	// Branch exists and points at the same commit as the source.
	exists, err = gitref.BranchExists(ctx, bare, "feature/ensured")
	require.NoError(t, err)
	require.True(t, exists)

	srcSHA, err := gitref.HeadCommit(ctx, bare, defaultBranch)
	require.NoError(t, err)
	dstSHA, err := gitref.HeadCommit(ctx, bare, "feature/ensured")
	require.NoError(t, err)
	require.Equal(t, srcSHA, dstSHA,
		"freshly-ensured branch must point at the source tip")
}

// TestEnsureBranch_IdempotentWhenPresent is the anti-clobber guarantee:
// an EnsureBranch call against a branch that already exists (and has
// advanced beyond the source) must NOT move the tip back. This is the
// exact guarantee that makes the primitive safe to call from the
// container respawn loop, where a worker may already have pushed work.
func TestEnsureBranch_IdempotentWhenPresent(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	defaultBranch, err := gitref.DefaultBranch(ctx, bare)
	require.NoError(t, err)

	// Create feature/live with an extra commit on top of the default
	// branch, simulating in-flight worker work.
	pushBranch(t, bare, "feature/live", "worker-commit.txt", "worker was here\n")

	// Capture the tip before calling EnsureBranch.
	tipBefore, err := gitref.HeadCommit(ctx, bare, "feature/live")
	require.NoError(t, err)

	// Sanity check: the tip should be AHEAD of the default branch.
	defaultSHA, err := gitref.HeadCommit(ctx, bare, defaultBranch)
	require.NoError(t, err)
	require.NotEqual(t, defaultSHA, tipBefore,
		"test setup invariant: feature/live must be ahead of default")

	// EnsureBranch is expected to no-op.
	require.NoError(t, gitref.EnsureBranch(ctx, bare, "feature/live", defaultBranch))

	tipAfter, err := gitref.HeadCommit(ctx, bare, "feature/live")
	require.NoError(t, err)
	require.Equal(t, tipBefore, tipAfter,
		"EnsureBranch must NOT rewind an existing branch to its source tip")
}

// TestEnsureBranch_ErrorsOnMissingSource asserts fork-from-ghost is
// rejected. Without this, a typo in fromBranch would create an empty
// branch or produce a less-specific git error.
func TestEnsureBranch_ErrorsOnMissingSource(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	err := gitref.EnsureBranch(ctx, bare, "feature/orphan", "no-such-source")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no-such-source",
		"error message must identify the missing source branch")

	// Target branch must NOT have been created as a side effect.
	exists, existsErr := gitref.BranchExists(ctx, bare, "feature/orphan")
	require.NoError(t, existsErr)
	require.False(t, exists, "failed EnsureBranch must not leave a partial branch")
}

// TestEnsureBranch_RejectsEmptyArgs documents the input-validation
// boundary: every arg is mandatory and every zero value is an error.
func TestEnsureBranch_RejectsEmptyArgs(t *testing.T) {
	ctx := context.Background()
	bare := testutil.SetupBareRepo(t)

	require.Error(t, gitref.EnsureBranch(ctx, "", "feature/x", "main"))
	require.Error(t, gitref.EnsureBranch(ctx, bare, "", "main"))
	require.Error(t, gitref.EnsureBranch(ctx, bare, "feature/x", ""))
}
