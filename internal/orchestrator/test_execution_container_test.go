package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestProcessTestingReady_DispatchesFixerViaSpawner exercises the
// container-mode fixer re-dispatch path: when tests fail, o.Spawner
// is wired, and the task has not previously attempted a fixer,
// processTestingReady spawns the fixer via o.Spawner.SpawnWorker
// instead of o.runner.SpawnAgentInWorktree.
//
// Uses a real bare repo + integration worktree (so go test ./...
// runs, fails — no Go files — and triggers the fixer branch), and a
// fakeWorkerSpawner so the spawn call is captured without actually
// running docker.
//
// See plans/phase-3.5-subtask-dispatch-migration.md Commit 5.
func TestProcessTestingReady_DispatchesFixerViaSpawner(t *testing.T) {
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
	require.NoError(t, db.Create(&parent).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-testing-fixer"}},
	}

	require.NoError(t, o.processTestingReady(&parent))

	// Spawner was called with AgentType=fixer (go test ./... failed
	// in the bare-repo worktree because there are no .go files).
	require.Len(t, fake.spawnCalls, 1, "expected one SpawnWorker call for fixer re-dispatch")
	assert.Equal(t, "fixer", fake.spawnCalls[0].AgentType)
	assert.Equal(t, "/host/.claude/.credentials.json", fake.spawnCalls[0].CredsMount)

	// Reload parent: AssignedAgentID now points at the fixer Agent row;
	// testing_ready_fixer_attempted context flag is set so a second
	// call would fall through to human-review rather than re-spawning.
	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", parentID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, db.First(&ag, "id = ?", reloaded.AssignedAgentID).Error)
	assert.Equal(t, "container-testing-fixer", ag.TmuxSession)
	assert.Equal(t, model.AgentFixer, ag.AgentType)

	if v, ok := reloaded.Context["testing_ready_fixer_attempted"].(bool); !ok || !v {
		t.Errorf("expected testing_ready_fixer_attempted=true on parent.Context; got %v",
			reloaded.Context["testing_ready_fixer_attempted"])
	}
}
