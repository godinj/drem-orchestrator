// Package state implements the task status state machine for the Drem
// Orchestrator, defining valid transitions and providing helpers to
// validate and execute status changes.
package state

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Evidence is the canonical explanation attached to guarded state-machine
// transitions. It is intentionally data-only so guards can be unit-tested
// without Docker, Git, or HTTP plumbing.
type Evidence struct {
	TaskID           uuid.UUID
	AttemptID        uuid.UUID
	Actor            string
	Source           string
	Reason           string
	NormalizedReason string
	Timestamp        time.Time
	References       map[string]any
}

// TransitionRequest carries the caller's observed state plus the evidence that
// authorizes a transition. ObservedUpdatedAt is optional because older callers
// may only have status-level freshness; when present it acts as a lightweight
// compare-and-swap guard.
type TransitionRequest struct {
	Target            model.TaskStatus
	Actor             string
	ExpectedStatus    model.TaskStatus
	ObservedUpdatedAt *time.Time
	Evidence          Evidence
}

// ErrMissingEvidence marks a guarded transition request that lacks the minimum
// durable evidence fields required to explain why the state machine moved.
var ErrMissingEvidence = errors.New("missing transition evidence")

// ValidTransitions defines which status transitions are allowed. Each key
// maps to the set of statuses a task may transition to from that state.
var ValidTransitions = map[model.TaskStatus][]model.TaskStatus{
	model.StatusClassifying:        {model.StatusBacklog, model.StatusFailed, model.StatusPaused},
	model.StatusBacklog:            {model.StatusPlanning, model.StatusInProgress, model.StatusFailed, model.StatusPaused},
	model.StatusPlanning:           {model.StatusNeedsClarification, model.StatusPlanReview, model.StatusFailed, model.StatusPaused},
	model.StatusNeedsClarification: {model.StatusPlanning, model.StatusPlanReview},
	model.StatusPlanReview:         {model.StatusTestWriting, model.StatusInProgress, model.StatusPlanning},
	model.StatusTestWriting:        {model.StatusTestReview, model.StatusFailed, model.StatusPaused, model.StatusPlanning},
	model.StatusTestReview:         {model.StatusInProgress, model.StatusTestWriting, model.StatusPlanning},
	model.StatusInProgress:         {model.StatusTestingReady, model.StatusFailed, model.StatusPaused},
	model.StatusTestingReady:       {model.StatusMerging, model.StatusInProgress, model.StatusPlanning},
	model.StatusMerging:            {model.StatusDone, model.StatusFailed},
	model.StatusPaused:             {model.StatusClassifying, model.StatusBacklog, model.StatusPlanning, model.StatusInProgress, model.StatusTestWriting, model.StatusNeedsClarification},
	model.StatusDone:               {},
	model.StatusFailed:             {model.StatusClassifying, model.StatusBacklog, model.StatusInProgress, model.StatusTestWriting},
	model.StatusRejected:           {},
	model.StatusCancelled:          {},
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

// GuardedTransitionTask validates freshness and evidence before applying a task
// transition. New evidence-backed paths should prefer this over TransitionTask;
// the older helper remains for legacy call sites that have not been migrated.
func GuardedTransitionTask(task *model.Task, req TransitionRequest) (*model.TaskEvent, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	if req.ExpectedStatus != "" && task.Status != req.ExpectedStatus {
		return nil, fmt.Errorf("%w: task in status %q, expected %q", ErrStaleTransition, task.Status, req.ExpectedStatus)
	}
	if req.ObservedUpdatedAt != nil && !task.UpdatedAt.Equal(*req.ObservedUpdatedAt) {
		return nil, fmt.Errorf("%w: task updated_at changed", ErrStaleTransition)
	}
	if err := validateEvidence(task.ID, req); err != nil {
		return nil, err
	}

	actor := req.Actor
	if actor == "" {
		actor = req.Evidence.Actor
	}
	return transitionTaskAt(task, req.Target, actor, evidenceDetails(req.Evidence), evidenceTimestamp(req.Evidence))
}

func validateEvidence(taskID uuid.UUID, req TransitionRequest) error {
	e := req.Evidence
	if e.TaskID == uuid.Nil {
		return fmt.Errorf("%w: task_id is required", ErrMissingEvidence)
	}
	if e.TaskID != taskID {
		return fmt.Errorf("%w: evidence task_id %s does not match task %s", ErrMissingEvidence, e.TaskID, taskID)
	}
	if e.Actor == "" {
		return fmt.Errorf("%w: actor is required", ErrMissingEvidence)
	}
	if e.Source == "" {
		return fmt.Errorf("%w: source is required", ErrMissingEvidence)
	}
	if e.Reason == "" && e.NormalizedReason == "" {
		return fmt.Errorf("%w: reason or normalized_reason is required", ErrMissingEvidence)
	}
	return nil
}

func evidenceTimestamp(e Evidence) time.Time {
	if e.Timestamp.IsZero() {
		return time.Now()
	}
	return e.Timestamp
}

func evidenceDetails(e Evidence) map[string]any {
	details := map[string]any{
		"evidence": map[string]any{
			"task_id": e.TaskID.String(),
			"actor":   e.Actor,
			"source":  e.Source,
		},
	}
	evidence := details["evidence"].(map[string]any)
	if e.AttemptID != uuid.Nil {
		evidence["attempt_id"] = e.AttemptID.String()
	}
	if e.Reason != "" {
		evidence["reason"] = e.Reason
	}
	if e.NormalizedReason != "" {
		evidence["normalized_reason"] = e.NormalizedReason
	}
	if !e.Timestamp.IsZero() {
		evidence["timestamp"] = e.Timestamp.Format(time.RFC3339Nano)
	}
	if len(e.References) > 0 {
		evidence["references"] = e.References
	}
	return details
}

func transitionTaskAt(task *model.Task, target model.TaskStatus, actor string, details map[string]any, now time.Time) (*model.TaskEvent, error) {
	if err := ValidateTransition(task.Status, target); err != nil {
		return nil, err
	}

	oldStatus := task.Status
	task.Status = target
	task.UpdatedAt = now

	return &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "status_change",
		OldValue:  string(oldStatus),
		NewValue:  string(target),
		Details:   model.JSONField(details),
		Actor:     actor,
		CreatedAt: now,
	}, nil
}
