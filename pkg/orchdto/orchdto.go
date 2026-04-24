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
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	AssignedWorker string    `json:"assigned_worker"`
}

// WorkerDTO describes a single worker (agent) — its container, branch,
// current task, and liveness timestamps. Returned by /workers/:id and
// /projects/:name/workers.
type WorkerDTO struct {
	ID            string    `json:"id"`
	ContainerID   string    `json:"container_id"`
	Project       string    `json:"project"`
	AgentType     string    `json:"agent_type"`
	Branch        string    `json:"branch"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	CurrentTask   string    `json:"current_task"`
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
