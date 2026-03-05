package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// AgentsModel renders the agent sidebar.
type AgentsModel struct {
	agents   []model.Agent
	cursor   int
	width    int
	height   int
	showDead bool
}

// visibleAgents returns the agents that should be displayed based on the
// current showDead toggle. When showDead is false, dead agents are hidden.
func (a AgentsModel) visibleAgents() []model.Agent {
	if a.showDead {
		return a.agents
	}
	var visible []model.Agent
	for _, ag := range a.agents {
		if ag.Status != model.AgentDead {
			visible = append(visible, ag)
		}
	}
	return visible
}

// clampAgentCursor ensures the agent cursor doesn't exceed the visible list length.
func (a *AgentsModel) clampAgentCursor() {
	n := len(a.visibleAgents())
	if a.cursor >= n {
		a.cursor = n - 1
	}
	if a.cursor < 0 {
		a.cursor = 0
	}
}

// NewAgentsModel creates an empty AgentsModel.
func NewAgentsModel() AgentsModel {
	return AgentsModel{}
}

// Update handles messages for the agent panel.
func (a AgentsModel) Update(msg tea.Msg) (AgentsModel, tea.Cmd) {
	visible := a.visibleAgents()
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if a.cursor < len(visible)-1 {
				a.cursor++
			}
		case "k", "up":
			if a.cursor > 0 {
				a.cursor--
			}
		}
	}
	return a, nil
}

// View renders the agent list.
func (a AgentsModel) View() string {
	visible := a.visibleAgents()
	if len(visible) == 0 {
		if !a.showDead && len(a.agents) > 0 {
			return subtitleStyle.Render("  No active agents. Press A to show archived.")
		}
		return subtitleStyle.Render("  No agents running.")
	}

	var lines []string
	for i, ag := range visible {
		badge := AgentStatusBadge(ag.Status)

		// Truncate name if needed.
		name := ag.Name
		maxName := a.width - 16
		if maxName < 8 {
			maxName = 8
		}
		if len(name) > maxName {
			name = name[:maxName-1] + "\u2026"
		}

		header := fmt.Sprintf("  %s  %s", name, badge)
		if i == a.cursor {
			header = selectedStyle.Width(a.width).Render(
				fmt.Sprintf("> %s  %s", name, badge),
			)
		}

		lines = append(lines, header)

		// Indented details.
		if ag.TmuxSession != "" {
			lines = append(lines, subtitleStyle.Render(
				fmt.Sprintf("    session: %s", ag.TmuxSession),
			))
		}
		if ag.WorktreeBranch != "" {
			lines = append(lines, subtitleStyle.Render(
				fmt.Sprintf("    branch: %s", ag.WorktreeBranch),
			))
		}
	}

	// Limit visible lines to height.
	visibleLines := lines
	if a.height > 0 && len(visibleLines) > a.height {
		// Simple scroll: keep the block around the cursor visible.
		// Each agent takes ~3 lines.
		blockStart := a.cursor * 3
		if blockStart >= len(visibleLines) {
			blockStart = len(visibleLines) - a.height
		}
		if blockStart < 0 {
			blockStart = 0
		}
		end := blockStart + a.height
		if end > len(visibleLines) {
			end = len(visibleLines)
		}
		visibleLines = visibleLines[blockStart:end]
	}

	return strings.Join(visibleLines, "\n")
}

// Selected returns the currently highlighted agent, or nil if there are no visible agents.
func (a AgentsModel) Selected() *model.Agent {
	visible := a.visibleAgents()
	if len(visible) == 0 {
		return nil
	}
	idx := a.cursor
	if idx >= len(visible) {
		idx = len(visible) - 1
	}
	if idx < 0 {
		return nil
	}
	ag := visible[idx]
	return &ag
}
