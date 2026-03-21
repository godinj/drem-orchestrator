package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/model"
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

// taskActionBindings returns the action bindings available for a given task
// based on its status and assigned agent.
func (m Model) taskActionBindings(task *model.Task) []helpBinding {
	var bindings []helpBinding

	switch task.Status {
	case model.StatusPlanReview:
		bindings = append(bindings,
			helpBinding{"a", "approve plan"},
			helpBinding{"r", "reject plan"},
			helpBinding{"v", "spawn reviewer"},
			helpBinding{"c", "add comment"},
			helpBinding{"d", "delete item"},
		)
	case model.StatusTestingReady:
		bindings = append(bindings,
			helpBinding{"t", "pass test"},
			helpBinding{"f", "fail test"},
			helpBinding{"v", "spawn reviewer"},
			helpBinding{"x", "spawn fixer"},
			helpBinding{"c", "add comment"},
			helpBinding{"d", "delete item"},
		)
	case model.StatusInProgress:
		bindings = append(bindings,
			helpBinding{"p", "pause"},
			helpBinding{"x", "spawn fixer"},
			helpBinding{"d", "delete item"},
		)
	case model.StatusPaused:
		bindings = append(bindings, helpBinding{"p", "resume"})
	case model.StatusFailed:
		bindings = append(bindings,
			helpBinding{"R", "retry"},
			helpBinding{"x", "spawn fixer"},
		)
	}

	bindings = append(bindings, helpBinding{"S", "supervisor eval"})

	if m.detail.agent != nil {
		bindings = append(bindings, helpBinding{"l", "view agent log"})
		bindings = append(bindings, helpBinding{"g", "jump to agent"})
	}

	bindings = append(bindings,
		helpBinding{"L", "orchestrator log"},
		helpBinding{"i", "open shell"},
	)

	return bindings
}

// renderStatusBar shows task counts per status.
func (m Model) renderStatusBar() string {
	counts := make(map[model.TaskStatus]int)
	for _, task := range m.board.tasks {
		counts[task.Status]++
	}

	order := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusPlanReview,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusMerging,
		model.StatusDone,
		model.StatusFailed,
	}

	var badges []string
	for _, s := range order {
		c := counts[s]
		if c == 0 {
			continue
		}
		color, ok := statusColors[s]
		if !ok {
			color = lipgloss.Color("241")
		}
		badge := lipgloss.NewStyle().
			Foreground(color).
			Render(fmt.Sprintf("[%s: %d]", strings.ToTitle(string(s)), c))
		badges = append(badges, badge)
	}

	if len(badges) == 0 {
		return subtitleStyle.Render("  No tasks")
	}
	return "  " + strings.Join(badges, " ")
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
		title = "Help — Tasks"
		if m.detail.deleteMode {
			title = "Help — Delete Mode"
		}
	case FocusAgents:
		title = "Help — Agents"
	case FocusDetail:
		title = "Help — Detail"
		if m.detail.deleteMode {
			title = "Help — Delete Mode"
		}
	case FocusCreate:
		title = "Help — New Task"
	case FocusFeedback:
		title = "Help — Feedback"
	case FocusBugReports:
		title = "Help — Bug Reports"
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
