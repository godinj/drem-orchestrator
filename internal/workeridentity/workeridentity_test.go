package workeridentity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestFromAgentClassifiesLegacyTmuxAndContainers(t *testing.T) {
	require.True(t, FromAgent(model.Agent{TmuxSession: "dashboard/coder - Fix bug abcd"}).CanJumpToTmux())
	require.True(t, FromAgent(model.Agent{TmuxSession: "session:abc"}).CanJumpToTmux())
	require.True(t, FromAgent(model.Agent{TmuxSession: "foo bar"}).CanJumpToTmux())
	require.True(t, FromAgent(model.Agent{TmuxSession: "abc123def456"}).HasContainer())
	require.True(t, FromAgent(model.Agent{TmuxSession: "c7a3b2f1-5d9e-4e8a-b3f5-2e7f0a1d2c3b"}).HasContainer())
}

func TestRecordSpawnCreatesAgentAttemptAndHandle(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	h, err := NewStore(db).RecordSpawn(context.Background(), SpawnRecord{
		Task:        &task,
		ProjectID:   projectID,
		AgentType:   "coder",
		WorkerID:    "worker-1",
		ContainerID: "container-1",
		Image:       "worker:latest",
		Branch:      "feature/task",
		Provider:    "claude",
		ModelID:     "sonnet",
		Effort:      "high",
		Now:         now,
	})
	require.NoError(t, err)
	require.True(t, h.HasContainer())
	require.Equal(t, "container-1", h.ContainerID)
	require.NotEqual(t, uuid.Nil, h.AgentID)
	require.NotEqual(t, uuid.Nil, h.AttemptID)

	var ag model.Agent
	require.NoError(t, db.First(&ag, "id = ?", h.AgentID).Error)
	require.Equal(t, "container-1", ag.TmuxSession)
	require.Equal(t, "feature/task", ag.WorktreeBranch)
	require.Equal(t, task.ID, *ag.CurrentTaskID)
	require.Equal(t, h.AgentID, *task.AssignedAgentID)

	var attempt model.WorkerAttempt
	require.NoError(t, db.First(&attempt, "id = ?", h.AttemptID).Error)
	require.Equal(t, h.AgentID, *attempt.AgentID)
	require.Equal(t, "worker-1", attempt.WorkerID)
	require.Equal(t, "container-1", attempt.ContainerID)
	require.Equal(t, "feature/task", attempt.Branch)
	require.Equal(t, model.WorkerAttemptRunning, attempt.State)
	require.Equal(t, "worker-1", attempt.LeaseOwner)
	require.NotNil(t, attempt.LeaseExpiresAt)
	require.Equal(t, now.Add(DefaultLeaseTTL), attempt.LeaseExpiresAt.UTC())
}

func TestReserveSpawn_SecondReservationForSameTaskAndRoleFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)

	store := NewStore(db)
	res, err := store.ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-1",
		Image:     "worker:latest",
		Branch:    "feature/task",
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, res.AgentID)

	_, err = store.ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-2",
		Image:     "worker:latest",
		Branch:    "feature/task",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTaskAlreadyClaimed))
}

func TestReserveSpawn_ConcurrentReservationsKeepOneActiveAttempt(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)

	store := NewStore(db)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, workerID := range []string{"worker-1", "worker-2"} {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			<-start
			_, err := store.ReserveSpawn(context.Background(), SpawnRecord{
				Task:      &task,
				ProjectID: projectID,
				AgentType: "coder",
				WorkerID:  workerID,
				Image:     "worker:latest",
				Branch:    "feature/task",
			})
			results <- err
		}(workerID)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	claimed := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, ErrTaskAlreadyClaimed)
		claimed++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, claimed)

	var activeAttempts int64
	require.NoError(t, db.Model(&model.WorkerAttempt{}).
		Where("task_id = ? AND agent_type = ? AND branch = ? AND completed_at IS NULL", task.ID, "coder", "feature/task").
		Count(&activeAttempts).Error)
	if activeAttempts != 1 {
		var attempts []model.WorkerAttempt
		require.NoError(t, db.Where("task_id = ?", task.ID).Order("created_at").Find(&attempts).Error)
		t.Logf("reservation attempts after concurrent claim: %+v", attempts)
	}
	require.Equal(t, int64(1), activeAttempts)

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
}

