package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// helpBinding pairs a key label with a short description for help display.
type helpBinding struct {
	Key  string
	Desc string
}

// contextActions returns the keybindings relevant to the current focus and
// selection state. This makes the help overlay context-aware: users see only
// the actions that apply right now.
func (m Model) contextActions() []helpBinding {
	// Global bindings shown in every context.
	global := []helpBinding{
		{"?", "close help"},
		{"tmux", "kill tmux session to exit"},
	}

	// Navigation bindings common to most panels.
	nav := []helpBinding{
		{"j/k", "navigate"},
		{"tab", "next panel"},
		{"shift+tab", "prev panel"},
	}

	// Gate action confirmation mode.
	if m.confirm != confirmNone {
		label := confirmActionLabel(m.confirm)
		return append([]helpBinding{
			{"y", "confirm " + label},
			{"n", "cancel"},
			{"esc", "cancel"},
		}, global...)
	}

	switch m.focus {
	case FocusBoard:
		if m.detail.deleteMode {
			return append([]helpBinding{
				{"j/k", "navigate items"},
				{"enter/y", "confirm delete"},
				{"esc", "cancel"},
			}, global...)
		}

		bindings := append(nav, m.boardContextBindings()...)
		return append(bindings, global...)

	case FocusAgents:
		bindings := append(nav, []helpBinding{
			{"g", "jump to agent session"},
			{"A", "toggle archived agents"},
			{"F", "toggle task filter"},
		}...)
		return append(bindings, global...)

	case FocusDetail:
		if m.detail.deleteMode {
			return append([]helpBinding{
				{"j/k", "navigate items"},
				{"enter/y", "confirm delete"},
				{"esc", "cancel"},
			}, global...)
		}

		bindings := append(nav, m.detailContextBindings()...)
		return append(bindings, global...)

	case FocusCreate:
		return append([]helpBinding{
			{"enter", "submit new task"},
			{"tab", "next field"},
			{"esc", "cancel"},
		}, global...)

	case FocusFeedback:
		return append([]helpBinding{
			{"enter", "submit feedback"},
			{"esc", "cancel"},
		}, global...)

	case FocusBugReports:
		bindings := []helpBinding{
			{"j/k", "navigate"},
			{"enter", "toggle detail"},
			{"a", "acknowledge"},
			{"p", "promote to task"},
			{"D", "dismiss"},
			{"x", "delete (with confirm)"},
			{"c", "add comment"},
			{"/", "toggle filter"},
			{"b/esc", "back to dashboard"},
		}
		return append(bindings, global...)

	case FocusCsuite:
		bindings := []helpBinding{
			{"j/k", "navigate agents"},
			{"w/esc", "back to dashboard"},
		}
		return append(bindings, global...)
	}

	return global
}

// boardContextBindings returns action bindings for the board panel based on
// the currently selected task's status.
func (m Model) boardContextBindings() []helpBinding {
	var bindings []helpBinding

	// Panel navigation extras.
	bindings = append(bindings, helpBinding{"ctrl+j", "jump to detail"})
	bindings = append(bindings, helpBinding{"space", "expand/collapse"})

	selected := m.board.Selected()
	if selected != nil {
		bindings = append(bindings, m.taskActionBindings(selected)...)
	}

	// Always-available board actions.
	bindings = append(bindings,
		helpBinding{"n", "new task"},
		helpBinding{"b", "bug reports"},
		helpBinding{"w", "C-Suite dashboard"},
		helpBinding{"A", "toggle archived agents"},
		helpBinding{"F", "toggle task filter"},
		helpBinding{"C", "clean dead sessions"},
	)

	return bindings
}

// detailContextBindings returns action bindings for the detail panel based on
// the currently selected task's status.
func (m Model) detailContextBindings() []helpBinding {
	var bindings []helpBinding

	// Detail-specific navigation.
	bindings = append(bindings,
		helpBinding{"j/k", "scroll detail"},
		helpBinding{"esc", "back to board"},
		helpBinding{"ctrl+k", "jump to board"},
		helpBinding{"ctrl+h", "jump to board"},
		helpBinding{"ctrl+l", "jump to agents"},
	)

	selected := m.board.Selected()
	if selected != nil {
		bindings = append(bindings, m.taskActionBindings(selected)...)
	}

	return bindings
}

// renderHelpBar shows a minimal hint directing users to the help overlay.
func (m Model) renderHelpBar() string {
	return helpStyle.Render("  ? help")
}

// renderOverlay renders content as a centered overlay on a blank screen.
func (m Model) renderOverlay(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		panelStyle.Width(m.width*2/3).Render(content),
	)
}

// renderHelpOverlay renders a centered overlay showing context-aware keybindings.
func (m Model) renderHelpOverlay() string {
	actions := m.contextActions()

	// Title based on current focus.
	title := "Help"
	switch m.focus {
	case FocusBoard:
		title = "Help \u2014 Tasks"
		if m.detail.deleteMode {
			title = "Help \u2014 Delete Mode"
		}
	case FocusAgents:
		title = "Help \u2014 Agents"
	case FocusDetail:
		title = "Help \u2014 Detail"
		if m.detail.deleteMode {
			title = "Help \u2014 Delete Mode"
		}
	case FocusCreate:
		title = "Help \u2014 New Task"
	case FocusFeedback:
		title = "Help \u2014 Feedback"
	case FocusBugReports:
		title = "Help \u2014 Bug Reports"
	case FocusCsuite:
		title = "Help \u2014 C-Suite Dashboard"
	}

	titleRendered := titleStyle.Render(title)

	// Find the longest key string for alignment.
	maxKeyLen := 0
	for _, b := range actions {
		if len(b.Key) > maxKeyLen {
			maxKeyLen = len(b.Key)
		}
	}

	keyStyle := lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	var lines []string
	lines = append(lines, titleRendered, "")

	for _, b := range actions {
		padded := fmt.Sprintf("%-*s", maxKeyLen+1, b.Key)
		line := keyStyle.Render(padded) + "  " + descStyle.Render(b.Desc)
		lines = append(lines, "  "+line)
	}

	lines = append(lines, "", helpStyle.Render("  Press ? or esc to close"))

	content := strings.Join(lines, "\n")

	overlayWidth := m.width * 2 / 3
	if overlayWidth < 40 {
		overlayWidth = 40
	}
	if overlayWidth > m.width-4 {
		overlayWidth = m.width - 4
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		panelStyle.Width(overlayWidth).Render(content),
	)
}
