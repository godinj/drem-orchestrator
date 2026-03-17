package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// handleApprove approves a plan (PLAN_REVIEW) or passes testing (TESTING_READY).
func (m Model) handleApprove() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	switch selected.Status {
	case model.StatusPlanReview:
		if err := m.orch.HandlePlanApproved(selected.ID); err != nil {
			m.err = err
		}
	case model.StatusTestReview:
		if err := m.orch.HandleTestReviewApproved(selected.ID); err != nil {
			m.err = err
		}
	case model.StatusTestingReady:
		if err := m.orch.HandleTestPassed(selected.ID); err != nil {
			m.err = err
		}
	default:
		return m, nil
	}
	return m, m.refreshData()
}

// handleReject rejects the plan (PLAN_REVIEW), fails testing (TESTING_READY),
// or rejects test review (TEST_REVIEW) with feedback.
func (m Model) handleReject() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	switch selected.Status {
	case model.StatusPlanReview:
		if err := m.orch.HandlePlanRejected(selected.ID); err != nil {
			m.err = err
		}
	case model.StatusTestReview:
		// Open feedback dialog for test review rejection (feedback required).
		m.feedback = NewFeedbackModel("Test Review Rejection Feedback")
		m.feedback.SetWidth(m.width*2/3 - 4)
		m.feedback.Show()
		m.feedbackAction = feedbackTestReviewReject
		m.focus = FocusFeedback
		return m, nil
	case model.StatusTestingReady:
		if err := m.orch.HandleTestFailed(selected.ID); err != nil {
			m.err = err
		}
	default:
		return m, nil
	}
	return m, m.refreshData()
}

// handleTestPass passes a test if the selected task is in TESTING_READY.
func (m Model) handleTestPass() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || selected.Status != model.StatusTestingReady {
		return m, nil
	}
	if err := m.orch.HandleTestPassed(selected.ID); err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handleTestFail fails the test and transitions back to PLANNING.
func (m Model) handleTestFail() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || selected.Status != model.StatusTestingReady {
		return m, nil
	}
	if err := m.orch.HandleTestFailed(selected.ID); err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handlePauseResume pauses or resumes the selected task.
func (m Model) handlePauseResume() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	var err error
	switch selected.Status {
	case model.StatusPaused:
		err = m.orch.ResumeTask(selected.ID)
	case model.StatusBacklog, model.StatusPlanning, model.StatusInProgress:
		err = m.orch.PauseTask(selected.ID)
	default:
		return m, nil
	}
	if err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handleRetry retries a failed task.
func (m Model) handleRetry() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || selected.Status != model.StatusFailed {
		return m, nil
	}
	if err := m.orch.RetryTask(selected.ID); err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handleJump focuses the tmux window of the selected task's agent (supervisors)
// or shows log output for headless agents.
func (m Model) handleJump() (tea.Model, tea.Cmd) {
	ag := m.detail.agent
	if ag == nil {
		if m.detail.task != nil && m.detail.task.Status == model.StatusPlanReview {
			m.err = fmt.Errorf("agent session ended; plan is shown in the detail panel")
		} else {
			m.err = fmt.Errorf("no agent assigned to this task")
		}
		return m, nil
	}
	// Headless agents don't have interactive tmux sessions.
	// Show their log output in the detail panel instead.
	return m.handleLog()
}

// handleLog reads the output log for the selected task's agent.
func (m Model) handleLog() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || m.detail.agent == nil {
		return m, nil
	}
	taskID := selected.ID
	agentID := m.detail.agent.ID
	orch := m.orch
	return m, func() tea.Msg {
		text, err := orch.GetAgentOutput(agentID)
		return logCapturedMsg{forTaskID: taskID, text: text, err: err}
	}
}

// handleOrchLog reads the tail of the orchestrator log file.
func (m Model) handleOrchLog() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	taskID := selected.ID
	logPath := m.logPath
	return m, func() tea.Msg {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return orchLogCapturedMsg{forTaskID: taskID, err: err}
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > 50 {
			lines = lines[len(lines)-50:]
		}
		return orchLogCapturedMsg{forTaskID: taskID, text: strings.Join(lines, "\n")}
	}
}

// handleAddComment opens the feedback dialog to add a comment.
func (m Model) handleAddComment() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || !selected.Status.IsHumanGate() {
		return m, nil
	}
	m.feedback = NewFeedbackModel("Add Comment")
	m.feedback.SetWidth(m.width*2/3 - 4)
	m.feedback.Show()
	m.feedbackAction = feedbackAddComment
	m.focus = FocusFeedback
	return m, nil
}

