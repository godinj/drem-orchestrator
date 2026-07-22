package watchdog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupRepoClone creates a bare repo via testutil.SetupBareRepo and
// returns a fresh clone of it plus the bare-repo path. The clone plays
// the role of the container-local working copy against which the
// watchdog operates; the bare repo is the remote push target.
func setupRepoClone(t *testing.T, branch string) (clone, bare string) {
	t.Helper()
	bare = testutil.SetupBareRepo(t)
	clone = filepath.Join(t.TempDir(), "clone")
	_, err := testutil.RunGit([]string{"clone", bare, clone}, "")
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"config", "user.email", "wd@test.com"}, clone)
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"config", "user.name", "Watchdog Test"}, clone)
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"checkout", "-b", branch}, clone)
	require.NoError(t, err)
	// Push the branch so the remote has a matching ref and subsequent
	// fast-forward pushes succeed without --set-upstream.
	_, err = testutil.RunGit([]string{"push", "-u", "origin", branch}, clone)
	require.NoError(t, err)
	return clone, bare
}

// newLoop builds a Loop pointed at `clone` with a frozen clock. Tests
// that want to observe heartbeat output pass a non-nil buffer.
func newLoop(clone, branch string, buf *bytes.Buffer) *Loop {
	l := &Loop{
		Repo:         clone,
		Branch:       branch,
		Remote:       "origin",
		Interval:     time.Second,
		TestInterval: time.Second,
		AgentID:      "agent-123",
		Clock:        func() time.Time { return time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC) },
	}
	if buf != nil {
		l.Out = buf
	}
	return l
}

// branchHead returns the SHA of the named branch in the bare repo. Used
// to assert that a push actually advanced the ref.
func branchHead(t *testing.T, bare, branch string) string {
	t.Helper()
	sha, err := testutil.RunGit([]string{"rev-parse", "refs/heads/" + branch}, bare)
	require.NoError(t, err)
	return sha
}

// countBranchCommits returns the number of commits on the named branch
// in the bare repo.
func countBranchCommits(t *testing.T, bare, branch string) int {
	t.Helper()
	out, err := testutil.RunGit([]string{"rev-list", "--count", "refs/heads/" + branch}, bare)
	require.NoError(t, err)
	n := 0
	_, err = parseInt(out, &n)
	require.NoError(t, err)
	return n
}

// parseInt is a minimal stdlib-free integer parser so that the test file
// does not pull in strconv purely for one conversion.
func parseInt(s string, out *int) (int, error) {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0, &invalidIntError{s: s}
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return n, nil
}

type invalidIntError struct{ s string }

func (e *invalidIntError) Error() string { return "invalid int: " + e.s }

func TestTickCommitCleanTreeIsNoOp(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-clean")
	l := newLoop(clone, "feat-clean", nil)

	before := branchHead(t, bare, "feat-clean")
	beforeCount := countBranchCommits(t, bare, "feat-clean")

	require.NoError(t, l.tickCommit(context.Background()))

	require.Equal(t, before, branchHead(t, bare, "feat-clean"), "HEAD must not move on a clean tree")
	require.Equal(t, beforeCount, countBranchCommits(t, bare, "feat-clean"))
}

func TestTickCommitDirtyTreeCommitsAndPushesOnce(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-dirty")
	l := newLoop(clone, "feat-dirty", nil)

	// Dirty the tree with a new file.
	require.NoError(t, os.WriteFile(filepath.Join(clone, "work.txt"), []byte("wip"), 0o644))

	beforeCount := countBranchCommits(t, bare, "feat-dirty")
	require.NoError(t, l.tickCommit(context.Background()))

	afterCount := countBranchCommits(t, bare, "feat-dirty")
	require.Equal(t, beforeCount+1, afterCount, "exactly one new commit must be pushed")

	// Inspect the commit subject on the bare repo.
	subject, err := testutil.RunGit([]string{"log", "-1", "--format=%s", "refs/heads/feat-dirty"}, bare)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(subject, "[watchdog] wip "),
		"subject %q must start with the wip prefix", subject)

	// A follow-up tick with no new changes is a no-op.
	require.NoError(t, l.tickCommit(context.Background()))
	require.Equal(t, afterCount, countBranchCommits(t, bare, "feat-dirty"),
		"follow-up clean tick must not create another commit")
}

