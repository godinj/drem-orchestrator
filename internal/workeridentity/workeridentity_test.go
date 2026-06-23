package workeridentity

import (
	"context"
	"errors"
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
	require.Equal(t, model.WorkerAttemptRunning, attempt.State)
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
}

func TestWorkerAttempt_ActiveUniqueIndexPreventsDuplicateTaskRole(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskID := uuid.New()
	require.NoError(t, db.Create(&model.Task{ID: taskID, ProjectID: uuid.New(), Title: "t", Description: "d"}).Error)

	require.NoError(t, db.Create(&model.WorkerAttempt{
		ID:        uuid.New(),
		TaskID:    taskID,
		WorkerID:  "worker-1",
		AgentType: "coder",
		State:     model.WorkerAttemptReserved,
	}).Error)
	err := db.Create(&model.WorkerAttempt{
		ID:        uuid.New(),
		TaskID:    taskID,
		WorkerID:  "worker-2",
		AgentType: "coder",
		State:     model.WorkerAttemptReserved,
	}).Error
	require.Error(t, err)
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