func TestReserveFinalizeSpawnCreatesRunningHandle(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)

	store := NewStore(db)
	res, err := store.ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-1",
		Image:     "worker:latest",
		Branch:    "feature/task",
		Provider:  "opencode",
		ModelID:   "gpt-5.5",
		Effort:    "high",
	})
	require.NoError(t, err)
	require.NotNil(t, task.AssignedAgentID)
	require.Equal(t, res.AgentID, *task.AssignedAgentID)

	h, err := store.FinalizeSpawn(context.Background(), res, "container-1")
	require.NoError(t, err)
	require.True(t, h.HasContainer())
	require.Equal(t, res.AttemptID, h.AttemptID)
	require.Equal(t, "container-1", h.ContainerID)

	var ag model.Agent
	require.NoError(t, db.First(&ag, "id = ?", res.AgentID).Error)
	require.Equal(t, "container-1", ag.TmuxSession)
	require.Equal(t, "opencode", ag.Provider)
	require.Equal(t, "gpt-5.5", ag.ModelID)
	require.Equal(t, "high", ag.Effort)

	var attempt model.WorkerAttempt
	require.NoError(t, db.First(&attempt, "id = ?", res.AttemptID).Error)
	require.Equal(t, model.WorkerAttemptRunning, attempt.State)
	require.Nil(t, attempt.CompletedAt)
	require.Equal(t, "container-1", attempt.ContainerID)
	require.Equal(t, "worker-1", attempt.LeaseOwner)
	require.NotNil(t, attempt.LeaseExpiresAt)
}

func TestRenewLeaseFencesOwnerAndExpiredAttempts(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	store := NewStore(db)
	res, err := store.ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-1",
		Image:     "worker:latest",
		Branch:    "feature/task",
		Now:       now,
	})
	require.NoError(t, err)

	renewedAt := now.Add(30 * time.Second)
	require.NoError(t, store.RenewLease(context.Background(), res.AttemptID, "worker-1", time.Minute, renewedAt))
	var attempt model.WorkerAttempt
	require.NoError(t, db.First(&attempt, "id = ?", res.AttemptID).Error)
	require.NotNil(t, attempt.LeaseExpiresAt)
	require.Equal(t, renewedAt.Add(time.Minute), attempt.LeaseExpiresAt.UTC())

	err = store.RenewLease(context.Background(), res.AttemptID, "worker-2", time.Minute, renewedAt.Add(time.Second))
	require.ErrorIs(t, err, ErrLeaseConflict)

	err = store.RenewLease(context.Background(), res.AttemptID, "worker-1", time.Minute, renewedAt.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrLeaseExpired)
}

func TestFinishAttemptFencesOwnerAndMarksTerminal(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	store := NewStore(db)
	res, err := store.ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-1",
		Image:     "worker:latest",
		Branch:    "feature/task",
		Now:       now,
	})
	require.NoError(t, err)

	finishAt := now.Add(time.Minute)
	err = store.FinishAttempt(context.Background(), res.AttemptID, "worker-2", model.WorkerAttemptFailed, "wrong owner", finishAt)
	require.ErrorIs(t, err, ErrLeaseConflict)

	require.NoError(t, store.FinishAttempt(context.Background(), res.AttemptID, "worker-1", model.WorkerAttemptFailed, "tool loop", finishAt))
	var attempt model.WorkerAttempt
	require.NoError(t, db.First(&attempt, "id = ?", res.AttemptID).Error)
	require.Equal(t, model.WorkerAttemptFailed, attempt.State)
	require.NotNil(t, attempt.CompletedAt)
	require.NotNil(t, attempt.FailedAt)
	require.Equal(t, "tool loop", attempt.FirstError)

	err = store.RenewLease(context.Background(), res.AttemptID, "worker-1", time.Minute, finishAt.Add(time.Second))
	require.ErrorIs(t, err, ErrAttemptTerminal)
}

