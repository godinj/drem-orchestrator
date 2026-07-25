package benchv2

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTakeCycleOraclesAgainstPinnedCanvas is an opt-in native acceptance test.
// It is intentionally excluded from ordinary Go-only gates because it builds
// the pinned Canvas fixture several times and requires Canvas's Skia cache.
// Run it with -timeout 35m; the complete red/green/mutant/Release matrix
// exceeds Go's default ten-minute package timeout on the reference Mac.
func TestTakeCycleOraclesAgainstPinnedCanvas(t *testing.T) {
	repo := os.Getenv("CANVASBENCH_REAL_CANVAS_REPO")
	if repo == "" {
		t.Skip("set CANVASBENCH_REAL_CANVAS_REPO to run native Canvas oracle acceptance")
	}
	assertPortableCanvasFixture(t, repo)

	suiteRoot, err := filepath.Abs(filepath.Join("..", "..", "bench", "canvasbench-v2"))
	require.NoError(t, err)
	_, tasks, err := LoadManifest(filepath.Join(suiteRoot, "manifest.json"))
	require.NoError(t, err)
	oracles := BuiltinVerifier{OracleRoot: filepath.Join(suiteRoot, "oracles")}

	byID := make(map[string]TaskSpec, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}

	t.Run("case-04", func(t *testing.T) {
		task := byID["case-04"]
		uncorrected := prepareRealTakeCandidate(t, repo, task)
		outcome := oracles.Verify(context.Background(), task, uncorrected.WorkDir, HarnessRun{})
		require.False(t, outcome.Passed, "clean base must not satisfy the red-test oracle")
		require.NoError(t, uncorrected.Cleanup())

		canonical := prepareRealTakeCandidate(t, repo, task)
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeTestsPatch))
		outcome = oracles.Verify(context.Background(), task, canonical.WorkDir, HarnessRun{})
		require.True(t, outcome.Passed, outcome.Failures)
		require.NoError(t, canonical.Cleanup())
	})

	t.Run("case-05", func(t *testing.T) {
		task := byID["case-05"]
		uncorrected := prepareRealTakeCandidate(t, repo, task)
		outcome := oracles.Verify(context.Background(), task, uncorrected.WorkDir, HarnessRun{})
		require.False(t, outcome.Passed, "clean base must not satisfy the implementation oracle")
		require.NoError(t, uncorrected.Cleanup())

		canonical := prepareRealTakeCandidate(t, repo, task)
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeImplPatch))
		runRealTakeChangedGate(t, canonical.WorkDir)
		outcome = oracles.Verify(context.Background(), task, canonical.WorkDir, HarnessRun{})
		require.True(t, outcome.Passed, outcome.Failures)
		require.NoError(t, canonical.Cleanup())
	})

	t.Run("case-06", func(t *testing.T) {
		task := byID["case-06"]
		uncorrected := prepareRealTakeCandidate(t, repo, task)
		outcome := oracles.Verify(context.Background(), task, uncorrected.WorkDir, HarnessRun{})
		require.False(t, outcome.Passed, "preserved bad artifact must not satisfy the repair oracle")
		require.NoError(t, uncorrected.Cleanup())

		canonical := prepareRealTakeCandidate(t, repo, task)
		require.NoError(t, restoreTakeCycleBaseFiles(canonical.WorkDir, task.Fixture.BaseCommit, []string{
			takeTestFile,
			takeHeaderFile,
			takeSourceFile,
			takeHandlerFile,
			takeRegisterFile,
		}))
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeTestsPatch))
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeImplPatch))
		runRealTakeChangedGate(t, canonical.WorkDir)
		outcome = oracles.Verify(context.Background(), task, canonical.WorkDir, HarnessRun{})
		require.True(t, outcome.Passed, outcome.Failures)
		require.NoError(t, canonical.Cleanup())
	})

	t.Run("case-09", func(t *testing.T) {
		task := byID["case-09"]
		uncorrected := prepareRealTakeCandidate(t, repo, task)
		outcome := oracles.Verify(context.Background(), task, uncorrected.WorkDir, HarnessRun{})
		require.False(t, outcome.Passed, "clean base must not satisfy the capstone oracle")
		require.NoError(t, uncorrected.Cleanup())

		canonical := prepareRealTakeCandidate(t, repo, task)
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeTestsPatch))
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeImplPatch))
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeKeymapPatch))
		changed, changedErr := ChangedPaths(canonical.WorkDir)
		require.NoError(t, changedErr)
		require.ElementsMatch(t, task.RequiredChangedPaths, changed, "Release candidate must contain only the six declared candidate paths")
		runRealTakeChangedGate(t, canonical.WorkDir)
		outcome = oracles.Verify(context.Background(), task, canonical.WorkDir, HarnessRun{})
		require.True(t, outcome.Passed, outcome.Failures)
		require.NoError(t, validateReleaseArtifact(task.ReleaseArtifactPath, outcome.ReleaseArtifact))
		require.NoError(t, canonical.Cleanup())
	})
}

func assertPortableCanvasFixture(t *testing.T, repo string) {
	t.Helper()
	const portable = "da8d567ea85a6ffc08e7a1ec0d3d7e49802306fc"
	const parent = "96db6b709f0a4f2069db4a7d3415ef17867b0274"
	cmd := exec.Command("git", "-C", repo, "rev-parse", portable+"^")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Equal(t, parent, string(bytes.TrimSpace(output)))
	cmd = exec.Command("git", "-C", repo, "diff-tree", "--no-commit-id", "--name-only", "-r", portable)
	output, err = cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.ElementsMatch(t, []string{
		"src/dc/plugins/PluginScanner.cpp",
		"src/engine/AudioPitchProcessor.cpp",
		"src/model/AudioClip.cpp",
		"tests/CMakeLists.txt",
		"tests/unit/model_layer/test_tempo_map.cpp",
	}, strings.Fields(string(output)))
}

func prepareRealTakeCandidate(t *testing.T, repo string, task TaskSpec) *PreparedFixture {
	t.Helper()
	prepared, err := PrepareFixture(repo, t.TempDir(), task.Fixture)
	require.NoError(t, err)
	t.Cleanup(func() { _ = prepared.Cleanup() })
	return prepared
}

func applyRealTakeOracle(t *testing.T, workDir, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, applyPatchBytes(workDir, raw))
}

func runRealTakeChangedGate(t *testing.T, workDir string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(workDir, "scripts", "dev"), "check", "changed")
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
