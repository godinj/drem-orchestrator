package tui

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// shortContainerID returns the first 12 characters of a container ID —
// the same "short form" Docker itself uses in `docker ps`. Empty input
// yields "-" so the agent row always renders a placeholder rather than
// collapsing the column width.
func shortContainerID(id string) string {
	if id == "" {
		return "-"
	}
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// imageForAgentType maps a model.AgentType to the worker image tag that
// the spawner would pick for it. The mapping is intentionally a TUI-local
// view: the authoritative table lives in internal/spawner/images.go, and
// this helper mirrors it only to give operators a rough "what's running"
// label without querying the spawner on every render.
//
// "coder" depends on the project's drem.language label which the TUI does
// not have on hand at render time; for that role we show the generic
// "drem-worker" stub. Unknown agent types fall back to "-".
func imageForAgentType(t model.AgentType) string {
	switch t {
	case model.AgentCoder:
		// Per-language coder image is resolved server-side by the spawner
		// using drem.language; the TUI shows the family instead of guessing.
		return "localhost:5000/drem-worker:latest"
	case "merger":
		return "localhost:5000/drem-merger:latest"
	case "csuite-mike":
		return "localhost:5000/drem-csuite-mike:latest"
	case "csuite-alex":
		return "localhost:5000/drem-csuite-alex:latest"
	case "csuite-seth":
		return "localhost:5000/drem-csuite-seth:latest"
	}
	if t == "" {
		return "-"
	}
	return fmt.Sprintf("localhost:5000/drem-worker-%s:latest", string(t))
}

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
