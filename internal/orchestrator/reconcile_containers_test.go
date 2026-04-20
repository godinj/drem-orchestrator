package orchestrator

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func reconcileTestRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	// Real bare repo: reconcileOnStartup's respawn path reaches
	// spawnTypedWorker which pre-creates the feature branch via
	// gitref.EnsureBranch. A literal /tmp/fake-bare would fail the
	// branch-ensure step on every respawn test.
	bareRepo := testutil.SetupBareRepo(t)
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "reconcile-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: bareRepo, Default: "main"},
		logger:         slog.Default().With("component", "reconcile_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake
}

// createTaskWithContainer seeds a task whose assigned agent carries the
// given container ID. Returns both so tests can assert post-reconcile state.
func createTaskWithContainer(t *testing.T, o *Orchestrator, containerID string) *model.Task {
	t.Helper()
	ag := model.Agent{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		AgentType:   model.AgentCoder,
		Name:        "test-agent",
		Status:      model.AgentWorking,
		TmuxSession: containerID,
	}
	require.NoError(t, o.db.Create(&ag).Error)

	task := &model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "In flight task",
		Description:     "x",
		Status:          model.StatusInProgress,
		WorktreeBranch:  "feature/test-branch",
		AssignedAgentID: &ag.ID,
	}
	require.NoError(t, o.db.Create(task).Error)

	// Link agent back to task.
	ag.CurrentTaskID = &task.ID
	require.NoError(t, o.db.Save(&ag).Error)
	return task
}

func TestReconcileOnStartup_LiveContainersUntouched(t *testing.T) {
	o, fake := reconcileTestRig(t)
	task := createTaskWithContainer(t, o, "container-alive")

	fake.listResult = spawner.ListWorkersResult{
		Workers: []spawner.WorkerInfo{
			{ContainerID: "container-alive", Project: o.projectID.String()},
		},
	}

	require.NoError(t, o.reconcileOnStartup(context.Background()))
	require.Empty(t, fake.spawnCalls, "live containers must not be respawned")

	// Task agent binding must remain.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
}

func TestReconcileOnStartup_GoneContainersRespawn(t *testing.T) {
	o, fake := reconcileTestRig(t)
	task := createTaskWithContainer(t, o, "container-gone")

	// Spawner reports an empty list — the container is not live.
	fake.listResult = spawner.ListWorkersResult{Workers: []spawner.WorkerInfo{}}
	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-fresh"}},
	}

	require.NoError(t, o.reconcileOnStartup(context.Background()))

	require.Len(t, fake.spawnCalls, 1, "gone container should trigger respawn")
	require.Equal(t, task.ID.String(), fake.spawnCalls[0].Labels["drem.task_id"])

	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID, "fresh agent must be assigned")
	// The old stale agent binding was cleared and a new one written via
	// spawnCoder's recordContainerOnAgent path.
}

func TestReconcileOnStartup_TasksWithoutContainerIDIgnored(t *testing.T) {
	o, fake := reconcileTestRig(t)

	// Legacy task: agent has a tmux-session-shaped value (contains '/'), not
	// a container ID. reconcileOnStartup must leave it untouched.
	ag := model.Agent{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		AgentType:   model.AgentCoder,
		Name:        "legacy",
		Status:      model.AgentWorking,
		TmuxSession: "dashboard/coder - Fix bug abcd",
	}
	require.NoError(t, o.db.Create(&ag).Error)

	task := &model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "Legacy task",
		Description:     "x",
		Status:          model.StatusInProgress,
		WorktreeBranch:  "feature/legacy",
		AssignedAgentID: &ag.ID,
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.listResult = spawner.ListWorkersResult{Workers: []spawner.WorkerInfo{}}

	require.NoError(t, o.reconcileOnStartup(context.Background()))
	require.Empty(t, fake.spawnCalls, "legacy tasks must not be respawned")
}

func TestReconcileOnStartup_WithoutSpawnerIsNoop(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "noop",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}).Error)
	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		events:    make(chan Event, 8),
		logger:    slog.Default(),
	}
	require.NoError(t, o.reconcileOnStartup(context.Background()))
}

func TestIsLegacyTmuxSession(t *testing.T) {
	require.True(t, isLegacyTmuxSession("dashboard/coder - Fix bug abcd"))
	require.True(t, isLegacyTmuxSession("session:abc"))
	require.True(t, isLegacyTmuxSession("foo bar"))
	require.False(t, isLegacyTmuxSession("abc123def456"))
	require.False(t, isLegacyTmuxSession("c7a3b2f1-5d9e-4e8a-b3f5-2e7f0a1d2c3b"))
}
