package benchv2

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareFixtureAttestsSeedAndCleansUp(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "bench@example.com")
	runGit("config", "user.name", "Bench")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "visible.txt"), []byte("before\n"), 0o644))
	runGit("add", "visible.txt")
	runGit("commit", "-m", "fixture")
	base := runGit("rev-parse", "HEAD")
	blob := runGit("rev-parse", "HEAD:visible.txt")
	patch := filepath.Join(t.TempDir(), "seed.patch")
	patchBody := "diff --git a/visible.txt b/visible.txt\nindex " + blob[:7] + "..0000000 100644\n--- a/visible.txt\n+++ b/visible.txt\n@@ -1 +1 @@\n-before\n+after\n"
	require.NoError(t, os.WriteFile(patch, []byte(patchBody), 0o644))
	digest := sha256Hex([]byte(patchBody))
	fixture := Fixture{RepoID: "fixture", BaseCommit: base, VisibleBlobs: []BlobPin{{Path: "visible.txt", SHA: blob}}, SeedPatch: patch, SeedPatchSHA: digest}
	prepared, err := PrepareFixture(repo, t.TempDir(), fixture)
	require.NoError(t, err)
	workDir := prepared.WorkDir
	raw, err := os.ReadFile(filepath.Join(workDir, "visible.txt"))
	require.NoError(t, err)
	require.Equal(t, "after\n", string(raw))
	require.NoError(t, prepared.Cleanup())
	_, err = os.Stat(workDir)
	require.True(t, os.IsNotExist(err))
}

func TestChangedPathsPreservesLeadingPorcelainStatusColumns(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "bench@example.com")
	runGit("config", "user.name", "Bench")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "src", "ui"), 0o755))
	path := filepath.Join(repo, "src", "ui", "ActionCoordinator.cpp")
	require.NoError(t, os.WriteFile(path, []byte("before\n"), 0o644))
	runGit("add", "src/ui/ActionCoordinator.cpp")
	runGit("commit", "-m", "fixture")
	require.NoError(t, os.WriteFile(path, []byte("after\n"), 0o644))

	paths, err := ChangedPaths(repo)
	require.NoError(t, err)
	require.Equal(t, []string{"src/ui/ActionCoordinator.cpp"}, paths)
}

func sha256Hex(raw []byte) string {
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}
