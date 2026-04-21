package projects

import (
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
// receive.denyCurrentBranch=updateInstead on a freshly-initialised
// bare repo.
func TestConfigureBareRepo_HappyPath(t *testing.T) {
	bare := initBareRepo(t)

	err := ConfigureBareRepo(bare)
	require.NoError(t, err)

	require.Equal(t, "updateInstead", readConfig(t, bare, "receive.denyCurrentBranch"))
}

// TestConfigureBareRepo_Idempotent asserts that calling the helper
// twice is safe (no error, value unchanged).
func TestConfigureBareRepo_Idempotent(t *testing.T) {
	bare := initBareRepo(t)

	require.NoError(t, ConfigureBareRepo(bare))
	require.NoError(t, ConfigureBareRepo(bare))

	require.Equal(t, "updateInstead", readConfig(t, bare, "receive.denyCurrentBranch"))
}

// TestConfigureBareRepo_OverwritesDifferingValue asserts that the
// helper is authoritative: a pre-existing value for
// receive.denyCurrentBranch is overwritten with updateInstead.
func TestConfigureBareRepo_OverwritesDifferingValue(t *testing.T) {
	bare := initBareRepo(t)

	// Seed the repo with a different value.
	cmd := exec.Command("git", "--git-dir="+bare, "config",
		"receive.denyCurrentBranch", "ignore")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "seed git config failed: %s", out)
	require.Equal(t, "ignore", readConfig(t, bare, "receive.denyCurrentBranch"))

	require.NoError(t, ConfigureBareRepo(bare))

	require.Equal(t, "updateInstead", readConfig(t, bare, "receive.denyCurrentBranch"))
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