func TestFinalFlushPushesHarnessCommittedCleanTree(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-harness-commit")
	l := newLoop(clone, "feat-harness-commit", nil)

	require.NoError(t, os.WriteFile(filepath.Join(clone, "completed.txt"), []byte("done"), 0o644))
	_, err := testutil.RunGit([]string{"add", "completed.txt"}, clone)
	require.NoError(t, err)
	_, err = testutil.RunGit([]string{"commit", "-m", "harness committed work"}, clone)
	require.NoError(t, err)

	localHead, err := testutil.RunGit([]string{"rev-parse", "HEAD"}, clone)
	require.NoError(t, err)
	require.NotEqual(t, localHead, branchHead(t, bare, "feat-harness-commit"), "fixture must start ahead of the bare ref")

	l.finalFlush()
	require.Equal(t, localHead, branchHead(t, bare, "feat-harness-commit"), "final flush must push an already-committed clean tree")
}

func TestTickTestPassingForcesCommitAndPush(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-testpass")
	l := newLoop(clone, "feat-testpass", nil)
	l.TestCmd = "true" // exits 0

	// Dirty the tree so that the forced commit has content.
	require.NoError(t, os.WriteFile(filepath.Join(clone, "pass.txt"), []byte("ok"), 0o644))

	// First tickCommit pushes the wip commit.
	require.NoError(t, l.tickCommit(context.Background()))
	wipCount := countBranchCommits(t, bare, "feat-testpass")

	// A passing test tick must still commit and push even though the
	// tree is now clean — but because the tree is clean after the wip
	// push, commitAll's `diff --cached --quiet` returns success and no
	// new commit is created. The test-pass contract is "force a push"
	// rather than "force an empty commit"; the current HEAD is what
	// gets pushed, and the ref simply stays at its current SHA.
	require.NoError(t, l.tickTest(context.Background()))
	require.Equal(t, wipCount, countBranchCommits(t, bare, "feat-testpass"))

	// Now dirty the tree again and run the test tick — it must commit
	// with the tests-passing subject prefix.
	require.NoError(t, os.WriteFile(filepath.Join(clone, "pass2.txt"), []byte("ok2"), 0o644))
	require.NoError(t, l.tickTest(context.Background()))
	afterCount := countBranchCommits(t, bare, "feat-testpass")
	require.Equal(t, wipCount+1, afterCount)

	subject, err := testutil.RunGit([]string{"log", "-1", "--format=%s", "refs/heads/feat-testpass"}, bare)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(subject, "[watchdog] tests-passing "),
		"subject %q must start with the tests-passing prefix", subject)
}

func TestTickTestFailingDoesNotCommit(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-testfail")
	l := newLoop(clone, "feat-testfail", nil)
	l.TestCmd = "false" // exits 1

	// Dirty the tree — a failing test must not capture this.
	require.NoError(t, os.WriteFile(filepath.Join(clone, "broken.txt"), []byte("oops"), 0o644))

	before := countBranchCommits(t, bare, "feat-testfail")
	require.NoError(t, l.tickTest(context.Background()))
	require.Equal(t, before, countBranchCommits(t, bare, "feat-testfail"),
		"failing tests must not create a watchdog commit")
}

func TestHeartbeatFormat(t *testing.T) {
	clone, _ := setupRepoClone(t, "feat-heart")
	var buf bytes.Buffer
	l := newLoop(clone, "feat-heart", &buf)

	l.heartbeat()

	line := buf.String()
	require.True(t, strings.HasPrefix(line, "DREM-HEARTBEAT "),
		"heartbeat line %q must start with the DREM-HEARTBEAT marker", line)
	require.Contains(t, line, "agent_id=agent-123")
	// The frozen clock produces this exact RFC3339 stamp.
	require.Contains(t, line, "timestamp=2026-04-19T12:00:00Z")
	require.True(t, strings.HasSuffix(line, "\n"))
}

func TestTestCmdEmptyIsNoOp(t *testing.T) {
	clone, bare := setupRepoClone(t, "feat-notest")
	l := newLoop(clone, "feat-notest", nil)
	// Leave TestCmd empty.

	require.NoError(t, os.WriteFile(filepath.Join(clone, "x.txt"), []byte("x"), 0o644))
	before := countBranchCommits(t, bare, "feat-notest")
	require.NoError(t, l.tickTest(context.Background()))
	require.Equal(t, before, countBranchCommits(t, bare, "feat-notest"),
		"empty TestCmd must not commit")
}
