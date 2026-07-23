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
	model.StatusClassifying:        {model.StatusBacklog, model.StatusFailed, model.StatusPaused, model.StatusCancelled},
	model.StatusBacklog:            {model.StatusPlanning, model.StatusInProgress, model.StatusFailed, model.StatusPaused, model.StatusCancelled},
	model.StatusPlanning:           {model.StatusNeedsClarification, model.StatusPlanReview, model.StatusFailed, model.StatusPaused, model.StatusCancelled},
	model.StatusNeedsClarification: {model.StatusPlanning, model.StatusPlanReview, model.StatusCancelled},
	model.StatusPlanReview:         {model.StatusTestWriting, model.StatusInProgress, model.StatusPlanning, model.StatusCancelled},
	model.StatusTestWriting:        {model.StatusTestReview, model.StatusFailed, model.StatusPaused, model.StatusPlanning, model.StatusCancelled},
	model.StatusTestReview:         {model.StatusInProgress, model.StatusTestWriting, model.StatusPlanning, model.StatusFailed, model.StatusCancelled},
	model.StatusInProgress:         {model.StatusBacklog, model.StatusTestingReady, model.StatusFailed, model.StatusPaused, model.StatusCancelled},
	model.StatusTestingReady:       {model.StatusVerificationReady, model.StatusInProgress, model.StatusPlanning, model.StatusFailed, model.StatusPaused, model.StatusCancelled},
	model.StatusVerificationReady:  {model.StatusIntegrationReady, model.StatusHostRework, model.StatusInProgress, model.StatusCancelled},
	model.StatusHostRework:         {model.StatusTestingReady, model.StatusInProgress, model.StatusCancelled},
	model.StatusIntegrationReady:   {model.StatusMerging, model.StatusHostRework, model.StatusInProgress, model.StatusCancelled},
	model.StatusMerging:            {model.StatusDone, model.StatusFailed, model.StatusTestingReady, model.StatusIntegrationReady, model.StatusInProgress, model.StatusCancelled},
	model.StatusPaused:             {model.StatusClassifying, model.StatusBacklog, model.StatusPlanning, model.StatusInProgress, model.StatusTestWriting, model.StatusTestingReady, model.StatusNeedsClarification, model.StatusCancelled},
	model.StatusDone:               {},
	model.StatusFailed:             {model.StatusClassifying, model.StatusBacklog, model.StatusInProgress, model.StatusTestWriting, model.StatusCancelled},
	model.StatusRejected:           {model.StatusCancelled},
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

// GuardedCompleteSubtask records worker completion without routing a child
// through the parent-only delivery states. Top-level tasks are rejected so
// this cannot bypass artifact verification.
func GuardedCompleteSubtask(task *model.Task, req TransitionRequest) (*model.TaskEvent, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	if task.ParentTaskID == nil {
		return nil, errors.New("only subtasks may use direct completion")
	}
	if req.Target != model.StatusDone {
		return nil, errors.New("subtask completion target must be done")
	}
	if req.ExpectedStatus != "" && task.Status != req.ExpectedStatus {
		return nil, fmt.Errorf("%w: task in status %q, expected %q", ErrStaleTransition, task.Status, req.ExpectedStatus)
	}
	if task.Status != model.StatusInProgress && task.Status != model.StatusTestWriting {
		return nil, fmt.Errorf("subtask in status %q cannot complete directly", task.Status)
	}
	if err := validateEvidence(task.ID, req); err != nil {
		return nil, err
	}
	actor := req.Actor
	if actor == "" {
		actor = req.Evidence.Actor
	}
	return transitionTaskUncheckedAt(task, model.StatusDone, actor, evidenceDetails(req.Evidence), evidenceTimestamp(req.Evidence)), nil
}

