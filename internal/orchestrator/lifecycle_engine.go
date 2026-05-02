package orchestrator

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// lifecycleEngine is the boundary for advancing task lifecycle state.
// Keep callers on this interface instead of reaching into phase-specific
// methods like processPlanning or scheduleSubtasks.
type lifecycleEngine interface {
	Tick(context.Context, TickScope) (LifecycleOutcome, error)
	Apply(context.Context, LifecycleCommand) (LifecycleOutcome, error)
	Ingest(context.Context, LifecycleExternalEvent) (LifecycleOutcome, error)
}

type TickScope struct {
	ProjectID uuid.UUID
	Limit     int
	Now       time.Time
}

type LifecycleCommand struct {
	TaskID uuid.UUID
	Actor  string
	Kind   LifecycleCommandKind
	Input  map[string]any
}

type LifecycleCommandKind string

const (
	LifecycleCommandApprovePlan         LifecycleCommandKind = "approve_plan"
	LifecycleCommandRejectPlan          LifecycleCommandKind = "reject_plan"
	LifecycleCommandApproveTests        LifecycleCommandKind = "approve_tests"
	LifecycleCommandRejectTests         LifecycleCommandKind = "reject_tests"
	LifecycleCommandSubmitClarification LifecycleCommandKind = "submit_clarification"
	LifecycleCommandPause               LifecycleCommandKind = "pause"
	LifecycleCommandResume              LifecycleCommandKind = "resume"
	LifecycleCommandRetry               LifecycleCommandKind = "retry"
	LifecycleCommandOverrideClassify    LifecycleCommandKind = "override_classify"
)

type LifecycleExternalEvent struct {
	Kind     LifecycleExternalEventKind
	TaskID   uuid.UUID
	AgentID  *uuid.UUID
	WorkerID string
	ExitCode int
	Payload  map[string]any
	Occurred time.Time
}

type LifecycleExternalEventKind string

const (
	LifecycleEventAgentCompleted LifecycleExternalEventKind = "agent_completed"
	LifecycleEventAgentFailed    LifecycleExternalEventKind = "agent_failed"
	LifecycleEventWorkerDied     LifecycleExternalEventKind = "worker_died"
	LifecycleEventMergeCompleted LifecycleExternalEventKind = "merge_completed"
)

type LifecycleOutcome struct {
	ChangedTasks []uuid.UUID
	Dispatched   []LifecycleDispatchRecord
	Blocked      []LifecycleBlockReason
}

type LifecycleDispatchRecord struct {
	TaskID  uuid.UUID
	AgentID *uuid.UUID
	Kind    string
}

type LifecycleBlockReason struct {
	TaskID  uuid.UUID
	Code    string
	Message string
}

type orchestratorLifecycleEngine struct {
	orchestrator *Orchestrator
}

func newOrchestratorLifecycleEngine(o *Orchestrator) lifecycleEngine {
	return &orchestratorLifecycleEngine{orchestrator: o}
}

func (e *orchestratorLifecycleEngine) Tick(ctx context.Context, scope TickScope) (LifecycleOutcome, error) {
	_ = scope
	e.orchestrator.doTickLegacy(ctx)
	return LifecycleOutcome{}, nil
}

func (e *orchestratorLifecycleEngine) Apply(context.Context, LifecycleCommand) (LifecycleOutcome, error) {
	return LifecycleOutcome{}, nil
}

func (e *orchestratorLifecycleEngine) Ingest(context.Context, LifecycleExternalEvent) (LifecycleOutcome, error) {
	return LifecycleOutcome{}, nil
}
