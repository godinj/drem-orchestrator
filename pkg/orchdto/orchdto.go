// Package orchdto holds the public JSON wire types returned by the
// orchestrator HTTP API. It is imported by server-side handlers in
// internal/orchhttp, by the client library in pkg/orchclient, and by any
// consumer (Kyle, the TUI) that needs to decode API responses.
//
// The package is intentionally leaf-level: it has no dependencies outside
// the Go standard library so importers never pull in GORM, the orchestrator,
// or any internal/* code transitively. Schema changes to internal GORM
// models must not change these DTOs without a conscious decision; handlers
// marshal from models into these shapes so drift is explicit.
package orchdto

import (
	"encoding/json"
	"time"
)

// ProjectDTO describes a single project served by an orchestrator. The
// public /projects endpoint returns a slice of these; each orchestrator
// instance owns exactly one project today but the shape is a slice so Kyle
// can aggregate across projects without special-casing single-entry
// responses.
type ProjectDTO struct {
	Name        string `json:"name"`
	Language    string `json:"language"`
	OrchURL     string `json:"orch_url"`
	WorkerCount int    `json:"worker_count"`
}

// TaskDTO is the minimal task projection returned by the tasks endpoint.
// Callers that need full plan/context fields should query the orchestrator
// directly; the HTTP API intentionally returns a narrow shape so Kyle and
// the TUI do not become coupled to every field on the GORM model.
type TaskDTO struct {
	ID                   string     `json:"id"`
	Title                string     `json:"title"`
	Status               string     `json:"status"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	AssignedWorker       string     `json:"assigned_worker"`
	Category             string     `json:"category,omitempty"`
	CurrentHealth        string     `json:"current_health,omitempty"`
	LatestFailureSummary string     `json:"latest_failure_summary,omitempty"`
	LatestFailureType    string     `json:"latest_failure_type,omitempty"`
	LatestFailureAt      *time.Time `json:"latest_failure_at,omitempty"`
	LatestFailureCurrent *bool      `json:"latest_failure_current,omitempty"`
}

// TaskCommentDTO is the public projection returned after appending a task
// comment through the HTTP API.
type TaskCommentDTO struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkerDTO describes a single worker (agent) — its container, branch,
// current task, and liveness timestamps. Returned by /workers/:id and
// /projects/:name/workers.
type WorkerDTO struct {
	ID                   string     `json:"id"`
	ContainerID          string     `json:"container_id"`
	Project              string     `json:"project"`
	AgentType            string     `json:"agent_type"`
	Branch               string     `json:"branch"`
	Status               string     `json:"status"`
	StartedAt            time.Time  `json:"started_at"`
	LastHeartbeat        time.Time  `json:"last_heartbeat"`
	CurrentTask          string     `json:"current_task"`
	Provider             string     `json:"provider"`
	ModelID              string     `json:"model_id"`
	Effort               string     `json:"effort"`
	CompletedAt          *time.Time `json:"completed_at"`
	ExitReason           string     `json:"exit_reason"`
	TotalCostUSD         float64    `json:"total_cost_usd"`
	FinalContextPct      int        `json:"final_context_pct"`
	TokensIn             int        `json:"tokens_in"`
	TokensOut            int        `json:"tokens_out"`
	ConstraintViolations int        `json:"constraint_violations"`
}

// WorkerAttemptDTO is the public projection of a task execution attempt
// attributed to a worker/container. New attempts are backed by a durable
// WorkerAttempt row; older history may still be projected from spawn events
// or Agent rows.
type WorkerAttemptDTO struct {
	AttemptID             string     `json:"attempt_id"`
	TaskID                string     `json:"task_id"`
	WorkerID              string     `json:"worker_id"`
	AgentID               string     `json:"agent_id"`
	ContainerID           string     `json:"container_id"`
	WorkerLabel           string     `json:"worker_label"`
	AgentType             string     `json:"agent_type"`
	Branch                string     `json:"branch"`
	Provider              string     `json:"provider"`
	ModelID               string     `json:"model_id"`
	Effort                string     `json:"effort"`
	Status                string     `json:"status"`
	StartedAt             time.Time  `json:"started_at"`
	CompletedAt           *time.Time `json:"completed_at"`
	LastHeartbeat         time.Time  `json:"last_heartbeat"`
	ExitReason            string     `json:"exit_reason"`
	FailureClassification string     `json:"failure_classification"`
	FirstError            string     `json:"first_error"`
	TokensIn              int        `json:"tokens_in"`
	TokensOut             int        `json:"tokens_out"`
	TotalCostUSD          float64    `json:"total_cost_usd"`
	FinalContextPct       int        `json:"final_context_pct"`
	ConstraintViolations  int        `json:"constraint_violations"`
}

// WorkerHistoryDTO wraps a worker's recent state transitions and exit
// events. The Events slice is ordered oldest-first.
type WorkerHistoryDTO struct {
	WorkerID string               `json:"worker_id"`
	Events   []WorkerHistoryEntry `json:"events"`
}

// WorkerHistoryEntry is one row of a worker's transition log.
type WorkerHistoryEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`
	Detail    string          `json:"detail"`
	ExitCode  int             `json:"exit_code"`
	Details   json.RawMessage `json:"details,omitempty"`
}

