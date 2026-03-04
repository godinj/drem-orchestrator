package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// AgentsModel renders the agent sidebar.
type AgentsModel struct {
	agents []model.Agent
	cursor int
	width  int
	height int
}

// NewAgentsModel creates an empty AgentsModel.
func NewAgentsModel() AgentsModel {
	return AgentsModel{}
}

// Update handles messages for the agent panel.
func (a AgentsModel) Update(msg tea.Msg) (AgentsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if a.cursor < len(a.agents)-1 {
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
	if len(a.agents) == 0 {
		return subtitleStyle.Render("  No agents running.")
	}

	var lines []string
	for i, ag := range a.agents {
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
	visible := lines
	if a.height > 0 && len(visible) > a.height {
		// Simple scroll: keep the block around the cursor visible.
		// Each agent takes ~3 lines.
		blockStart := a.cursor * 3
		if blockStart >= len(visible) {
			blockStart = len(visible) - a.height
		}
		if blockStart < 0 {
			blockStart = 0
		}
		end := blockStart + a.height
		if end > len(visible) {
			end = len(visible)
		}
		visible = visible[blockStart:end]
	}

	return strings.Join(visible, "\n")
}

// Selected returns the currently highlighted agent, or nil if there are no agents.
func (a AgentsModel) Selected() *model.Agent {
	if len(a.agents) == 0 {
		return nil
	}
	idx := a.cursor
	if idx >= len(a.agents) {
		idx = len(a.agents) - 1
	}
	if idx < 0 {
		return nil
	}
	ag := a.agents[idx]
	return &ag
}