func TestWorkerAttempt_ActiveUniqueIndexPreventsDuplicateTaskRoleBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskID := uuid.New()
	require.NoError(t, db.Create(&model.Task{ID: taskID, ProjectID: uuid.New(), Title: "t", Description: "d"}).Error)

	require.NoError(t, db.Create(&model.WorkerAttempt{
		ID:        uuid.New(),
		TaskID:    taskID,
		WorkerID:  "worker-1",
		AgentType: "coder",
		Branch:    "feature/a",
		State:     model.WorkerAttemptReserved,
	}).Error)
	err := db.Create(&model.WorkerAttempt{
		ID:        uuid.New(),
		TaskID:    taskID,
		WorkerID:  "worker-2",
		AgentType: "coder",
		Branch:    "feature/a",
		State:     model.WorkerAttemptReserved,
	}).Error
	require.Error(t, err)

	require.NoError(t, db.Create(&model.WorkerAttempt{
		ID:        uuid.New(),
		TaskID:    taskID,
		WorkerID:  "worker-3",
		AgentType: "coder",
		Branch:    "feature/b",
		State:     model.WorkerAttemptReserved,
	}).Error)
}

func TestReserveSpawn_SupersedesUnassignedActiveAttemptForSameBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress, WorktreeBranch: "feature/task"}
	require.NoError(t, db.Create(&task).Error)

	stale := model.WorkerAttempt{
		ID:        uuid.New(),
		TaskID:    task.ID,
		WorkerID:  "worker-stale",
		AgentType: "coder",
		Branch:    "feature/task",
		State:     model.WorkerAttemptRunning,
	}
	require.NoError(t, db.Create(&stale).Error)

	res, err := NewStore(db).ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-current",
		Branch:    "feature/task",
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, res.AttemptID)

	require.NoError(t, db.First(&stale, "id = ?", stale.ID).Error)
	require.Equal(t, model.WorkerAttemptSuperseded, stale.State)
	require.NotNil(t, stale.CompletedAt)
}

func TestAbortReservation_MarksAttemptAbortedAndClearsOnlyMatchingAssignment(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := model.Task{ID: uuid.New(), ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress}
	require.NoError(t, db.Create(&task).Error)

	store := NewStore(db)
	res, err := store.ReserveSpawn(context.Background(), SpawnRecord{
		Task:      &task,
		ProjectID: projectID,
		AgentType: "coder",
		WorkerID:  "worker-1",
		Branch:    "feature/task",
	})
	require.NoError(t, err)

	require.NoError(t, store.AbortReservation(context.Background(), res, "spawn_failed"))

	var attempt model.WorkerAttempt
	require.NoError(t, db.First(&attempt, "id = ?", res.AttemptID).Error)
	require.Equal(t, model.WorkerAttemptAborted, attempt.State)
	require.NotNil(t, attempt.CompletedAt)

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", task.ID).Error)
	require.Nil(t, reloaded.AssignedAgentID)
}

func TestAbortReservation_StaleAttemptCannotClearCurrentAssignment(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	taskID := uuid.New()
	currentAgentID := uuid.New()
	staleAgentID := uuid.New()
	task := model.Task{ID: taskID, ProjectID: projectID, Title: "Fix bug", Description: "desc", Status: model.StatusInProgress, AssignedAgentID: &currentAgentID}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&model.Agent{ID: currentAgentID, ProjectID: projectID, AgentType: model.AgentCoder, Name: "current", CurrentTaskID: &taskID}).Error)
	require.NoError(t, db.Create(&model.Agent{ID: staleAgentID, ProjectID: projectID, AgentType: model.AgentCoder, Name: "stale", CurrentTaskID: &taskID}).Error)
	completedAt := time.Now()
	attemptID := uuid.New()
	require.NoError(t, db.Create(&model.WorkerAttempt{
		ID:          attemptID,
		TaskID:      taskID,
		AgentID:     &staleAgentID,
		WorkerID:    "worker-stale",
		AgentType:   "coder",
		Branch:      "feature/task",
		State:       model.WorkerAttemptSuperseded,
		CompletedAt: &completedAt,
	}).Error)

	err := NewStore(db).AbortReservation(context.Background(), Reservation{TaskID: taskID, AgentID: staleAgentID, AttemptID: attemptID}, "stale")
	require.NoError(t, err)

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", taskID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Equal(t, currentAgentID, *reloaded.AssignedAgentID)
}

func TestForTaskReturnsEmptyHandleForUnassignedTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	task := model.Task{ID: uuid.New(), ProjectID: uuid.New(), Title: "Backlog", Description: "desc", Status: model.StatusBacklog}
	h, err := NewStore(db).ForTask(context.Background(), &task)
	require.NoError(t, err)
	require.Equal(t, task.ID, h.TaskID)
	require.False(t, h.HasContainer())
	require.False(t, h.CanJumpToTmux())
}
