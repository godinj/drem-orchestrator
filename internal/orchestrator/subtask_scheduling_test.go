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

// TestScheduleSubtasks_SkipsWhenNoCandidates verifies that scheduleSubtasks
// returns no error and makes no state changes when the parent has no backlog
// subtasks.
func TestScheduleSubtasks_SkipsWhenNoCandidates(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "test-project", "/tmp/bare", "master")

	parent := testutil.CreateTask(t, db, project.ID, "parent task", model.StatusInProgress)

	events := make(chan Event, 100)
	o := &Orchestrator{
		db:        db,
		projectID: project.ID,
		logger:    slog.Default(),
		events:    events,
	}

	err := o.scheduleSubtasks(&parent)
	require.NoError(t, err)

	// No events should have been emitted.
	assert.Empty(t, events)
}

// TestDispatchPendingSubtasks_SkipsTerminalParents verifies that
// dispatchPendingSubtasks does not dispatch subtasks whose parent is in a
// terminal state (done/failed/rejected).
func TestDispatchPendingSubtasks_SkipsTerminalParents(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "test-project", "/tmp/bare", "master")

	parent := testutil.CreateTask(t, db, project.ID, "done parent", model.StatusDone)

	// Create a backlog subtask under the terminal parent.
	parentID := parent.ID
	subtask := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		ParentTaskID: &parentID,
		Title:        "child subtask",
		Description:  "child subtask",
		Status:       model.StatusBacklog,
	}
	require.NoError(t, db.Create(&subtask).Error)

	events := make(chan Event, 100)
	o := &Orchestrator{
		db:        db,
		projectID: project.ID,
		logger:    slog.Default(),
		events:    events,
	}

	o.dispatchPendingSubtasks()

	// Subtask must still be in backlog — terminal parent means no dispatch.
	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", subtask.ID).Error)
	assert.Equal(t, model.StatusBacklog, reloaded.Status)
}

func TestDispatchPendingSubtasks_SkipsHumanGateParents(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "test-project", "/tmp/bare", "master")

	for _, status := range []model.TaskStatus{
		model.StatusPlanReview,
		model.StatusTestReview,
		model.StatusTestingReady,
		model.StatusNeedsClarification,
	} {
		t.Run(status.String(), func(t *testing.T) {
			parent := testutil.CreateTask(t, db, project.ID, "gated parent", status)

			parentID := parent.ID
			subtask := model.Task{
				ID:           uuid.New(),
				ProjectID:    project.ID,
				ParentTaskID: &parentID,
				Title:        "child subtask",
				Description:  "child subtask",
				Status:       model.StatusBacklog,
			}
			require.NoError(t, db.Create(&subtask).Error)

			o := &Orchestrator{
				db:        db,
				projectID: project.ID,
				logger:    slog.Default(),
				events:    make(chan Event, 100),
			}

			o.dispatchPendingSubtasks()

			var reloaded model.Task
			require.NoError(t, db.First(&reloaded, "id = ?", subtask.ID).Error)
			assert.Equal(t, model.StatusBacklog, reloaded.Status)
		})
	}
}

// TestOrderCandidatesByExperimentPriority_NilScheduler verifies that when
// experimentScheduler is nil the candidates are returned unchanged.
func TestOrderCandidatesByExperimentPriority_NilScheduler(t *testing.T) {
	o := &Orchestrator{
		experimentScheduler: nil,
	}

	candidates := []model.Task{
		{ID: uuid.New(), Title: "alpha"},
		{ID: uuid.New(), Title: "beta"},
		{ID: uuid.New(), Title: "gamma"},
	}

	result := o.orderCandidatesByExperimentPriority(candidates)

	require.Len(t, result, 3)
	assert.Equal(t, candidates[0].ID, result[0].ID)
	assert.Equal(t, candidates[1].ID, result[1].ID)
	assert.Equal(t, candidates[2].ID, result[2].ID)
}

