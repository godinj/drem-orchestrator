package benchv2

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCase8RunsProductionOwnershipReplay(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	_, tasks, err := LoadManifest(filepath.Join(repo, "bench", "canvasbench-v2", "manifest.json"))
	require.NoError(t, err)
	task := tasks[7]
	prepared, err := PrepareFixture(repo, t.TempDir(), task.Fixture)
	require.NoError(t, err)
	defer prepared.Cleanup()
	run, err := (ReplayAdapter{}).Run(context.Background(), TrialRequest{Task: task, WorkDir: prepared.WorkDir, Harness: validHarness()})
	require.NoError(t, err, run.Output)
	require.Equal(t, "deterministic", run.StopReason)
	require.True(t, run.Telemetry.CheckpointObserved)
	require.Equal(t, "not_applicable", run.ServerUsage.Source)
	require.Contains(t, run.Trajectory.Extra, "diagnostic_permutations")
}
