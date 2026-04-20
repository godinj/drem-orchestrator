package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// dockerEventsTestRig builds an Orchestrator wired to both a fake runtime
// and a fake spawner. Tests use the runtime to emit synthetic events and
// assert on spawner calls that would respawn the dead worker. The bare
// repo is real because the respawn path (handleWorkerDeath →
// spawnCoder → spawnTypedWorker) now pre-creates the feature branch via
// gitref.EnsureBranch, which needs an actual git object database.
func dockerEventsTestRig(t *testing.T) (*Orchestrator, *container.FakeRuntime, *fakeWorkerSpawner) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	bareRepo := testutil.SetupBareRepo(t)
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "docker-events-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	rt := container.NewFakeRuntime()
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: bareRepo, Default: "main"},
		logger:         slog.Default().With("component", "docker_events_test"),
		Spawner:        fake,
		Runtime:        rt,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, rt, fake
}

// seedInFlightTask creates an in_progress task with the given feature
// branch so event-driven respawn paths have something to target.
func seedInFlightTask(t *testing.T, o *Orchestrator) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Worker death test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/worker-death",
	}
	require.NoError(t, o.db.Create(task).Error)
	return task
}

func TestDispatchEvent_OOMTriggersHandleWorkerDeath(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-oom",
		ExitCode:    137,
		OOMKilled:   true,
		Labels: map[string]string{
			"drem.task_id": task.ID.String(),
		},
	}
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Len(t, fake.spawnCalls, 1, "expected respawn on OOM")
	require.Equal(t, "coder", fake.spawnCalls[0].AgentType)

	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").Find(&evts).Error)
	require.Len(t, evts, 1)
}

func TestDispatchEvent_ExitZeroNoReplacement(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-ok",
		ExitCode:    0,
		OOMKilled:   false,
		Labels: map[string]string{
			"drem.task_id": task.ID.String(),
		},
	}
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Empty(t, fake.spawnCalls, "exit 0 must not respawn")
}

func TestDispatchEvent_ReplacementCapExhaustsAfterThreeAttempts(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-dead",
		ExitCode:    137,
		Labels: map[string]string{
			"drem.task_id": task.ID.String(),
		},
	}
	for i := 0; i < replacementCap; i++ {
		o.dispatchEvent(context.Background(), ev, tracker)
	}
	require.Len(t, fake.spawnCalls, replacementCap)

	// Task must still be active — refresh first.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)

	// Fourth death must push the task into failed without spawning again.
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Len(t, fake.spawnCalls, replacementCap,
		"spawner must not be invoked beyond the replacement cap")

	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
}

func TestDispatchEvent_IgnoresEventsWithoutTaskIDLabel(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-strange",
		ExitCode:    1,
		Labels:      map[string]string{},
	}
	o.dispatchEvent(context.Background(), ev, tracker)
	require.Empty(t, fake.spawnCalls)
}

func TestWatchDockerEvents_ClosesCleanlyOnCtxCancel(t *testing.T) {
	o, _, _ := dockerEventsTestRig(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.watchDockerEvents(ctx) }()

	// Give the subscription time to register.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("watchDockerEvents did not exit within 1s")
	}
}

// Guard: dispatch path must not mutate tasks that are already terminal.
func TestDispatchEvent_SkipsTerminalTasks(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "Already done",
		Description: "x",
		Status:      model.StatusDone,
	}
	require.NoError(t, o.db.Create(task).Error)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-late",
		ExitCode:    1,
		Labels:      map[string]string{"drem.task_id": task.ID.String()},
	}
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Empty(t, fake.spawnCalls)
	// Container death audit row is still written even for terminal tasks.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").Find(&evts).Error)
	require.Len(t, evts, 1)
}

// Unused spawner result guard — ensures compile-time dependency to spawner.
var _ = spawner.ListWorkersParams{}
