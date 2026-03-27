package testutil

import (
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// AgentOption is a functional option for CreateAgent that sets enrichment
// fields on the agent before it is persisted to the database.
type AgentOption func(*model.Agent)

// WithModelID returns an AgentOption that sets the ModelID field.
func WithModelID(modelID string) AgentOption { return func(*model.Agent) {} }

// WithEffort returns an AgentOption that sets the Effort field.
func WithEffort(effort string) AgentOption { return func(*model.Agent) {} }

// WithCompletedAt returns an AgentOption that sets the CompletedAt field.
func WithCompletedAt(completedAt *time.Time) AgentOption { return func(*model.Agent) {} }

// WithExitReason returns an AgentOption that sets the ExitReason field.
func WithExitReason(reason string) AgentOption { return func(*model.Agent) {} }

// WithTotalCostUSD returns an AgentOption that sets the TotalCostUSD field.
func WithTotalCostUSD(cost float64) AgentOption { return func(*model.Agent) {} }

// WithFinalContextPct returns an AgentOption that sets the FinalContextPct field.
func WithFinalContextPct(pct float64) AgentOption { return func(*model.Agent) {} }
