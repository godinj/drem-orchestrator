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

// extractTokens formats the agent's TokensIn / TokensOut struct fields
// as a compact "<in>↑ <out>↓" string suitable for the narrow TUI column.
// Values >= 1000 are abbreviated with a "k" suffix (one decimal place).
// Returns "-" if the agent is nil.
func extractTokens(agent *model.Agent) string {
	if agent == nil {
		return "-"
	}
	return fmt.Sprintf("%s↑ %s↓", formatTokenCount(agent.TokensIn), formatTokenCount(agent.TokensOut))
}

// formatTokenCount renders an integer token count in a compact form.
// Counts >= 1000 are shown as "<X.Y>k"; smaller counts are shown as-is.
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000.0)
}
