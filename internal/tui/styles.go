// Package tui implements the Bubble Tea TUI dashboard for the Drem
// Orchestrator. It provides a task board, agent list, task detail view,
// task creation form, and feedback dialogs composed into a single
// terminal application.
package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Color palette used throughout the TUI.
var (
	colorPrimary   = lipgloss.Color("62")  // Purple
	colorSecondary = lipgloss.Color("241") // Gray
	colorSuccess   = lipgloss.Color("42")  // Green
	colorWarning   = lipgloss.Color("214") // Orange
	colorDanger    = lipgloss.Color("196") // Red
	colorInfo      = lipgloss.Color("39")  // Blue
)

// statusColors maps each TaskStatus to a display color.
var statusColors = map[model.TaskStatus]lipgloss.Color{
	model.StatusBacklog:       lipgloss.Color("241"),
	model.StatusPlanning:      lipgloss.Color("39"),
	model.StatusPlanReview:    lipgloss.Color("214"),
	model.StatusInProgress:    lipgloss.Color("62"),
	model.StatusTestingReady:  lipgloss.Color("214"),
	model.StatusManualTesting: lipgloss.Color("214"),
	model.StatusMerging:       lipgloss.Color("62"),
	model.StatusPaused:        lipgloss.Color("241"),
	model.StatusDone:          lipgloss.Color("42"),
	model.StatusFailed:        lipgloss.Color("196"),
}

// agentStatusColors maps each AgentStatus to a display color.
var agentStatusColors = map[model.AgentStatus]lipgloss.Color{
	model.AgentIdle:    lipgloss.Color("241"),
	model.AgentWorking: lipgloss.Color("42"),
	model.AgentBlocked: lipgloss.Color("214"),
	model.AgentDead:    lipgloss.Color("196"),
}

// statusIcons maps each TaskStatus to a Unicode icon.
var statusIcons = map[model.TaskStatus]string{
	model.StatusBacklog:       "\u25cb", // ○
	model.StatusPlanning:      "\u25cc", // ◌
	model.StatusPlanReview:    "\u25c9", // ◉
	model.StatusInProgress:    "\u25cf", // ●
	model.StatusTestingReady:  "\u25c8", // ◈
	model.StatusManualTesting: "\u25c7", // ◇
	model.StatusMerging:       "\u27f3", // ⟳
	model.StatusPaused:        "\u23f8", // ⏸
	model.StatusDone:          "\u2713", // ✓
	model.StatusFailed:        "\u2717", // ✗
}

// Component styles used across the TUI.
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	subtitleStyle = lipgloss.NewStyle().Foreground(colorSecondary)
	selectedStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236"))
	statusBadge   = lipgloss.NewStyle().Padding(0, 1)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	helpStyle     = lipgloss.NewStyle().Foreground(colorSecondary)
)

// StatusBadge renders a colored status badge for a task status.
func StatusBadge(status model.TaskStatus) string {
	color, ok := statusColors[status]
	if !ok {
		color = lipgloss.Color("241")
	}
	icon, ok := statusIcons[status]
	if !ok {
		icon = "?"
	}
	return statusBadge.Foreground(color).Render(icon + " " + string(status))
}

// AgentStatusBadge renders a colored agent status indicator.
func AgentStatusBadge(status model.AgentStatus) string {
	color, ok := agentStatusColors[status]
	if !ok {
		color = lipgloss.Color("241")
	}
	return statusBadge.Foreground(color).Render(string(status))
}
