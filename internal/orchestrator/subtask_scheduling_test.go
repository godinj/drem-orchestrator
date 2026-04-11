package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
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
