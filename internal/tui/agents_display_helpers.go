package tui

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// extractModelID returns the agent's ModelID struct field.
// Returns "-" if the agent is nil or ModelID is empty.
func extractModelID(agent *model.Agent) string {
	if agent == nil || agent.ModelID == "" {
		return "-"
	}
	return agent.ModelID
}

// extractCost formats the agent's TotalCostUSD struct field as "$X.XX".
// Returns "-" if the agent is nil.
func extractCost(agent *model.Agent) string {
	if agent == nil {
		return "-"
	}
	return fmt.Sprintf("$%.2f", agent.TotalCostUSD)
}