// newContainerSubtaskRig builds an Orchestrator wired to a fresh in-memory
// DB plus a fakeWorkerSpawner so tests can drive scheduleSubtasks through
// the container-mode dispatch path (o.Spawner != nil). Mirrors the
// worker_spawn_test.go workerSpawnTestRig, adapted for subtask scheduling
// (includes FakeWorktreeManager for feature-dir lookup and a large
// events-channel buffer so subtask_scheduled emits do not block).
//
// The bare repo is a real `git init --bare` output because
// spawnTypedWorker now pre-creates the subtask branch via
// gitref.EnsureBranch — a /tmp/fake-bare literal would fail the
// branch-ensure step on every dispatch. Callers that seed a parent
// task with a non-default WorktreeBranch should call
// pushTestFeatureBranch(t, bareRepo, parentBranch) so EnsureBranch can
// fork off it.
//
// See plans/phase-3.5-subtask-dispatch-migration.md §"Test strategy"
// and plans/orch-container-subtask-branch-provisioning.md.
func newContainerSubtaskRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner, model.Project, chan Event, string) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	bareRepo := testutil.SetupBareRepo(t)
	project := testutil.CreateProject(t, db, "phase35-test-project", bareRepo, "main")

	fake := &fakeWorkerSpawner{}
	events := make(chan Event, 100)
	o := &Orchestrator{
		db:             db,
		projectID:      project.ID,
		projectName:    project.Name,
		events:         events,
		worktree:       &FakeWorktreeManager{BarePath: bareRepo, Default: "main"},
		logger:         slog.Default().With("component", "subtask_scheduling_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake, project, events, bareRepo
}

// TestScheduleSubtasks_DispatchesCoderViaSpawner is the primary
// acceptance test for the Phase 3.5 subtask-dispatch migration: when
// o.Spawner is wired, a backlog coder subtask whose dependencies are
// met routes through o.spawnTypedWorker → o.Spawner.SpawnWorker,
// records an Agent row carrying the container ID in TmuxSession,
// fast-tracks to IN_PROGRESS, and emits a subtask_scheduled event.
// No legacy runner.SpawnAgent call happens; the runner field is nil.
//
// See plans/phase-3.5-subtask-dispatch-migration.md §"Test strategy"
// test (1).
func TestScheduleSubtasks_DispatchesCoderViaSpawner(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, events, bareRepo := newContainerSubtaskRig(t)

	// Seed the parent's feature branch so spawnTypedWorker's
	// EnsureBranch step can fork the subtask off it.
	pushTestFeatureBranch(t, bareRepo, "feature/phase35-coder")

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "parent with container-mode dispatch",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/phase35-coder",
	}
	require.NoError(t, o.db.Create(&parent).Error)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		ParentTaskID: &parent.ID,
		Title:        "implement thing",
		Description:  "implement thing end-to-end",
		Status:       model.StatusBacklog,
		Context:      model.JSONField{"agent_type": "coder"},
	}
	require.NoError(t, o.db.Create(&sub).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-coder-phase35"}},
	}

	require.NoError(t, o.scheduleSubtasks(&parent))

	// SpawnWorker was called with coder params. Dual-label contract
	// (plans/dual-label-worker-spawn.md): Project is the name;
	// ProjectID is the UUID; both land on container labels.
	require.Len(t, fake.spawnCalls, 1)
	p := fake.spawnCalls[0]
	assert.Equal(t, project.Name, p.Project)
	assert.Equal(t, project.ID.String(), p.ProjectID)
	assert.Equal(t, "coder", p.AgentType)
	assert.Equal(t, sub.ID.String(), p.Env["DREM_TASK_ID"])
	assert.Equal(t, "/host/.claude/.credentials.json", p.CredsMount)
	assert.NotContains(t, p.Env, "ANTHROPIC_API_KEY")

	// Subtask was reloaded and assigned to an Agent whose TmuxSession
	// carries the container ID (the container-mode handle convention
	// documented in reconcile_containers.go).
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", sub.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	assert.Equal(t, model.StatusInProgress, reloaded.Status,
		"fast-track transitions must land on IN_PROGRESS")

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", reloaded.AssignedAgentID).Error)
	assert.Equal(t, "container-coder-phase35", ag.TmuxSession)
	assert.Equal(t, model.AgentCoder, ag.AgentType)

	// worker_spawned audit event landed on the subtask.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?",
		sub.ID, "worker_spawned").Find(&evts).Error)
	require.Len(t, evts, 1)
	assert.Equal(t, "coder", evts[0].NewValue)

	// subtask_scheduled event fired on the orchestrator events channel.
	select {
	case evt := <-events:
		assert.Equal(t, "subtask_scheduled", evt.Type)
	default:
		t.Fatal("expected subtask_scheduled event on events channel")
	}
}

