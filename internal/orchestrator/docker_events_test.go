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

func seedAssignedWorkerAttempt(t *testing.T, o *Orchestrator, task *model.Task, agentType model.AgentType, workerID, containerID string) *model.WorkerAttempt {
	t.Helper()
	agentID := uuid.New()
	ag := &model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      agentType,
		Name:           string(agentType) + "-death-test",
		Status:         model.AgentWorking,
		CurrentTaskID:  &task.ID,
		TmuxSession:    containerID,
		WorktreeBranch: task.WorktreeBranch,
	}
	require.NoError(t, o.db.Create(ag).Error)
	task.AssignedAgentID = &agentID
	require.NoError(t, o.db.Save(task).Error)
	attempt := &model.WorkerAttempt{
		ID:          uuid.New(),
		TaskID:      task.ID,
		AgentID:     &agentID,
		WorkerID:    workerID,
		ContainerID: containerID,
		AgentType:   string(agentType),
		State:       model.WorkerAttemptRunning,
	}
	require.NoError(t, o.db.Create(attempt).Error)
	return attempt
}

func currentAssignedWorkerAttempt(t *testing.T, o *Orchestrator, taskID uuid.UUID) model.WorkerAttempt {
	t.Helper()
	var task model.Task
	require.NoError(t, o.db.First(&task, "id = ?", taskID).Error)
	require.NotNil(t, task.AssignedAgentID)
	var attempt model.WorkerAttempt
	require.NoError(t, o.db.Where("task_id = ? AND agent_id = ? AND completed_at IS NULL", taskID, *task.AssignedAgentID).
		Order("created_at DESC").First(&attempt).Error)
	return attempt
}

func workerDeathEvent(taskID uuid.UUID, attempt model.WorkerAttempt, exitCode int, oom bool) container.Event {
	return container.Event{
		Type:        container.EventDie,
		ContainerID: attempt.ContainerID,
		ExitCode:    exitCode,
		OOMKilled:   oom,
		Labels: map[string]string{
			"drem.task_id":    taskID.String(),
			"drem.worker_id":  attempt.WorkerID,
			"drem.agent_type": attempt.AgentType,
		},
	}
}

func TestDispatchEvent_OOMTriggersHandleWorkerDeath(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-oom", "c-oom")

	tracker := newReplacementTracker()
	ev := workerDeathEvent(task.ID, *attempt, 137, true)
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
	seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-dead-0", "c-dead-0")

	tracker := newReplacementTracker()
	for i := 0; i < replacementCap; i++ {
		attempt := currentAssignedWorkerAttempt(t, o, task.ID)
		ev := workerDeathEvent(task.ID, attempt, 137, false)
		o.dispatchEvent(context.Background(), ev, tracker)
	}
	require.Len(t, fake.spawnCalls, replacementCap)

	// Task must still be active — refresh first.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)

	// Fourth death must push the task into failed without spawning again.
	attempt := currentAssignedWorkerAttempt(t, o, task.ID)
	ev := workerDeathEvent(task.ID, attempt, 137, false)
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Len(t, fake.spawnCalls, replacementCap,
		"spawner must not be invoked beyond the replacement cap")

	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
}

func TestDispatchEvent_StaleDeathDoesNotKillCurrentAssignedWorker(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	oldAttempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-old", "c-old")
	oldAgentID := *oldAttempt.AgentID
	now := time.Now()
	oldAttempt.State = model.WorkerAttemptFailed
	oldAttempt.CompletedAt = &now
	require.NoError(t, o.db.Save(oldAttempt).Error)
	require.NoError(t, o.db.Model(&model.Agent{}).Where("id = ?", oldAgentID).Updates(map[string]any{
		"status":          model.AgentDead,
		"current_task_id": nil,
		"completed_at":    now,
	}).Error)
	current := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-current", "c-current")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *oldAttempt, 1, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Equal(t, *current.AgentID, *reloaded.AssignedAgentID)
	var currentAgent model.Agent
	require.NoError(t, o.db.First(&currentAgent, "id = ?", *current.AgentID).Error)
	require.Equal(t, model.AgentWorking, currentAgent.Status)
}

func TestDispatchEvent_ReviewerDeathRespawnsReviewer(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	task.Status = model.StatusTestingReady
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentReviewer, "worker-reviewer", "c-reviewer")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 1, false), newReplacementTracker())

	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, string(model.AgentReviewer), fake.spawnCalls[0].AgentType)
}

func TestDispatchEvent_FixerDeathRespawnsFixerInProgress(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentFixer, "worker-fixer", "c-fixer")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 1, false), newReplacementTracker())

	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, string(model.AgentFixer), fake.spawnCalls[0].AgentType)
}

func TestDispatchEvent_UnmatchedDeathAuditsWithoutRespawnOrClear(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	current := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-current", "c-current")

	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-unknown",
		ExitCode:    1,
		Labels: map[string]string{
			"drem.task_id":   task.ID.String(),
			"drem.worker_id": "worker-unknown",
		},
	}
	o.dispatchEvent(context.Background(), ev, newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Equal(t, *current.AgentID, *reloaded.AssignedAgentID)
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").Find(&evts).Error)
	require.Len(t, evts, 1)
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
