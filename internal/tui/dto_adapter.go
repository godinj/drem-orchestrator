package tui

import (
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// TaskFromDTO builds a model.Task from a TaskDTO. The orchestrator's
// read-only HTTP API returns a deliberately narrow projection (id, title,
// status, timestamps, assigned worker); fields the DTO does not carry
// (description, plan, labels, worktree branch, dependencies, …) remain
// their zero value and the existing rendering code treats them as unset.
// This is the lossy direction of the DTO boundary documented in
// docs/prd-containerization.md.
func TaskFromDTO(d orchdto.TaskDTO) model.Task {
	t := model.Task{
		Title:     d.Title,
		Status:    model.TaskStatus(d.Status),
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
	}
	if len(d.ActiveAttempts) > 0 || d.ActiveAttemptCount > 0 {
		t.Context = model.JSONField{"active_attempt_count": float64(d.ActiveAttemptCount)}
		attempts := make([]any, 0, len(d.ActiveAttempts))
		for _, attempt := range d.ActiveAttempts {
			attempts = append(attempts, map[string]any{
				"attempt_id":  attempt.AttemptID,
				"worker_id":   attempt.WorkerID,
				"agent_id":    attempt.AgentID,
				"role":        attempt.Role,
				"branch":      attempt.Branch,
				"lease_state": attempt.LeaseState,
			})
		}
		t.Context["active_attempts"] = attempts
		if d.ActiveAttemptCount == 0 {
			t.Context["active_attempt_count"] = float64(len(d.ActiveAttempts))
		}
		if len(d.ActiveAttempts) > 0 && d.ActiveAttempts[0].Branch != "" {
			t.WorktreeBranch = d.ActiveAttempts[0].Branch
		}
	}
	if id, err := uuid.Parse(d.ID); err == nil {
		t.ID = id
	}
	if d.AssignedWorker != "" {
		if aid, err := uuid.Parse(d.AssignedWorker); err == nil {
			t.AssignedAgentID = &aid
		}
	}
	return t
}

// TasksFromDTOs converts a slice of TaskDTO into model.Task preserving
// order. Returns nil when in is nil so callers observing a "no fetch yet"
// condition can distinguish it from "empty result".
func TasksFromDTOs(in []orchdto.TaskDTO) []model.Task {
	if in == nil {
		return nil
	}
	out := make([]model.Task, 0, len(in))
	for _, d := range in {
		out = append(out, TaskFromDTO(d))
	}
	return out
}

// AgentFromDTO builds a model.Agent from a WorkerDTO. The DTO uses the
// "worker" vocabulary introduced by containerization; the existing TUI
// renders rows as "agents", so this adapter bridges the naming shift
// without forcing a rename across hundreds of test callers.
func AgentFromDTO(d orchdto.WorkerDTO) model.Agent {
	a := model.Agent{
		AgentType:            model.AgentType(d.AgentType),
		Name:                 d.ContainerID,
		Status:               model.AgentStatus(d.Status),
		Provider:             d.Provider,
		ModelID:              d.ModelID,
		Effort:               d.Effort,
		CompletedAt:          d.CompletedAt,
		ExitReason:           d.ExitReason,
		TotalCostUSD:         d.TotalCostUSD,
		FinalContextPct:      d.FinalContextPct,
		TokensIn:             d.TokensIn,
		TokensOut:            d.TokensOut,
		ConstraintViolations: d.ConstraintViolations,
		CreatedAt:            d.StartedAt,
		UpdatedAt:            d.LastHeartbeat,
	}
	if d.FinalContextPct != 0 {
		a.Config = model.JSONField{"context_used_pct": float64(d.FinalContextPct)}
	}
	if id, err := uuid.Parse(d.ID); err == nil {
		a.ID = id
	}
	if !d.LastHeartbeat.IsZero() {
		hb := d.LastHeartbeat
		a.HeartbeatAt = &hb
	}
	if d.CurrentTask != "" {
		if tid, err := uuid.Parse(d.CurrentTask); err == nil {
			a.CurrentTaskID = &tid
		}
	}
	if d.Branch != "" {
		a.WorktreeBranch = d.Branch
	}
	return a
}

// AgentsFromDTOs converts a slice of WorkerDTO into model.Agent preserving
// order. See TasksFromDTOs for the nil-vs-empty contract.
func AgentsFromDTOs(in []orchdto.WorkerDTO) []model.Agent {
	if in == nil {
		return nil
	}
	out := make([]model.Agent, 0, len(in))
	for _, d := range in {
		out = append(out, AgentFromDTO(d))
	}
	return out
}
