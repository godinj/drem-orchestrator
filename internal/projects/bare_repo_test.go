package projects

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// initBareRepo initialises a bare git repo in a temp dir and returns
// the path. Used by every test in this file; kept local (rather than
// promoting to testutil) because testutil already imports a richer
// helper and this one wants the minimal layout: `git init --bare` and
// nothing else.
func initBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "test.git")
	cmd := exec.Command("git", "init", "--bare", bare)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare failed: %s", out)
	return bare
}

// readConfig returns the current value of the given git config key in
// the bare repo, or the empty string when the key is unset.
func readConfig(t *testing.T, bare, key string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+bare, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		// git config --get exits 1 when the key is unset; tests treat
		// that as "unset" rather than a failure.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ""
		}
		t.Fatalf("git config --get %s failed: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

// TestConfigureBareRepo_HappyPath asserts that ConfigureBareRepo sets
// receive.denyCurrentBranch=ignore on a freshly-initialised bare
// repo.
func TestConfigureBareRepo_HappyPath(t *testing.T) {
	bare := initBareRepo(t)

	err := ConfigureBareRepo(bare)
	require.NoError(t, err)

	require.Equal(t, "ignore", readConfig(t, bare, "receive.denyCurrentBranch"))
}

// TestConfigureBareRepo_Idempotent asserts that calling the helper
// twice is safe (no error, value unchanged).
func TestConfigureBareRepo_Idempotent(t *testing.T) {
	bare := initBareRepo(t)

	require.NoError(t, ConfigureBareRepo(bare))
	require.NoError(t, ConfigureBareRepo(bare))

	require.Equal(t, "ignore", readConfig(t, bare, "receive.denyCurrentBranch"))
}

// TestConfigureBareRepo_OverwritesDifferingValue asserts that the
// helper is authoritative: a pre-existing value for
// receive.denyCurrentBranch is overwritten with ignore. The seed
// value here is updateInstead because that was the original design
// target before the container path-mismatch discovery; this test
// doubles as a migration check for operators whose bare repo still
// carries the old setting.
func TestConfigureBareRepo_OverwritesDifferingValue(t *testing.T) {
	bare := initBareRepo(t)

	// Seed the repo with a different value.
	cmd := exec.Command("git", "--git-dir="+bare, "config",
		"receive.denyCurrentBranch", "updateInstead")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "seed git config failed: %s", out)
	require.Equal(t, "updateInstead", readConfig(t, bare, "receive.denyCurrentBranch"))

	require.NoError(t, ConfigureBareRepo(bare))

	require.Equal(t, "ignore", readConfig(t, bare, "receive.denyCurrentBranch"))
}

// TestConfigureBareRepo_MissingPath asserts that a non-existent path
// surfaces a clear error rather than silently succeeding (git would
// create the config file if we let it).
func TestConfigureBareRepo_MissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.git")

	err := ConfigureBareRepo(missing)
	require.Error(t, err)
	require.Contains(t, err.Error(), missing)
}

// TestConfigureBareRepo_NotAGitRepo asserts that the helper refuses
// to configure a directory that is not a git repository.
func TestConfigureBareRepo_NotAGitRepo(t *testing.T) {
	empty := t.TempDir()

	err := ConfigureBareRepo(empty)
	require.Error(t, err)
}

