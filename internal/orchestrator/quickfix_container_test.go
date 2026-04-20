package orchestrator

import (
	"errors"
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

// newContainerQuickFixRig builds an Orchestrator wired to a real bare
// repo plus a fakeWorkerSpawner so tests can drive processQuickFix /
// respawnQuickFixAgent through the container-mode dispatch path
// (o.Spawner != nil, o.runner == nil). Mirrors newContainerSubtaskRig
// in subtask_scheduling_test.go. The bare repo is real because
// spawnTypedWorker pre-creates the feature branch via
// gitref.EnsureBranch, which requires an actual git object database.
//
// See plans/phase-3.5b-quickfix-migration.md §"Test strategy".
func newContainerQuickFixRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner, model.Project, chan Event, string) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	bareRepo := testutil.SetupBareRepo(t)
	project := testutil.CreateProject(t, db, "phase35b-quickfix-test", bareRepo, "main")

	fake := &fakeWorkerSpawner{}
	events := make(chan Event, 64)
	o := &Orchestrator{
		db:             db,
		projectID:      project.ID,
		events:         events,
		worktree:       &FakeWorktreeManager{BarePath: bareRepo, Default: "main"},
		logger:         slog.Default().With("component", "quickfix_container_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake, project, events, bareRepo
}

// TestProcessQuickFix_DispatchesViaSpawner is the primary acceptance
// test for the Phase 3.5b quickfix-dispatch migration: when o.Spawner
// is wired, a BACKLOG quickfix task routes through
// o.spawnCoder → o.Spawner.SpawnWorker, transitions to IN_PROGRESS,
// records an Agent row whose TmuxSession carries the container ID,
// and emits the quickfix_started event. No legacy runner.SpawnAgent
// call happens (o.runner is nil).
//
// See plans/phase-3.5b-quickfix-migration.md §"Test strategy" test (1).
func TestProcessQuickFix_DispatchesViaSpawner(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, events, _ := newContainerQuickFixRig(t)

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "fix typo in README",
		Description:    "quickfix: correct readme typo",
		Status:         model.StatusBacklog,
		Category:       model.CategoryQuickFix,
		WorktreeBranch: "feature/quickfix-readme-typo",
	}
	require.NoError(t, o.db.Create(&task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-quickfix-001"}},
	}

	require.NoError(t, o.processQuickFix(&task))

	// SpawnWorker was called once with coder params + creds mount.
	require.Len(t, fake.spawnCalls, 1)
	p := fake.spawnCalls[0]
	assert.Equal(t, "coder", p.AgentType)
	assert.Equal(t, project.ID.String(), p.Project)
	assert.Equal(t, task.ID.String(), p.Env["DREM_TASK_ID"])
	assert.Equal(t, "/host/.claude/.credentials.json", p.CredsMount)
	assert.NotContains(t, p.Env, "ANTHROPIC_API_KEY")

	// Task transitioned BACKLOG → IN_PROGRESS and the Agent row carries
	// the container ID in TmuxSession (the container-mode handle
	// convention documented in reconcile_containers.go).
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	assert.Equal(t, model.StatusInProgress, reloaded.Status)
	require.NotNil(t, reloaded.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", reloaded.AssignedAgentID).Error)
	assert.Equal(t, "container-quickfix-001", ag.TmuxSession)
	assert.Equal(t, model.AgentCoder, ag.AgentType)

	// worker_spawned audit event on the task.
	var spawnEvts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?",
		task.ID, "worker_spawned").Find(&spawnEvts).Error)
	require.Len(t, spawnEvts, 1)
	assert.Equal(t, "coder", spawnEvts[0].NewValue)

	// quickfix_started event fired on the orchestrator events channel.
	var sawStarted bool
	for {
		select {
		case evt := <-events:
			if evt.Type == "quickfix_started" {
				sawStarted = true
			}
		default:
			goto done
		}
	}
done:
	assert.True(t, sawStarted, "expected quickfix_started event on events channel")
}

// TestProcessQuickFix_SpawnerFailurePropagates verifies that when the
// container spawner returns an error, processQuickFix surfaces the
// error to the caller (doTick). The task has already been transitioned
// to IN_PROGRESS by that point because the state transition runs BEFORE
// the spawn call — matching the legacy path's semantics. The worker_spawn_failed
// audit event must exist.
func TestProcessQuickFix_SpawnerFailurePropagates(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, _, _ := newContainerQuickFixRig(t)

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "fix doomed quickfix",
		Description:    "quickfix that the spawner refuses",
		Status:         model.StatusBacklog,
		Category:       model.CategoryQuickFix,
		WorktreeBranch: "feature/quickfix-doomed",
	}
	require.NoError(t, o.db.Create(&task).Error)

	fake.spawnResults = []spawnOutcome{{err: errors.New("docker daemon refused")}}

	err := o.processQuickFix(&task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker daemon refused")

	// worker_spawn_failed audit row must exist — spawnTypedWorker writes
	// it via recordSpawnFailureEvent on the spawn-error path.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?",
		task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
}

// TestRespawnQuickFixAgent_DispatchesViaSpawner covers the empty-work
// retry path: when o.Spawner is wired and a quickfix task has
// empty_work=true in its context, respawnQuickFixAgent routes through
// o.spawnCoder → o.Spawner.SpawnWorker and clears the empty_work flag
// so the next tick does not immediately retry again.
//
// See plans/phase-3.5b-quickfix-migration.md §"Test strategy" test (3).
func TestRespawnQuickFixAgent_DispatchesViaSpawner(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, events, _ := newContainerQuickFixRig(t)

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "fix empty-work retry",
		Description:    "quickfix that needs a respawn after empty work",
		Status:         model.StatusInProgress,
		Category:       model.CategoryQuickFix,
		WorktreeBranch: "feature/quickfix-retry",
		Context: model.JSONField{
			"empty_work":        true,
			"retry_count":       float64(1),
			"prompt_adjustment": "You MUST edit the file and commit your changes.",
		},
	}
	require.NoError(t, o.db.Create(&task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-retry-001"}},
	}

	require.NoError(t, o.respawnQuickFixAgent(&task))

	// SpawnWorker called once with coder AgentType.
	require.Len(t, fake.spawnCalls, 1)
	assert.Equal(t, "coder", fake.spawnCalls[0].AgentType)
	assert.Equal(t, "/host/.claude/.credentials.json", fake.spawnCalls[0].CredsMount)

	// Task reloaded with AssignedAgentID set and empty_work cleared.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	if reloaded.Context != nil {
		_, stillSet := reloaded.Context["empty_work"]
		assert.False(t, stillSet,
			"empty_work flag must be cleared after successful respawn")
		// prompt_adjustment is left intact — the respawned agent consumes
		// it via prompt.Generate on the next spawn cycle if the agent
		// produces empty work again.
		assert.Contains(t, reloaded.Context, "prompt_adjustment",
			"prompt_adjustment must survive the respawn")
	}

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", reloaded.AssignedAgentID).Error)
	assert.Equal(t, "container-retry-001", ag.TmuxSession)
	assert.Equal(t, model.AgentCoder, ag.AgentType)

	// quickfix_retry event fired.
	var sawRetry bool
	for {
		select {
		case evt := <-events:
			if evt.Type == "quickfix_retry" {
				sawRetry = true
			}
		default:
			goto done
		}
	}
done:
	assert.True(t, sawRetry, "expected quickfix_retry event on events channel")
}
