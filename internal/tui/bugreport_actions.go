package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

// handleBugReportKeys dispatches keys when the bug report screen is focused.
func (m Model) handleBugReportKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Delete confirmation mode intercepts all keys.
	if m.bugreports.confirmDelete {
		switch msg.String() {
		case "y":
			m.bugreports.confirmDelete = false
			selected := m.bugreports.Selected()
			if selected == nil {
				return m, nil
			}
			svc := m.bugreportSvc
			id := selected.ID
			return m, func() tea.Msg {
				if svc == nil {
					return bugReportActionMsg{err: fmt.Errorf("bug report service not available")}
				}
				return bugReportActionMsg{err: svc.Delete(id)}
			}
		case "n", "esc":
			m.bugreports.confirmDelete = false
			return m, nil
		}
		return m, nil
	}

	// Filter mode intercepts keys.
	if m.bugreports.filterMode {
		return m.handleBugReportFilterKeys(msg)
	}

	switch msg.String() {
	case "j", "down":
		reports := m.bugreports.filteredReports()
		if m.bugreports.showDetail {
			// In detail view, j/k scrolls the detail pane.
			m.bugreports.detailScroll++
		} else if m.bugreports.cursor < len(reports)-1 {
			m.bugreports.cursor++
			m.bugreports.adjustScroll()
		}
		return m, nil

	case "k", "up":
		if m.bugreports.showDetail {
			if m.bugreports.detailScroll > 0 {
				m.bugreports.detailScroll--
			}
		} else if m.bugreports.cursor > 0 {
			m.bugreports.cursor--
			m.bugreports.adjustScroll()
		}
		return m, nil

	case "enter":
		// Toggle detail view.
		selected := m.bugreports.Selected()
		if selected == nil {
			return m, nil
		}
		if m.bugreports.showDetail && m.bugreports.selectedReport != nil && m.bugreports.selectedReport.ID == selected.ID {
			// Close detail.
			m.bugreports.showDetail = false
			m.bugreports.selectedReport = nil
			m.bugreports.comments = nil
			m.bugreports.detailScroll = 0
			return m, nil
		}
		// Open detail: load full report with comments.
		m.bugreports.showDetail = true
		m.bugreports.detailScroll = 0
		return m, m.loadBugReportDetail(selected.ID)

	case "a":
		// Acknowledge.
		selected := m.bugreports.Selected()
		if selected == nil {
			return m, nil
		}
		svc := m.bugreportSvc
		id := selected.ID
		return m, func() tea.Msg {
			if svc == nil {
				return bugReportActionMsg{err: fmt.Errorf("bug report service not available")}
			}
			return bugReportActionMsg{err: svc.Acknowledge(id)}
		}

	case "p":
		// Promote: spawn editor.
		selected := m.bugreports.Selected()
		if selected == nil {
			return m, nil
		}
		return m.handleBugReportPromote(selected)

	case "D":
		// Dismiss.
		selected := m.bugreports.Selected()
		if selected == nil {
			return m, nil
		}
		svc := m.bugreportSvc
		id := selected.ID
		return m, func() tea.Msg {
			if svc == nil {
				return bugReportActionMsg{err: fmt.Errorf("bug report service not available")}
			}
			return bugReportActionMsg{err: svc.Dismiss(id)}
		}

	case "x":
		// Hard-delete with confirmation.
		selected := m.bugreports.Selected()
		if selected == nil {
			return m, nil
		}
		m.bugreports.confirmDelete = true
		return m, nil

	case "c":
		// Add comment via feedback overlay.
		selected := m.bugreports.Selected()
		if selected == nil {
			return m, nil
		}
		m.feedback = NewFeedbackModel("Add Bug Report Comment")
		m.feedback.SetWidth(m.width*2/3 - 4)
		m.feedback.Show()
		m.feedbackAction = feedbackBugReportComment
		m.focus = FocusFeedback
		return m, nil

	case "/":
		// Toggle filter mode.
		m.bugreports.filterMode = !m.bugreports.filterMode
		m.bugreports.filterCursor = 0
		return m, nil

	case "b", "esc":
		// Return to main dashboard.
		m.focus = FocusBoard
		return m, nil
	}

	return m, nil
}

