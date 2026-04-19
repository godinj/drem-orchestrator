package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// fakeWorkerSpawner captures every call made against the WorkerSpawner
// interface so tests can assert on payload shape and return preconfigured
// results or errors.
type fakeWorkerSpawner struct {
	mu sync.Mutex

	spawnCalls   []spawner.SpawnWorkerParams
	spawnResults []spawnOutcome

	destroyCalls []spawner.DestroyWorkerParams
	destroyErr   error

	listResult spawner.ListWorkersResult
	listErr    error

	inspectResult spawner.InspectWorkerResult
	inspectErr    error
}

type spawnOutcome struct {
	res spawner.SpawnWorkerResult
	err error
}

func (f *fakeWorkerSpawner) SpawnWorker(_ context.Context, p spawner.SpawnWorkerParams) (spawner.SpawnWorkerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawnCalls = append(f.spawnCalls, p)
	idx := len(f.spawnCalls) - 1
	if idx >= len(f.spawnResults) {
		return spawner.SpawnWorkerResult{ContainerID: "fake-container-" + uuid.NewString()[:8]}, nil
	}
	return f.spawnResults[idx].res, f.spawnResults[idx].err
}

func (f *fakeWorkerSpawner) DestroyWorker(_ context.Context, p spawner.DestroyWorkerParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyCalls = append(f.destroyCalls, p)
	return f.destroyErr
}

func (f *fakeWorkerSpawner) ListWorkers(_ context.Context, _ spawner.ListWorkersParams) (spawner.ListWorkersResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listResult, f.listErr
}

func (f *fakeWorkerSpawner) InspectWorker(_ context.Context, _ spawner.InspectWorkerParams) (spawner.InspectWorkerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inspectResult, f.inspectErr
}

// workerSpawnTestRig builds an Orchestrator wired to a fresh in-memory DB
// and a fakeWorkerSpawner. It returns both so tests can drive spawnCoder
// directly and assert on DB state.
func workerSpawnTestRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "worker-spawn-test",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: "/tmp/fake-bare", Default: "main"},
		logger:         slog.Default().With("component", "worker_spawn_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake
}

func TestSpawnCoder_BuildsExpectedParams(t *testing.T) {
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Implement feature X",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/task-x",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-abc123"}},
	}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	require.Len(t, fake.spawnCalls, 1)
	p := fake.spawnCalls[0]
	require.Equal(t, o.projectID.String(), p.Project)
	require.Equal(t, "coder", p.AgentType)
	require.Equal(t, "feature/task-x", p.Branch)
	require.Equal(t, task.ID.String(), p.Env["DREM_TASK_ID"])
	require.Equal(t, task.ID.String(), p.Labels["drem.task_id"])
	require.Equal(t, "go", p.Labels["drem.language"])
	require.Equal(t, "/tmp/fake-bare", p.BareRepoMount)
}

func TestSpawnCoder_RecordsContainerIDAndImageOnAgent(t *testing.T) {
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-x",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-xyz"}},
	}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	// Reload task to pick up AssignedAgentID written by recordContainerOnAgent.
	require.NoError(t, o.db.First(task, "id = ?", task.ID).Error)
	require.NotNil(t, task.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", task.AssignedAgentID).Error)
	require.Equal(t, "container-xyz", ag.TmuxSession)
	// ModelID carries the image when the spawner returned one; with the
	// default fake path Image is empty string, so the field is left blank.
	require.Equal(t, model.AgentCoder, ag.AgentType)
}

func TestSpawnCoder_OnSpawnFailureReturnsError(t *testing.T) {
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-y",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{{err: errors.New("docker daemon refused")}}

	err := o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.Contains(t, err.Error(), "docker daemon refused")

	// Task status must be unchanged; spawnCoder is a side-effect primitive
	// that never mutates the state machine on failure.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)
	require.Nil(t, reloaded.AssignedAgentID)

	// A worker_spawn_failed audit event must exist (user story 49).
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
}

func TestSpawnCoder_RegistersBranchInGitref(t *testing.T) {
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/register-branch",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "c1"}}}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	ref, err := o.GitrefRegistry.FindByBranch(context.Background(), "/tmp/fake-bare", "feature/register-branch")
	require.NoError(t, err)
	require.Equal(t, gitref.StatusActive, ref.Status)
	require.Equal(t, task.ID.String(), ref.TaskID)
	require.Equal(t, "coder", ref.AgentType)
}

func TestSpawnCoder_WithoutSpawnerReturnsError(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	o := &Orchestrator{
		db:        db,
		projectID: uuid.New(),
		events:    make(chan Event, 8),
		logger:    slog.Default(),
	}
	err := o.spawnCoder(context.Background(), &model.Task{ID: uuid.New()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no WorkerSpawner configured")
}
