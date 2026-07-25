package benchv2

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTakeCycleOraclesAgainstPinnedCanvas is an opt-in native acceptance test.
// It is intentionally excluded from ordinary Go-only gates because it builds
// the pinned Canvas fixture several times and requires Canvas's Skia cache.
func TestTakeCycleOraclesAgainstPinnedCanvas(t *testing.T) {
	repo := os.Getenv("CANVASBENCH_REAL_CANVAS_REPO")
	if repo == "" {
		t.Skip("set CANVASBENCH_REAL_CANVAS_REPO to run native Canvas oracle acceptance")
	}

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
			"src/vim/adapters/EditorAdapter.h",
			"src/vim/adapters/fragments/EditorAdapterActionHandlers.inc",
			"src/vim/adapters/fragments/EditorAdapterActionRegistration.inc",
		}))
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeTestsPatch))
		applyRealTakeOracle(t, canonical.WorkDir, filepath.Join(oracles.OracleRoot, takeImplPatch))
		outcome = oracles.Verify(context.Background(), task, canonical.WorkDir, HarnessRun{})
		require.True(t, outcome.Passed, outcome.Failures)
		require.NoError(t, canonical.Cleanup())
	})
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