// TestConfigureBareRepo_PushSemantics is an integration test that
// exercises the real reason we set receive.denyCurrentBranch=ignore:
// a push from a clone to a branch that is ALSO checked out in a
// host worktree under the bare repo must succeed, AND the worktree
// must stay frozen at its old working-tree contents (stale) while
// the branch ref advances.
//
// This mirrors the drem container layout: the bare repo lives at
// `<bare>` with host worktrees `git worktree add`ed under
// `<bare>/feature/<id>/integration`. Workers push from inside a
// container that has `<bare>` bind-mounted; receive-pack runs on
// the bare repo and, with `ignore`, accepts the push without
// touching the worktree's working tree. Merger later reads the
// bare refs directly (fresh clone per run), so staleness is safe.
//
// Validation:
//  1. Bare repo accepts the push (exit 0).
//  2. Bare repo's feature-branch ref advances to the new commit.
//  3. Host worktree's working-tree files remain at the OLD contents
//     (staleness confirmed); git status in the worktree reports the
//     diff between worktree files and the (new) branch tip.
func TestConfigureBareRepo_PushSemantics(t *testing.T) {
	// Skip if git is missing (unlikely in CI, but keeps the test
	// honest as a true integration test).
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		// Avoid picking up the user's global git config which may
		// set signing, hooks, or default-branch names that break
		// deterministic assertions.
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	run := func(t *testing.T, dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v in %s failed: %s",
			args, dir, out)
		return out
	}

	tmp := t.TempDir()

	// 1. Bare repo + initial master commit via a seeder clone.
	bare := filepath.Join(tmp, "test.git")
	run(t, tmp, "init", "--bare", "-b", "master", bare)

	seeder := filepath.Join(tmp, "seeder")
	run(t, tmp, "clone", bare, seeder)
	require.NoError(t, os.WriteFile(
		filepath.Join(seeder, "README.md"), []byte("seed\n"), 0o644))
	run(t, seeder, "add", "README.md")
	run(t, seeder, "commit", "-m", "seed")
	run(t, seeder, "push", "origin", "master")
	// Seeder has served its purpose; real workflow wouldn't keep it.
	require.NoError(t, os.RemoveAll(seeder))

	// 2. Host worktree checked out on a feature branch, mirroring
	//    `git worktree add <bare>/feature/<id>/integration -b ...`.
	wt := filepath.Join(bare, "feature", "canary", "integration")
	run(t, bare, "--git-dir="+bare, "worktree", "add",
		"-b", "feature/canary", wt, "master")
	oldTip := strings.TrimSpace(string(
		run(t, bare, "--git-dir="+bare, "rev-parse", "feature/canary")))

	// 3. Apply the config under test.
	require.NoError(t, ConfigureBareRepo(bare))
	require.Equal(t, "ignore",
		readConfig(t, bare, "receive.denyCurrentBranch"))

	// 4. A separate "pusher" clone does what a worker does: commit
	//    on feature/canary and push to the bare repo. Without
	//    `ignore`, this push would be rejected because feature/canary
	//    is checked out in the host worktree.
	pusher := filepath.Join(tmp, "pusher")
	run(t, tmp, "clone", "-b", "feature/canary", bare, pusher)
	require.NoError(t, os.WriteFile(
		filepath.Join(pusher, "canary.txt"),
		[]byte("canary\n"), 0o644))
	run(t, pusher, "add", "canary.txt")
	run(t, pusher, "commit", "-m", "canary commit")
	newTip := strings.TrimSpace(string(
		run(t, pusher, "rev-parse", "HEAD")))
	require.NotEqual(t, oldTip, newTip, "pusher must advance the branch")

	// Push to bare. This is the assertion the whole test exists for:
	// with receive.denyCurrentBranch=ignore, this must succeed even
	// though the branch is checked out in the host worktree.
	pushCmd := exec.Command("git", "push", "origin",
		"feature/canary:feature/canary")
	pushCmd.Dir = pusher
	pushCmd.Env = gitEnv
	pushOut, pushErr := pushCmd.CombinedOutput()
	require.NoError(t, pushErr,
		"push to bare must succeed with ignore: %s", pushOut)

	// 5. Bare ref advanced.
	bareTipAfter := strings.TrimSpace(string(
		run(t, bare, "--git-dir="+bare, "rev-parse", "feature/canary")))
	require.Equal(t, newTip, bareTipAfter,
		"bare repo's feature/canary must point at the pushed commit")

	// 6. Worktree's working tree is stale: canary.txt should NOT
	//    exist in the host worktree (push didn't touch the working
	//    tree — that's the whole point of `ignore`).
	_, err := os.Stat(filepath.Join(wt, "canary.txt"))
	require.True(t, os.IsNotExist(err),
		"host worktree must remain stale: canary.txt should not "+
			"have been materialised (got err=%v)", err)

	// 7. The worktree's branch ref (shared with the bare repo) HAS
	//    advanced, so `git -C <wt> status` reports the divergence
	//    between the old working-tree contents and the new HEAD.
	//    We assert canary.txt appears in the porcelain status as a
	//    deletion (HEAD has it, worktree doesn't).
	statusOut := run(t, wt, "status", "--porcelain")
	require.Contains(t, string(statusOut), "canary.txt",
		"git status in stale worktree should report canary.txt "+
			"as a divergence (HEAD advanced, working tree stale)")
}