// handleDeleteTask deletes the currently selected task or subtask from the
// board. Parent tasks cascade through all subtasks.
func (m Model) handleDeleteTask() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	orch := m.orch
	taskID := selected.ID
	return m, func() tea.Msg {
		return deleteResultMsg{err: orch.DeleteTask(taskID)}
	}
}

// handleDelete enters delete mode, letting the user select a plan step,
// subtask, or comment to remove.
func (m Model) handleDelete() (tea.Model, tea.Cmd) {
	items := m.detail.deletableItems()
	if len(items) == 0 {
		return m, nil
	}
	m.detail.deleteMode = true
	m.detail.deleteCursor = len(items) - 1
	return m, nil
}

// deleteResultMsg carries the result of an async delete operation.
type deleteResultMsg struct {
	err error
}

// handleSupervisorEval spawns an interactive supervisor Claude session in tmux
// for the selected task and switches the user to it.
func (m Model) handleSupervisorEval() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	orch := m.orch
	taskID := selected.ID
	return m, func() tea.Msg {
		sessionName, err := orch.SpawnSupervisorSession(taskID)
		return supervisorSpawnedMsg{sessionName: sessionName, err: err}
	}
}

// handleReviewerEval spawns a reviewer agent for the selected task.
// Works on the parent task regardless of which row (parent or subtask) is
// selected. If the selected row is a subtask, the parent is looked up.
func (m Model) handleReviewerEval() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}

	// Resolve the parent task if a subtask is selected.
	task := selected
	if task.ParentTaskID != nil {
		var parent model.Task
		if err := m.db.First(&parent, "id = ?", task.ParentTaskID).Error; err == nil {
			task = &parent
		}
	}

	if task.Status != model.StatusPlanReview && task.Status != model.StatusTestingReady {
		m.err = fmt.Errorf("review requires plan_review or testing_ready status (task is %s)", task.Status)
		return m, nil
	}
	orch := m.orch
	taskID := task.ID
	return m, func() tea.Msg {
		sessionName, err := orch.SpawnReviewerSession(taskID)
		return reviewerSpawnedMsg{sessionName: sessionName, err: err}
	}
}

// handleFixerEval spawns a fixer agent for the selected task.
// Works on the parent task regardless of which row (parent or subtask) is
// selected. If the selected row is a subtask, the parent is looked up.
func (m Model) handleFixerEval() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}

	// Resolve the parent task if a subtask is selected.
	task := selected
	if task.ParentTaskID != nil {
		var parent model.Task
		if err := m.db.First(&parent, "id = ?", task.ParentTaskID).Error; err == nil {
			task = &parent
		}
	}

	switch task.Status {
	case model.StatusInProgress, model.StatusFailed, model.StatusTestingReady:
		// OK
	default:
		m.err = fmt.Errorf("fixer requires in_progress, failed, or testing_ready status (task is %s)", task.Status)
		return m, nil
	}
	orch := m.orch
	taskID := task.ID
	return m, func() tea.Msg {
		sessionName, err := orch.SpawnFixerSession(taskID)
		return fixerSpawnedMsg{sessionName: sessionName, err: err}
	}
}

// handleShell opens a new tmux session at the selected task's integration directory.
func (m Model) handleShell() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}

	path := m.orch.IntegrationWorktreePath(selected.ID)
	if path == "" {
		m.err = fmt.Errorf("no integration worktree for this task")
		return m, nil
	}

	sessionName := fmt.Sprintf("%s/shell %s", m.tmux.SessionName, selected.ID.String()[:4])
	if err := m.tmux.CreateShellSession(sessionName, path); err != nil {
		m.err = fmt.Errorf("open shell: %w", err)
		return m, nil
	}
	if err := m.tmux.FocusAgentSession(sessionName); err != nil {
		m.err = fmt.Errorf("focus shell: %w", err)
	}
	return m, nil
}

// reapMsg carries the result of a manual session reap.
type reapMsg struct {
	reaped int
	err    error
}

// handleReap cleans up dead tmux agent sessions on demand.
func (m Model) handleReap() (tea.Model, tea.Cmd) {
	orch := m.orch
	return m, func() tea.Msg {
		reaped, err := orch.ReapOrphanedSessions()
		return reapMsg{reaped: reaped, err: err}
	}
}