// TestScheduleSubtasks_SpawnerFailureFailsFast verifies that when the
// container spawner returns an error, the subtask stays in BACKLOG,
// no Agent assignment is written, and scheduleSubtasks returns nil
// (per-subtask failures are local and must not starve siblings).
// A worker_spawn_failed audit event must exist carrying the error.
//
// See plans/phase-3.5-subtask-dispatch-migration.md §"Test strategy"
// test (2).
func TestScheduleSubtasks_SpawnerFailureFailsFast(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, _, bareRepo := newContainerSubtaskRig(t)

	// Seed the parent's feature branch so spawnTypedWorker's
	// EnsureBranch step succeeds and the test actually reaches the
	// spawner call it wants to assert on.
	pushTestFeatureBranch(t, bareRepo, "feature/phase35-refused")

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "parent whose spawner refuses",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/phase35-refused",
	}
	require.NoError(t, o.db.Create(&parent).Error)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		ParentTaskID: &parent.ID,
		Title:        "doomed subtask",
		Description:  "doomed subtask",
		Status:       model.StatusBacklog,
		Context:      model.JSONField{"agent_type": "coder"},
	}
	require.NoError(t, o.db.Create(&sub).Error)

	fake.spawnResults = []spawnOutcome{
		{err: assertError("docker daemon refused")},
	}

	err := o.scheduleSubtasks(&parent)
	require.NoError(t, err, "per-subtask failures must not hoist to the scheduler caller")

	// Subtask is untouched — still in BACKLOG and unassigned.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", sub.ID).Error)
	assert.Equal(t, model.StatusBacklog, reloaded.Status,
		"failed spawn must not advance task status")
	assert.Nil(t, reloaded.AssignedAgentID,
		"failed spawn must not bind an AssignedAgentID")

	// worker_spawn_failed audit row must exist on the subtask.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?",
		sub.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
	assert.Equal(t, "coder", evts[0].NewValue)
	assert.Contains(t, evts[0].Details["error"], "docker daemon refused")
}

// TestScheduleSubtasks_WithoutSpawnerSkipsDispatch verifies that the
// scheduler tolerates both o.Spawner == nil AND o.runner == nil — it
// returns nil and makes no state changes. This documents the contract
// for a future decomposition where the orchestrator might load before
// any dispatch surface is wired: dispatch is effectively a no-op
// rather than a crash.
//
// See plans/phase-3.5-subtask-dispatch-migration.md §"Test strategy"
// test (3).
func TestScheduleSubtasks_WithoutSpawnerSkipsDispatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "no-dispatch-project", "/tmp/fake-bare", "main")

	parent := model.Task{
		ID:             uuid.New(),
		ProjectID:      project.ID,
		Title:          "parent without any dispatch",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/no-dispatch",
	}
	require.NoError(t, db.Create(&parent).Error)

	sub := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		ParentTaskID: &parent.ID,
		Title:        "unreachable subtask",
		Description:  "unreachable subtask",
		Status:       model.StatusBacklog,
		Context:      model.JSONField{"agent_type": "coder"},
	}
	require.NoError(t, db.Create(&sub).Error)

	events := make(chan Event, 16)
	o := &Orchestrator{
		db:        db,
		projectID: project.ID,
		worktree:  &FakeWorktreeManager{BarePath: "/tmp/fake-bare", Default: "main"},
		logger:    slog.Default(),
		events:    events,
		// Both Spawner and runner are nil — the legacy path breaks at
		// the runner==nil capacity gate and the container path is
		// skipped because Spawner==nil.
	}

	require.NoError(t, o.scheduleSubtasks(&parent))

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", sub.ID).Error)
	assert.Equal(t, model.StatusBacklog, reloaded.Status)
	assert.Nil(t, reloaded.AssignedAgentID)

	// No events should have been emitted.
	assert.Empty(t, events)
}

// assertError is a tiny helper that returns an error with a given
// message. Kept local to this test file because subtask_scheduling_test
// is the only consumer; errors.New from the stdlib would work but this
// avoids adding another import for a single call.
func assertError(msg string) error { return &simpleErr{msg: msg} }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
