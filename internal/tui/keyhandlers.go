package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// handleKey dispatches key messages based on the current focus.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c is suppressed — exit by killing the tmux session.
	if msg.String() == "ctrl+c" {
		return m, nil
	}

	// Help overlay toggle.
	if msg.String() == "?" {
		m.showHelp = !m.showHelp
		return m, nil
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil // dismiss overlay and consume the key
	}

	switch m.focus {
	case FocusCreate:
		return m.handleCreateKeys(msg)
	case FocusFeedback:
		return m.handleFeedbackKeys(msg)
	case FocusBugReports:
		return m.handleBugReportKeys(msg)
	case FocusAgents:
		return m.handleAgentKeys(msg)
	case FocusDetail:
		return m.handleDetailKeys(msg)
	default:
		return m.handleBoardKeys(msg)
	}
}

// handleBoardKeys handles keys when the board panel is focused.
func (m Model) handleBoardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Delete-comment selection mode intercepts all keys.
	if m.detail.deleteMode {
		return m.handleDeleteModeKeys(msg)
	}

	switch msg.String() {
	case "j", "down", "k", "up":
		prevCursor := m.board.cursor
		var cmd tea.Cmd
		m.board, cmd = m.board.Update(msg)
		// Only refresh when the cursor actually moved; pressing j at the
		// bottom (or k at the top) should be a no-op to avoid redundant
		// async refreshes that can re-sort the board mid-view.
		if m.board.cursor != prevCursor {
			m.updateDetail()
			return m, tea.Batch(cmd, m.refreshData())
		}
		return m, cmd

	case "tab", "ctrl+l":
		m.focus = FocusAgents
		return m, nil
	case "shift+tab", "btab":
		m.focus = FocusDetail
		return m, nil
	case "ctrl+j":
		m.focus = FocusDetail
		return m, nil

	case "enter":
		// Drill into the detail panel for the currently selected task.
		m.focus = FocusDetail
		return m, nil

	case "esc":
		// No-op on board — board is the home panel. Clear any error.
		m.err = nil
		return m, nil

	case " ":
		m.toggleBoardCollapse()
		m.board.adjustScroll()
		return m, nil

	case "n":
		m.focus = FocusCreate
		m.create.Reset()
		return m, nil

	case "a":
		return m.handleApprove()
	case "r":
		return m.handleReject()
	case "t":
		return m.handleTestPass()
	case "f":
		return m.handleTestFail()
	case "p":
		return m.handlePauseResume()
	case "R":
		return m.handleRetry()
	case "g":
		return m.handleJump()
	case "l":
		return m.handleLog()
	case "L":
		return m.handleOrchLog()
	case "c":
		return m.handleAddComment()
	case "d":
		return m.handleDeleteTask()
	case "S":
		return m.handleSupervisorEval()
	case "v":
		return m.handleReviewerEval()
	case "x":
		return m.handleFixerEval()
	case "X":
		// Reconcile disabled pending overhaul — see feature/reconcile-overhaul.md
		return m, nil
	case "i":
		return m.handleShell()
	case "C":
		return m.handleReap()
	case "A":
		m.agents.showArchived = !m.agents.showArchived
		m.agents.clampAgentCursor()
		return m, nil
	case "F":
		m.agents.autoFilter = !m.agents.autoFilter
		m.agents.clampAgentCursor()
		return m, nil
	case "b":
		m.focus = FocusBugReports
		return m, m.loadBugReports()
	}

	return m, nil
}

// handleAgentKeys handles keys when the agent panel is focused.
func (m Model) handleAgentKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down", "k", "up":
		var cmd tea.Cmd
		m.agents, cmd = m.agents.Update(msg)
		return m, cmd

	case "enter":
		// Drill into the selected agent: jump to its tmux session if alive,
		// otherwise fall through to the detail panel.
		if ag := m.agents.Selected(); ag != nil {
			if ag.TmuxSession != "" {
				alive, _ := m.tmux.IsAgentSessionAlive(ag.TmuxSession)
				if alive {
					_ = m.tmux.FocusAgentSession(ag.TmuxSession)
					return m, nil
				}
			}
		}
		// No live session — drill into detail panel.
		m.focus = FocusDetail
		return m, nil

	case "esc":
		// Go back to the board (overview).
		m.focus = FocusBoard
		return m, nil

	case "tab":
		m.focus = FocusDetail
		return m, nil
	case "shift+tab", "btab", "ctrl+h":
		m.focus = FocusBoard
		return m, nil
	case "ctrl+j":
		m.focus = FocusDetail
		return m, nil

	case "g":
		// Jump to the selected agent's tmux session (supervisors only)
		// or show log for headless agents.
		if ag := m.agents.Selected(); ag != nil {
			if ag.TmuxSession != "" {
				alive, _ := m.tmux.IsAgentSessionAlive(ag.TmuxSession)
				if alive {
					_ = m.tmux.FocusAgentSession(ag.TmuxSession)
					return m, nil
				}
			}
			// For headless agents or dead sessions, no action from agent panel.
		}
		return m, nil

	case "A":
		m.agents.showArchived = !m.agents.showArchived
		m.agents.clampAgentCursor()
		return m, nil
	case "F":
		m.agents.autoFilter = !m.agents.autoFilter
		m.agents.clampAgentCursor()
		return m, nil
	}

	return m, nil
}

