package testutil

import (
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// AgentOption is a functional option for CreateAgent that sets enrichment
// fields on the agent before it is persisted to the database.
type AgentOption func(*model.Agent)

// WithModelID returns an AgentOption that sets the ModelID field.
func WithModelID(modelID string) AgentOption {
	return func(a *model.Agent) { a.ModelID = modelID }
}

// WithEffort returns an AgentOption that sets the Effort field.
func WithEffort(effort string) AgentOption {
	return func(a *model.Agent) { a.Effort = effort }
}

// WithCompletedAt returns an AgentOption that sets the CompletedAt field.
func WithCompletedAt(completedAt *time.Time) AgentOption {
	return func(a *model.Agent) { a.CompletedAt = completedAt }
}

// WithExitReason returns an AgentOption that sets the ExitReason field.
func WithExitReason(reason string) AgentOption {
	return func(a *model.Agent) { a.ExitReason = reason }
}

// WithTotalCostUSD returns an AgentOption that sets the TotalCostUSD field.
func WithTotalCostUSD(cost float64) AgentOption {
	return func(a *model.Agent) { a.TotalCostUSD = cost }
}

// WithFinalContextPct returns an AgentOption that sets the FinalContextPct field.
func WithFinalContextPct(pct float64) AgentOption {
	return func(a *model.Agent) { a.FinalContextPct = pct }
}