// handleBugReportFilterKeys handles keys in filter mode.
func (m Model) handleBugReportFilterKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		// Cycle filter dimensions (category, severity, status, dismissed).
		m.bugreports.filterCursor = (m.bugreports.filterCursor + 1) % 4
		return m, nil

	case "shift+tab", "btab":
		m.bugreports.filterCursor = (m.bugreports.filterCursor + 3) % 4
		return m, nil

	case "j", "down":
		// Cycle value forward within current dimension.
		m.cycleBugReportFilter(1)
		return m, nil

	case "k", "up":
		// Cycle value backward within current dimension.
		m.cycleBugReportFilter(-1)
		return m, nil

	case "enter":
		// Apply filters and close filter mode.
		m.bugreports.filterMode = false
		m.bugreports.cursor = 0
		m.bugreports.scrollOffset = 0
		return m, nil

	case "esc":
		// Cancel filter mode.
		m.bugreports.filterMode = false
		return m, nil
	}

	return m, nil
}

// cycleBugReportFilter cycles the current filter dimension's value.
func (m *Model) cycleBugReportFilter(dir int) {
	switch m.bugreports.filterCursor {
	case 0: // Category
		m.bugreports.filters.category = cycleEnumFilter(
			m.bugreports.filters.category, allBugCategories, dir)
	case 1: // Severity
		m.bugreports.filters.severity = cycleEnumFilter(
			m.bugreports.filters.severity, allBugSeverities, dir)
	case 2: // Status
		m.bugreports.filters.status = cycleEnumFilter(
			m.bugreports.filters.status, allBugStatuses, dir)
	case 3: // Dismissed toggle
		m.bugreports.filters.showDismissed = !m.bugreports.filters.showDismissed
	}
}

// cycleEnumFilter cycles through an enum slice with a nil ("all") option.
// Returns the next value in the cycle, or nil for "all".
func cycleEnumFilter[T comparable](current *T, values []T, dir int) *T {
	if current == nil {
		// Currently "all" -- go to first or last value.
		if dir > 0 && len(values) > 0 {
			return &values[0]
		}
		if dir < 0 && len(values) > 0 {
			return &values[len(values)-1]
		}
		return nil
	}
	// Find current index.
	idx := -1
	for i, v := range values {
		if v == *current {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	next := idx + dir
	if next < 0 || next >= len(values) {
		return nil // wrap to "all"
	}
	return &values[next]
}

// handleEditorFinished processes the result of the promotion editor session.
func (m Model) handleEditorFinished(msg editorFinishedMsg) (tea.Model, tea.Cmd) {
	defer os.Remove(msg.tempFile)

	if msg.err != nil {
		m.err = fmt.Errorf("editor: %w", msg.err)
		return m, nil
	}

	// Read the edited temp file.
	data, err := os.ReadFile(msg.tempFile)
	if err != nil {
		m.err = fmt.Errorf("read temp file: %w", err)
		return m, nil
	}

	// Parse: first line = title, rest = description.
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	title := strings.TrimSpace(lines[0])
	var desc string
	if len(lines) > 1 {
		desc = strings.TrimSpace(lines[1])
	}

	if title == "" {
		m.err = fmt.Errorf("promotion cancelled: empty title")
		return m, nil
	}

	svc := m.bugreportSvc
	bugID := msg.bugReportID
	projectID := m.projectID

	return m, func() tea.Msg {
		if svc == nil {
			return bugReportActionMsg{err: fmt.Errorf("bug report service not available")}
		}
		_, err := svc.Promote(bugID, title, desc, projectID)
		return bugReportActionMsg{err: err}
	}
}

// loadBugReportDetail returns a Cmd that loads full detail for a bug report.
func (m Model) loadBugReportDetail(id uuid.UUID) tea.Cmd {
	svc := m.bugreportSvc
	return func() tea.Msg {
		if svc == nil {
			return bugReportDetailLoadedMsg{}
		}
		report, err := svc.Get(id)
		if err != nil {
			return bugReportDetailLoadedMsg{}
		}
		comments, err := svc.GetComments(id)
		if err != nil {
			comments = nil
		}
		return bugReportDetailLoadedMsg{report: report, comments: comments}
	}
}