// EventDTO is one structured event emitted by agentmon and surfaced via
// GET /events. Payload is left as a raw JSON message so new event types
// can be added without recompiling clients that only care about Timestamp
// and Type.
type EventDTO struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// HealthIssueDTO is one operator-facing health finding from the orchestrator.
// Findings are intentionally descriptive rather than prescriptive: recovery
// mutations use separate, narrower endpoints.
type HealthIssueDTO struct {
	Type       string     `json:"type"`
	Severity   string     `json:"severity"`
	TaskID     string     `json:"task_id,omitempty"`
	WorkerID   string     `json:"worker_id,omitempty"`
	Status     string     `json:"status,omitempty"`
	DetectedAt time.Time  `json:"detected_at"`
	AgeSeconds int64      `json:"age_seconds,omitempty"`
	LastEvent  *time.Time `json:"last_event,omitempty"`
	Message    string     `json:"message"`
}

// StaleAssignmentRecoveryRequest asks the orchestrator to classify or repair a
// single task assignment. Exactly one of DryRun or Apply must be true.
type StaleAssignmentRecoveryRequest struct {
	DryRun bool   `json:"dry_run"`
	Apply  bool   `json:"apply"`
	Actor  string `json:"actor,omitempty"`
}

// StaleAssignmentRecoveryDTO is returned by successful stale-assignment
// recovery classification or repair. Unsafe live assignments return 409.
type StaleAssignmentRecoveryDTO struct {
	TaskID         string `json:"task_id"`
	Status         string `json:"status"`
	AssignedWorker string `json:"assigned_worker,omitempty"`
	WorkerStatus   string `json:"worker_status,omitempty"`
	Classification string `json:"classification"`
	Safe           bool   `json:"safe"`
	Applied        bool   `json:"applied"`
	Message        string `json:"message"`
}

// RecoveryAuditRequest is the public payload accepted by Kyle's narrow
// recovery audit endpoint. It records why an autonomous action was allowed and
// what result it produced without exposing a generic task-event write surface.
type RecoveryAuditRequest struct {
	Actor          string `json:"actor"`
	PolicyRule     string `json:"policy_rule"`
	Evidence       string `json:"evidence"`
	Surface        string `json:"surface"`
	Action         string `json:"action"`
	Result         string `json:"result"`
	NextFollowUp   string `json:"next_follow_up"`
	SupportedPath  bool   `json:"supported_path"`
	BreakGlassPath bool   `json:"break_glass_path"`
}

// IngestRequest is the body accepted by POST /internal/logs. Each element
// of Records is a discriminated-union record tagged by a "type" field; the
// server decodes and routes per record.
type IngestRequest struct {
	Records []json.RawMessage `json:"records"`
}

// IngestResponse is returned (with status 202) once all records in a batch
// have been written. The count mirrors len(IngestRequest.Records) on
// success; all-or-nothing semantics mean a partial count is never reported.
type IngestResponse struct {
	Accepted int `json:"accepted"`
}
