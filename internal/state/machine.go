// Package state implements the task status state machine for the Drem
// Orchestrator, defining valid transitions and providing helpers to
// validate and execute status changes.
package state

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ValidTransitions defines which status transitions are allowed. Each key
// maps to the set of statuses a task may transition to from that state.
var ValidTransitions = map[model.TaskStatus][]model.TaskStatus{
	model.StatusBacklog:       {model.StatusPlanning, model.StatusPaused},
	model.StatusPlanning:      {model.StatusPlanReview, model.StatusFailed, model.StatusPaused},
	model.StatusPlanReview:    {model.StatusInProgress, model.StatusPlanning},
	model.StatusInProgress:    {model.StatusTestingReady, model.StatusFailed, model.StatusPaused},
	model.StatusTestingReady: {model.StatusMerging, model.StatusInProgress, model.StatusPlanning},
	model.StatusMerging:       {model.StatusDone, model.StatusFailed},
	model.StatusPaused:        {model.StatusBacklog, model.StatusPlanning, model.StatusInProgress},
	model.StatusDone:          {},
	model.StatusFailed:        {model.StatusBacklog, model.StatusInProgress},
}

// ValidateTransition checks if moving from current to target is an allowed
// transition. Returns nil on success, or an error describing why the
// transition is invalid.
func ValidateTransition(current, target model.TaskStatus) error {
	allowed, ok := ValidTransitions[current]
	if !ok {
		return fmt.Errorf("unknown current status: %q", current)
	}
	for _, s := range allowed {
		if s == target {
			return nil
		}
	}
	return fmt.Errorf(
		"invalid transition from %q to %q; valid targets: %v",
		current, target, allowed,
	)
}

// GetAvailableTransitions returns the list of valid next statuses for a
// given current status. Returns nil if the status is unknown or terminal.
func GetAvailableTransitions(current model.TaskStatus) []model.TaskStatus {
	return ValidTransitions[current]
}

// IsHumanGate returns true if the given status requires human approval
// before the task can proceed.
func IsHumanGate(status model.TaskStatus) bool {
	return status.IsHumanGate()
}

// TransitionTask validates the transition from task.Status to target,
// updates the task status and UpdatedAt timestamp, and returns a new
// TaskEvent recording the change. The caller is responsible for persisting
// both the task and the event to the database.
//
// Returns an error if the transition is not valid.
func TransitionTask(task *model.Task, target model.TaskStatus, actor string, details map[string]any) (*model.TaskEvent, error) {
	if err := ValidateTransition(task.Status, target); err != nil {
		return nil, err
	}

	now := time.Now()
	oldStatus := task.Status

	task.Status = target
	task.UpdatedAt = now

	event := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "status_change",
		OldValue:  string(oldStatus),
		NewValue:  string(target),
		Details:   model.JSONField(details),
		Actor:     actor,
		CreatedAt: now,
	}

	return event, nil
}
