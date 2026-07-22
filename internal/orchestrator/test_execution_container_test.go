package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// A deterministic preliminary-gate failure is typed and routed back to
// implementation even when a worker spawner is available. It must not launch
// an unplanned fixer or spend inference tokens.
func TestProcessTestingReady_DoesNotDispatchFixerViaSpawner(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")

	bareRepoPath := setupTestRepoWithMainBranch(t)
	featureName := "phase35-testing-ready-fixer"
	_ = createFeatureWorktree(t, bareRepoPath, featureName)

	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "phase35-testing-ready-fixer",
		BareRepoPath:  bareRepoPath,
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	events := make(chan Event, 64)
	o := &Orchestrator{
		db:              db,
		projectID:       projectID,
		worktree:        &FakeWorktreeManager{BarePath: bareRepoPath, Default: "main"},
		events:          events,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "test_execution_container_test"),
		Spawner:         fake,
		GitrefRegistry:  gitref.NewRegistry(db),
		testGate:        TestGateConfig{TestCommand: "go test ./..."},
	}

	parentID := uuid.New()
	parent := model.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "parent whose tests fail",
		Description:    "parent",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	recordBranchAcceptanceForTest(t, &parent, bareRepoPath, "main")
	require.NoError(t, db.Create(&parent).Error)
	persistBranchAcceptanceForTest(t, db, &parent)

	require.NoError(t, o.processTestingReady(&parent))

	require.Empty(t, fake.spawnCalls, "deterministic gate failures must not spend model tokens")
	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", parentID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)
	var gateRun model.PreliminaryGateRun
	require.NoError(t, db.Where("task_id = ?", parentID).First(&gateRun).Error)
	require.Equal(t, model.PreliminaryGateCodeFailure, gateRun.Outcome)
}
