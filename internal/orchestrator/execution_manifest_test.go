package orchestrator

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestCompileExecutionManifestSelectsAtomicLaneForSharedWriter(t *testing.T) {
	task := &model.Task{ID: uuid.New(), Title: "divide transients", WorktreeBaseSHA: "base-sha"}
	plans := []planEntry{
		{Title: "model contract", Phase: "test", Files: []string{"tests/transients.cpp"}, TestsFor: []int{2}},
		{Title: "action contract", Phase: "test", Files: []string{"tests/transients.cpp"}, Dependencies: []int{0}, TestsFor: []int{3}},
		{Title: "model", Phase: "implementation", Files: []string{"src/audio.cpp"}, Dependencies: []int{0}},
		{Title: "action", Phase: "implementation", Files: []string{"src/action.cpp"}, Dependencies: []int{1, 2}},
	}

	manifest, err := compileExecutionManifest(task, plans, "spec-fingerprint")
	require.NoError(t, err)
	require.Equal(t, executionLaneAtomic, manifest.Lane)
	require.Len(t, manifest.Steps, 4)
	require.NotEmpty(t, manifest.Hash)
	require.Equal(t, []string{"step-02", "step-03"}, manifest.Steps[3].Dependencies)
	require.Zero(t, manifest.Recovery.BlindRetries)

	collapsed := collapseAtomicPlan(task, plans)
	require.Len(t, collapsed, 1)
	require.Equal(t, "implementation", collapsed[0].Phase)
	require.ElementsMatch(t, []string{"tests/transients.cpp", "src/audio.cpp", "src/action.cpp"}, collapsed[0].WritableFiles)
}

func TestCompileExecutionManifestKeepsIndependentDAG(t *testing.T) {
	task := &model.Task{ID: uuid.New(), Title: "independent work"}
	plans := []planEntry{
		{Title: "left", Phase: "implementation", Files: []string{"left.go"}},
		{Title: "right", Phase: "implementation", Files: []string{"right.go"}},
	}

	manifest, err := compileExecutionManifest(task, plans, "")
	require.NoError(t, err)
	require.Equal(t, executionLaneDecomposed, manifest.Lane)
	require.Equal(t, plans, collapseAtomicPlan(task, plans))
}

func TestExecutionManifestHashIsStableAcrossFileOrder(t *testing.T) {
	task := &model.Task{ID: uuid.New(), Title: "stable"}
	left, err := compileExecutionManifest(task, []planEntry{{Title: "x", Phase: "implementation", Files: []string{"b", "a"}}}, "fp")
	require.NoError(t, err)
	right, err := compileExecutionManifest(task, []planEntry{{Title: "x", Phase: "implementation", Files: []string{"a", "b"}}}, "fp")
	require.NoError(t, err)
	require.Equal(t, left.Hash, right.Hash)
}

func TestTaskManifestExpectedTurnsSumsAtomicPlanStages(t *testing.T) {
	task := &model.Task{Context: model.JSONField{"execution_manifest": map[string]any{
		"steps": []any{
			map[string]any{"expected_turns": float64(8)},
			map[string]any{"expected_turns": float64(10)},
			map[string]any{"expected_turns": float64(8)},
		},
	}}}
	require.Equal(t, 26, taskManifestExpectedTurns(task))
}
