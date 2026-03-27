package tui

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// extractModelID extracts the model_id field from an agent's Config.
// Returns "-" if the field is missing or empty.
func extractModelID(agent *model.Agent) string {
	if agent == nil || agent.Config == nil {
		return "-"
	}

	modelID, ok := agent.Config["model_id"].(string)
	if !ok || modelID == "" {
		return "-"
	}

	return modelID
}

// extractCost extracts the total_cost_usd field from an agent's Config.
// Returns "-" if the field is missing.
// Returns "$X.XX" format for any numeric value (including zero).
func extractCost(agent *model.Agent) string {
	if agent == nil || agent.Config == nil {
		return "-"
	}

	costVal, ok := agent.Config["total_cost_usd"]
	if !ok {
		return "-"
	}

	// Handle various numeric types that could come from JSON unmarshaling
	var cost float64
	switch v := costVal.(type) {
	case float64:
		cost = v
	case float32:
		cost = float64(v)
	case int:
		cost = float64(v)
	case int64:
		cost = float64(v)
	default:
		return "-"
	}

	// Format as currency: "$X.XX" (even for zero)
	return fmt.Sprintf("$%.2f", cost)
}
