package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// panelBorderColor returns the border color for a panel based on focus.
func (m Model) panelBorderColor(panel Focus) lipgloss.Color {
	if m.focus == panel {
		return colorPrimary
	}
	return lipgloss.Color("238")
}

// panelDimensions holds the computed layout dimensions after a resize.
type panelDimensions struct {
	upperHeight  int
	detailHeight int
	innerWidth   int
	tasksWidth   int
	agentsWidth  int
}

// computePanelDimensions calculates layout dimensions from the current
// terminal width and height. Both updatePanelSizes (on WindowSizeMsg) and
// View() share this single implementation to avoid drift.
func (m Model) computePanelDimensions() panelDimensions {
	// Title (1) + status bar (1) + blank (1) + help bar (1) + panel borders (4) = 8 overhead.
	overhead := 8
	availableHeight := m.height - overhead
	if availableHeight < 4 {
		availableHeight = 4
	}

	// Split: upper panels (60%), detail panel (40%).
	upperHeight := availableHeight * 6 / 10
	detailHeight := availableHeight - upperHeight
	if upperHeight < 3 {
		upperHeight = 3
	}
	if detailHeight < 3 {
		detailHeight = 3
	}

	// Split width: tasks (60%) | agents (40%).
	innerWidth := m.width - 2 // Account for outer margin.
	if innerWidth < 10 {
		innerWidth = 10
	}
	tasksWidth := innerWidth * 6 / 10
	agentsWidth := innerWidth - tasksWidth

	return panelDimensions{
		upperHeight:  upperHeight,
		detailHeight: detailHeight,
		innerWidth:   innerWidth,
		tasksWidth:   tasksWidth,
		agentsWidth:  agentsWidth,
	}
}

// updatePanelSizes recalculates panel dimensions after a window resize.
// These are persisted on the sub-models so that scroll offsets can be
// maintained correctly between Update and View calls.
func (m *Model) updatePanelSizes() {
	d := m.computePanelDimensions()

	m.board.width = d.tasksWidth - 4 // Account for panel border + padding.
	m.board.height = d.upperHeight - 2
	m.agents.width = d.agentsWidth - 4
	m.agents.height = d.upperHeight - 2
	m.detail.width = d.innerWidth - 4
	m.detail.height = d.detailHeight - 2

	// Size overlay text inputs to fit the dialog container.
	// The overlay is 2/3 of screen width; subtract 4 for border (2) + padding (2).
	overlayInnerWidth := m.width*2/3 - 4
	m.create.SetWidth(overlayInnerWidth)
	m.feedback.SetWidth(overlayInnerWidth)
}

// confirmActionLabel returns a human-readable label for the pending confirmation.
func confirmActionLabel(action confirmAction) string {
	switch action {
	case confirmPlanApprove:
		return "Approve plan"
	case confirmPlanReject:
		return "Reject plan"
	case confirmTestReviewApprove:
		return "Approve test review"
	case confirmTestPass:
		return "Pass testing"
	case confirmTestFail:
		return "Fail testing"
	default:
		return ""
	}
}

// renderConfirmPrompt renders the gate action confirmation prompt.
func (m Model) renderConfirmPrompt() string {
	label := confirmActionLabel(m.confirm)
	if label == "" {
		return ""
	}

	taskTitle := ""
	if selected := m.board.Selected(); selected != nil {
		taskTitle = selected.Title
		if len(taskTitle) > 40 {
			taskTitle = taskTitle[:39] + "\u2026"
		}
	}

	style := lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	return style.Render(fmt.Sprintf("  Confirm: %s for \"%s\"? [y]es / [n]o", label, taskTitle))
}

// renderBugReportsScreen renders the full-screen bug report view.
func (m Model) renderBugReportsScreen() string {
	// Update bug report panel sizes.
	innerWidth := m.width - 4 // account for border + padding
	if innerWidth < 10 {
		innerWidth = 10
	}
	m.bugreports.width = innerWidth
	m.bugreports.height = m.height - 4 // account for border

	content := m.bugreports.View()

	// Error line.
	if m.err != nil {
		content += "\n" + lipglossRender(colorDanger, fmt.Sprintf("Error: %v", m.err))
	}

	return panelStyle.
		Width(m.width - 2).
		Height(m.height - 2).
		BorderForeground(colorPrimary).
		Render(content)
}
