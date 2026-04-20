package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

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

// backdateAgentPastGrace pushes the agent's created_at well past the
// spawn grace period so reconcileStuckAgents actually evaluates it.
// Without this the grace-period gate swallows every new agent.
func backdateAgentPastGrace(t *testing.T, o *Orchestrator, agentID uuid.UUID) {
	t.Helper()
	require.NoError(t, o.db.Model(&model.Agent{}).
		Where("id = ?", agentID).
		Update("created_at", time.Now().Add(-2*agentSpawnGracePeriod)).Error)
}

// TestReconcileStuckAgents_ContainerAlive_Skips verifies the fix: a
// container-mode agent whose container the spawner reports as running
// is NOT false-positive killed. This is the bug that caused the T3 v6
// canary respawn loop (focused_wiles / nifty_cray) — orch thought the
// first worker was dead after 60s because the container runner path
// never populates runner.GetRunningAgents.
func TestReconcileStuckAgents_ContainerAlive_Skips(t *testing.T) {
	o, fake := reconcileTestRig(t)
	task := createTaskWithContainer(t, o, "container-alive-abc")
	backdateAgentPastGrace(t, o, *task.AssignedAgentID)

	fake.listResult = spawner.ListWorkersResult{
		Workers: []spawner.WorkerInfo{
			{ContainerID: "container-alive-abc", Project: o.projectID.String(), Status: "running"},
		},
	}

	fixes, err := o.reconcileStuckAgents()
	require.NoError(t, err)
	require.Zero(t, fixes, "live container must not be flagged stuck")

	// Task unchanged.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)
	require.NotNil(t, reloaded.AssignedAgentID, "assignment must remain")
	if reloaded.Context != nil {
		_, hasRetry := reloaded.Context["retry_count"]
		require.False(t, hasRetry, "no retry_count should be written for live container")
	}

	// Agent still working.
	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", *task.AssignedAgentID).Error)
	require.Equal(t, model.AgentWorking, ag.Status)
}

// TestReconcileStuckAgents_ContainerExited_Kills confirms the existing
// dead-agent path still fires when the spawner reports the container as
// no longer running. This is the case the legacy host-spawn path
// handled via runner.GetRunningAgents and that we must preserve.
func TestReconcileStuckAgents_ContainerExited_Kills(t *testing.T) {
	o, fake := reconcileTestRig(t)
	task := createTaskWithContainer(t, o, "container-exited-xyz")
	backdateAgentPastGrace(t, o, *task.AssignedAgentID)

	// Container present in list but status != running.
	fake.listResult = spawner.ListWorkersResult{
		Workers: []spawner.WorkerInfo{
			{ContainerID: "container-exited-xyz", Project: o.projectID.String(), Status: "exited"},
		},
	}

	fixes, err := o.reconcileStuckAgents()
	require.NoError(t, err)
	require.Equal(t, 1, fixes, "exited container must be flagged stuck")

	// Agent marked dead.
	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", *task.AssignedAgentID).Error)
	require.Equal(t, model.AgentDead, ag.Status)

	// Task auto-retried (retry_count bumped, backlog since InProgress).
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusBacklog, reloaded.Status)
	require.NotNil(t, reloaded.Context)
	rc, ok := reloaded.Context["retry_count"].(float64)
	require.True(t, ok, "retry_count must be written as float64")
	require.Equal(t, float64(1), rc)
}

// TestReconcileStuckAgents_ContainerMissing_Kills covers the case where
// the container is absent from ListWorkers entirely (e.g. removed
// out-of-band). The agent should be treated the same as an exited one.
func TestReconcileStuckAgents_ContainerMissing_Kills(t *testing.T) {
	o, fake := reconcileTestRig(t)
	task := createTaskWithContainer(t, o, "container-missing-def")
	backdateAgentPastGrace(t, o, *task.AssignedAgentID)

	fake.listResult = spawner.ListWorkersResult{Workers: []spawner.WorkerInfo{}}

	fixes, err := o.reconcileStuckAgents()
	require.NoError(t, err)
	require.Equal(t, 1, fixes)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", *task.AssignedAgentID).Error)
	require.Equal(t, model.AgentDead, ag.Status)
}

// TestReconcileStuckAgents_SpawnerListError_FallsBackToLegacy verifies
// that a transient ListWorkers RPC failure does not crash or hang the
// reconciler. The set stays empty, legacy host-mode tracking works as
// before, and container-mode agents may be flagged dead (pre-fix
// behaviour — deliberately no worse than today on spawner outage).
func TestReconcileStuckAgents_SpawnerListError_FallsBackToLegacy(t *testing.T) {
	o, fake := reconcileTestRig(t)
	task := createTaskWithContainer(t, o, "container-errd")
	backdateAgentPastGrace(t, o, *task.AssignedAgentID)

	fake.listErr = errors.New("spawner RPC unreachable")

	fixes, err := o.reconcileStuckAgents()
	require.NoError(t, err, "reconciler must not propagate spawner RPC error")
	require.Equal(t, 1, fixes, "fallback behaviour kills the agent as pre-fix code did")

	// Agent marked dead (the pre-fix false-positive path).
	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", *task.AssignedAgentID).Error)
	require.Equal(t, model.AgentDead, ag.Status)

	_ = task
}

// TestReconcileStuckAgents_LegacyRunnerPathIntact is a regression guard
// for host-mode agents. An agent with an empty container ID (no
// TmuxSession) and no entry in runner.running must still be flagged
// dead exactly as before. No container changes may mask legacy dead
// agents.
func TestReconcileStuckAgents_LegacyRunnerPathIntact(t *testing.T) {
	o, fake := reconcileTestRig(t)

	// Host-mode agent: empty TmuxSession (no container) and no runner
	// configured. The stuck-agent path should still fire.
	agentID := uuid.New()
	ag := model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "legacy-host-agent",
		Status:    model.AgentWorking,
		// TmuxSession intentionally empty.
	}
	require.NoError(t, o.db.Create(&ag).Error)

	task := &model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "Legacy host task",
		Description:     "x",
		Status:          model.StatusInProgress,
		AssignedAgentID: &agentID,
	}
	require.NoError(t, o.db.Create(task).Error)
	ag.CurrentTaskID = &task.ID
	require.NoError(t, o.db.Save(&ag).Error)
	backdateAgentPastGrace(t, o, agentID)

	// Spawner knows about no workers — legacy path does the talking.
	fake.listResult = spawner.ListWorkersResult{Workers: []spawner.WorkerInfo{}}

	fixes, err := o.reconcileStuckAgents()
	require.NoError(t, err)
	require.Equal(t, 1, fixes, "legacy host agent with no container must still be flagged")

	var reloadedAg model.Agent
	require.NoError(t, o.db.First(&reloadedAg, "id = ?", agentID).Error)
	require.Equal(t, model.AgentDead, reloadedAg.Status)
}