// handleDetailKeys handles keys when the detail panel is focused.
// Task-action keys work the same as the board; j/k scroll the detail content.
func (m Model) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.detail.deleteMode {
		return m.handleDeleteModeKeys(msg)
	}

	switch msg.String() {
	case "esc":
		// Go back to the board (overview).
		m.focus = FocusBoard
		m.detail.scrollOffset = 0
		return m, nil
	case "tab":
		m.focus = FocusBoard
		return m, nil
	case "shift+tab", "btab":
		m.focus = FocusAgents
		return m, nil
	case "ctrl+k":
		m.focus = FocusBoard
		return m, nil
	case "ctrl+h":
		m.focus = FocusBoard
		return m, nil
	case "ctrl+l":
		m.focus = FocusAgents
		return m, nil
	case "j", "down":
		m.detail.scrollOffset++
		return m, nil
	case "k", "up":
		if m.detail.scrollOffset > 0 {
			m.detail.scrollOffset--
		}
		return m, nil
	case "a":
		return m.handleApprove()
	case "r":
		return m.handleReject()
	case "t":
		return m.handleTestPass()
	case "f":
		return m.handleTestFail()
	case "p":
		return m.handlePauseResume()
	case "R":
		return m.handleRetry()
	case "g":
		return m.handleJump()
	case "l":
		return m.handleLog()
	case "L":
		return m.handleOrchLog()
	case "c":
		return m.handleAddComment()
	case "d":
		return m.handleDelete()
	case "S":
		return m.handleSupervisorEval()
	case "v":
		return m.handleReviewerEval()
	case "x":
		return m.handleFixerEval()
	case "i":
		return m.handleShell()
	}

	return m, nil
}

// handleCreateKeys handles keys when the create form is focused.
func (m Model) handleCreateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.focus = FocusBoard
		return m, nil

	case "enter":
		title, desc, _ := m.create.Value()
		if title == "" {
			m.create.err = fmt.Errorf("title is required")
			return m, nil
		}
		if desc == "" {
			desc = title // Use title as description if empty.
		}
		_, err := m.orch.CreateTask(title, desc, 0)
		if err != nil {
			m.create.err = err
			return m, nil
		}
		m.focus = FocusBoard
		return m, m.refreshData()
	}

	var cmd tea.Cmd
	m.create, cmd = m.create.Update(msg)
	return m, cmd
}

// handleFeedbackKeys handles keys when the feedback dialog is focused.
func (m Model) handleFeedbackKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		wasBugReport := m.feedbackAction == feedbackBugReportComment
		m.feedback.Hide()
		m.feedbackAction = feedbackNone
		if wasBugReport {
			m.focus = FocusBugReports
		} else {
			m.focus = FocusBoard
		}
		return m, nil

	case "enter":
		body := m.feedback.Value()

		// Bug report comments use a different model (not board.Selected).
		if m.feedbackAction == feedbackBugReportComment {
			bugReport := m.bugreports.Selected()
			if bugReport != nil && m.bugreportSvc != nil && body != "" {
				if err := m.bugreportSvc.AddComment(bugReport.ID, "user", body); err != nil {
					m.err = err
				}
			}
			m.feedback.Hide()
			m.feedbackAction = feedbackNone
			m.focus = FocusBugReports
			return m, m.loadBugReports()
		}

		selected := m.board.Selected()
		if selected == nil || body == "" {
			m.feedback.Hide()
			m.feedbackAction = feedbackNone
			m.focus = FocusBoard
			return m, nil
		}

		switch m.feedbackAction {
		case feedbackAddComment:
			if err := m.orch.AddComment(selected.ID, "user", body); err != nil {
				m.err = err
			}
		case feedbackClarificationAnswer:
			// Route to clarification handler and also save as a comment for visibility.
			if err := m.orch.HandleClarificationAnswer(selected.ID, body); err != nil {
				m.err = err
			}
			if err := m.orch.AddComment(selected.ID, "user", body); err != nil {
				m.err = err
			}
		case feedbackTestReviewReject:
			if err := m.orch.HandleTestReviewRejected(selected.ID, body); err != nil {
				m.err = err
			}
		}

		m.feedback.Hide()
		m.feedbackAction = feedbackNone
		m.focus = FocusBoard

		return m, m.refreshData()
	}

	var cmd tea.Cmd
	m.feedback, cmd = m.feedback.Update(msg)
	return m, cmd
}

// handleDeleteModeKeys handles keys while in delete selection mode.
func (m Model) handleDeleteModeKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.detail.deletableItems()
	switch msg.String() {
	case "esc":
		m.detail.deleteMode = false
		return m, nil
	case "j", "down":
		if m.detail.deleteCursor < len(items)-1 {
			m.detail.deleteCursor++
		}
		return m, nil
	case "k", "up":
		if m.detail.deleteCursor > 0 {
			m.detail.deleteCursor--
		}
		return m, nil
	case "enter", "y":
		if m.detail.deleteCursor < 0 || m.detail.deleteCursor >= len(items) {
			m.detail.deleteMode = false
			return m, nil
		}
		item := items[m.detail.deleteCursor]
		m.detail.deleteMode = false

		// Run deletion async to avoid blocking the TUI event loop
		// (StopAgent can block on tmux kills and semaphore drains).
		orch := m.orch
		switch item.kind {
		case deleteItemComment:
			commentID := m.detail.comments[item.index].ID
			return m, func() tea.Msg {
				return deleteResultMsg{err: orch.DeleteComment(commentID)}
			}
		case deleteItemPlanStep:
			taskID := m.detail.task.ID
			stepIdx := item.index
			return m, func() tea.Msg {
				return deleteResultMsg{err: orch.DeletePlanStep(taskID, stepIdx)}
			}
		case deleteItemTask:
			taskID := m.detail.task.ID
			return m, func() tea.Msg {
				return deleteResultMsg{err: orch.DeleteTask(taskID)}
			}
		}
	}
	return m, nil
}
