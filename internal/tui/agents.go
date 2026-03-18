package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const (
	maxBranchDisplayLen = 30
	ctxDangerPct        = 90 // context usage % shown in red
	ctxWarningPct       = 75 // context usage % shown in orange
)

// AgentsModel renders the agent sidebar.
type AgentsModel struct {
	agents       []model.Agent
	cursor       int
	width        int
	height       int
	showArchived bool        // A toggle: show dead + idle agents
	filterTaskID *uuid.UUID  // selected board task (nil = no filter)
	subtaskIDs   []uuid.UUID // subtask IDs of selected task
	autoFilter   bool        // F toggle: auto-filter by task (default true)
}

// visibleAgents returns the agents that should be displayed based on the
// showArchived toggle and task filter. When showArchived is false, dead and
// idle agents are hidden. When autoFilter is true and a task is selected,
// only agents working on that task or its subtasks are shown.
func (a AgentsModel) visibleAgents() []model.Agent {
	var visible []model.Agent
	for _, ag := range a.agents {
		// Filter out dead + idle unless showArchived is on.
		if !a.showArchived && (ag.Status == model.AgentDead || ag.Status == model.AgentIdle) {
			continue
		}
		// Task-based filter: keep only agents related to the selected task.
		if a.autoFilter && a.filterTaskID != nil {
			if ag.CurrentTaskID == nil {
				continue
			}
			if *ag.CurrentTaskID != *a.filterTaskID && !a.isSubtaskID(*ag.CurrentTaskID) {
				continue
			}
		}
		visible = append(visible, ag)
	}
	return visible
}

// isSubtaskID checks whether the given ID is one of the subtask IDs.
func (a AgentsModel) isSubtaskID(id uuid.UUID) bool {
	for _, sid := range a.subtaskIDs {
		if sid == id {
			return true
		}
	}
	return false
}

// setTaskFilter updates the task filter from the board selection.
func (a *AgentsModel) setTaskFilter(taskID *uuid.UUID, subtaskIDs []uuid.UUID) {
	a.filterTaskID = taskID
	a.subtaskIDs = subtaskIDs
	a.clampAgentCursor()
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
	return AgentsModel{autoFilter: true}
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
		if len(a.agents) > 0 {
			return subtitleStyle.Render("  No matching agents. A:archived  F:filter")
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

		// Exit reason for dead/idle agents.
		if (ag.Status == model.AgentDead || ag.Status == model.AgentIdle) && ag.Config != nil {
			if exitReason, ok := ag.Config["exit_reason"].(string); ok {
				lines = append(lines, subtitleStyle.Render(
					fmt.Sprintf("    exit: %s", exitReason),
				))
			}
		}

		// Show activity indicator if available.
		if tool, ok := ag.Config["activity_tool"].(string); ok && tool != "" {
			target, _ := ag.Config["activity_target"].(string)
			progress, _ := ag.Config["activity_file_progress"].(string)
			committed, _ := ag.Config["activity_committed"].(bool)
			phase, _ := ag.Config["activity_phase"].(string)
			stuckScore := 0
			if ss, ok := ag.Config["activity_stuck_score"].(float64); ok {
				stuckScore = int(ss)
			}

			// Truncate target for display.
			if len(target) > maxBranchDisplayLen {
				target = "…" + target[len(target)-maxBranchDisplayLen+1:]
			}

			actLine := fmt.Sprintf("    ▸ %s %s", tool, target)
			if progress != "" {
				actLine += " | " + progress
			}
			if committed {
				actLine += " | committed"
			} else {
				actLine += " | no commit"
			}

			// Color by phase.
			actStyle := subtitleStyle
			switch phase {
			case "exploring":
				actStyle = lipgloss.NewStyle().Foreground(colorInfo)
			case "implementing":
				actStyle = lipgloss.NewStyle().Foreground(colorWarning)
			case "testing":
				actStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
			case "fixing":
				actStyle = lipgloss.NewStyle().Foreground(colorDanger)
			}
			if stuckScore > 0 {
				actStyle = lipgloss.NewStyle().Foreground(colorDanger)
			}

			// Append ctx% inline if available.
			if pct, ok := ag.Config["context_used_pct"].(float64); ok {
				ctxStyle := subtitleStyle
				switch {
				case pct >= ctxDangerPct:
					ctxStyle = lipgloss.NewStyle().Foreground(colorDanger)
				case pct >= ctxWarningPct:
					ctxStyle = lipgloss.NewStyle().Foreground(colorWarning)
				}
				ctxSuffix := fmt.Sprintf(" | ctx: %d%%", int(pct))
				lines = append(lines, actStyle.Render(actLine)+ctxStyle.Render(ctxSuffix))
			} else {
				lines = append(lines, actStyle.Render(actLine))
			}
		} else if pct, ok := ag.Config["context_used_pct"].(float64); ok {
			// Fallback: show ctx standalone when no activity data.
			ctxStyle := subtitleStyle
			switch {
			case pct >= ctxDangerPct:
				ctxStyle = lipgloss.NewStyle().Foreground(colorDanger)
			case pct >= ctxWarningPct:
				ctxStyle = lipgloss.NewStyle().Foreground(colorWarning)
			}
			lines = append(lines, ctxStyle.Render(
				fmt.Sprintf("    ctx: %d%%", int(pct)),
			))
		}
	}

	// Limit visible lines to height, scrolling to keep the cursor visible.
	if a.height > 0 && len(lines) > a.height {
		// Calculate the actual start line for the selected agent.
		cursorStart := 0
		cursorEnd := 0
		lineIdx := 0
		for i, ag := range visible {
			blockLen := 1 // header line
			if ag.TmuxSession != "" {
				blockLen++
			}
			if ag.WorktreeBranch != "" {
				blockLen++
			}
			if (ag.Status == model.AgentDead || ag.Status == model.AgentIdle) && ag.Config != nil {
				if _, ok := ag.Config["exit_reason"].(string); ok {
					blockLen++
				}
			}
			_, hasCTX := ag.Config["context_used_pct"].(float64)
			hasActivity := false
			if tool, ok := ag.Config["activity_tool"].(string); ok && tool != "" {
				hasActivity = true
				blockLen++
			}
			if !hasActivity && hasCTX {
				blockLen++
			}
			if i == a.cursor {
				cursorStart = lineIdx
				cursorEnd = lineIdx + blockLen
				break
			}
			lineIdx += blockLen
		}

		// Determine the scroll window that keeps the cursor block visible.
		start := 0
		if cursorEnd > a.height {
			start = cursorEnd - a.height
		}
		if cursorStart < start {
			start = cursorStart
		}
		end := start + a.height
		if end > len(lines) {
			end = len(lines)
			start = end - a.height
			if start < 0 {
				start = 0
			}
		}
		lines = lines[start:end]
	}

	return strings.Join(lines, "\n")
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