// GuardedAdoptFailedSubtask is the narrow recovery path for a host Codex
// repair that has already passed deterministic branch admission. It keeps a
// rejected worker attempt immutable while allowing its failed child task to
// become done without pretending another worker ran.
func GuardedAdoptFailedSubtask(task *model.Task, req TransitionRequest) (*model.TaskEvent, error) {
	if task == nil || task.ParentTaskID == nil {
		return nil, errors.New("only failed subtasks may be adopted")
	}
	if task.Status != model.StatusFailed || req.Target != model.StatusDone {
		return nil, errors.New("Codex adoption requires failed subtask to done")
	}
	if req.ExpectedStatus != "" && req.ExpectedStatus != model.StatusFailed {
		return nil, fmt.Errorf("%w: expected status must be failed", ErrStaleTransition)
	}
	if req.Evidence.Source != "codex_adapter_adoption" {
		return nil, fmt.Errorf("%w: codex_adapter_adoption source is required", ErrMissingEvidence)
	}
	if err := validateEvidence(task.ID, req); err != nil {
		return nil, err
	}
	actor := req.Actor
	if actor == "" {
		actor = req.Evidence.Actor
	}
	return transitionTaskUncheckedAt(task, model.StatusDone, actor, evidenceDetails(req.Evidence), evidenceTimestamp(req.Evidence)), nil
}

// GuardedAcceptExistingSubtask records that a parent-scoped unit of work is
// already present on the feature branch. It is intentionally separate from
// worker completion so deduplication never synthesizes parent delivery states.
func GuardedAcceptExistingSubtask(task *model.Task, req TransitionRequest) (*model.TaskEvent, error) {
	if task == nil || task.ParentTaskID == nil {
		return nil, errors.New("only subtasks may accept existing work")
	}
	if req.Target != model.StatusDone {
		return nil, errors.New("existing-work acceptance target must be done")
	}
	if req.ExpectedStatus != "" && task.Status != req.ExpectedStatus {
		return nil, fmt.Errorf("%w: task in status %q, expected %q", ErrStaleTransition, task.Status, req.ExpectedStatus)
	}
	if task.Status != model.StatusBacklog && task.Status != model.StatusPlanning && task.Status != model.StatusInProgress {
		return nil, fmt.Errorf("subtask in status %q cannot accept existing work", task.Status)
	}
	if req.Evidence.Source != "dedup_existing_work" {
		return nil, fmt.Errorf("%w: dedup_existing_work source is required", ErrMissingEvidence)
	}
	if err := validateEvidence(task.ID, req); err != nil {
		return nil, err
	}
	actor := req.Actor
	if actor == "" {
		actor = req.Evidence.Actor
	}
	return transitionTaskUncheckedAt(task, model.StatusDone, actor, evidenceDetails(req.Evidence), evidenceTimestamp(req.Evidence)), nil
}

// GuardedSupersedeCompletedTestSubtask is the sole exception to DONE being
// terminal: a completed test-writing child may be superseded when its parent
// test review is rejected. The rejected record remains immutable history and
// the orchestrator creates a new backlog revision rather than reopening it.
func GuardedSupersedeCompletedTestSubtask(task *model.Task, req TransitionRequest) (*model.TaskEvent, error) {
	if req.Target != model.StatusRejected {
		return nil, errors.New("test subtask supersession requires done to rejected")
	}
	return GuardedInvalidateCompletedTestSubtask(task, req)
}

// GuardedInvalidateCompletedTestSubtask lets automated review fail a completed
// test child so a Codex-authored correction can use the audited adoption path.
func GuardedInvalidateCompletedTestSubtask(task *model.Task, req TransitionRequest) (*model.TaskEvent, error) {
	if task == nil || task.ParentTaskID == nil || task.Phase != "test" {
		return nil, errors.New("only completed test subtasks may be invalidated")
	}
	if task.Status != model.StatusDone || (req.Target != model.StatusRejected && req.Target != model.StatusFailed) {
		return nil, errors.New("test subtask invalidation requires done to rejected or failed")
	}
	if req.ExpectedStatus != "" && req.ExpectedStatus != model.StatusDone {
		return nil, fmt.Errorf("%w: expected status must be done", ErrStaleTransition)
	}
	if req.Evidence.Source != "review_gate" {
		return nil, fmt.Errorf("%w: review_gate source is required", ErrMissingEvidence)
	}
	if err := validateEvidence(task.ID, req); err != nil {
		return nil, err
	}
	actor := req.Actor
	if actor == "" {
		actor = req.Evidence.Actor
	}
	return transitionTaskUncheckedAt(task, req.Target, actor, evidenceDetails(req.Evidence), evidenceTimestamp(req.Evidence)), nil
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

	return transitionTaskUncheckedAt(task, target, actor, details, now), nil
}

func transitionTaskUncheckedAt(task *model.Task, target model.TaskStatus, actor string, details map[string]any, now time.Time) *model.TaskEvent {
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
	}
}
