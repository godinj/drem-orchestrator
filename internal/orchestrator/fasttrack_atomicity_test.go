package orchestrator

import (
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestOnAgentCompleted_FastTrackAtomicity verifies that a successful agent
// completion fast-tracks the subtask to DONE and persists a corresponding
// status_change event, i.e. status transition and event creation are visible
// together in the DB after onAgentCompleted returns.
func TestOnAgentCompleted_FastTrackAtomicity(t *testing.T) {
	db := testutil.NewSharedTestDB(t)
	wt := &FakeWorktreeManager{BarePath: "/tmp/fake", Default: "main"}
	o := testOrchestratorWithRunner(t, db, wt)

	project := model.Project{ID: o.projectID, Name: "test", BareRepoPath: "/tmp/fake"}
	db.Create(&project)

	parentID := uuid.New()
	parent := model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "parent",
		Description: "parent in in_progress",
		Status:      model.StatusInProgress,
	}
	db.Create(&parent)

	subID := uuid.New()
	agentID := uuid.New()
	subtask := model.Task{
		ID:              subID,
		ProjectID:       o.projectID,
		ParentTaskID:    &parentID,
		Title:           "subtask",
		Description:     "impl subtask",
		Status:          model.StatusInProgress,
		Phase:           "implementation",
		AssignedAgentID: &agentID,
	}
	db.Create(&subtask)

	ag := model.Agent{
		ID:            agentID,
		ProjectID:     o.projectID,
		AgentType:     model.AgentCoder,
		Name:          "test-coder",
		Status:        model.AgentWorking,
		CurrentTaskID: &subID,
	}
	db.Create(&ag)

	if err := o.onAgentCompleted(&ag, &subtask); err != nil {
		t.Fatalf("onAgentCompleted: unexpected error: %v", err)
	}

	var updated model.Task
	if err := db.First(&updated, "id = ?", subID).Error; err != nil {
		t.Fatalf("load subtask: %v", err)
	}
	if updated.Status != model.StatusDone {
		t.Fatalf("expected subtask status %q, got %q", model.StatusDone, updated.Status)
	}

	var events int64
	db.Model(&model.TaskEvent{}).
		Where("task_id = ? AND event_type = ?", subID, "status_change").
		Count(&events)
	if events < 1 {
		t.Fatalf("expected at least one status_change event for subtask, got %d", events)
	}
}
