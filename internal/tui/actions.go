package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/bugreport"
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
// For tasks in StatusNeedsClarification, it opens as a clarification answer dialog.
func (m Model) handleAddComment() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || !selected.Status.IsHumanGate() {
		return m, nil
	}

	if selected.Status == model.StatusNeedsClarification {
		title := "Answer Clarification Question"
		if selected.Context != nil {
			if current, ok := selected.Context["clarification_current_question"].(string); ok {
				title = "Q: " + current
			}
		}
		m.feedback = NewFeedbackModel(title)
		m.feedback.SetWidth(m.width*2/3 - 4)
		m.feedback.Show()
		m.feedbackAction = feedbackClarificationAnswer
		m.focus = FocusFeedback
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

// bugReportsLoadedMsg is sent when the bug report list load completes.
type bugReportsLoadedMsg struct {
	reports []model.BugReport
}

// bugReportActionMsg is sent after a bug report action completes.
type bugReportActionMsg struct {
	err error
}

// bugReportDetailLoadedMsg is sent when a bug report's detail data is loaded.
type bugReportDetailLoadedMsg struct {
	report   *model.BugReport
	comments []model.BugReportComment
}

// editorFinishedMsg is sent when the external editor closes after promotion.
type editorFinishedMsg struct {
	tempFile    string
	bugReportID uuid.UUID
	err         error
}

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

// handleBugReportPromote starts the promotion workflow: create temp file, spawn editor.
func (m Model) handleBugReportPromote(report *model.BugReport) (tea.Model, tea.Cmd) {
	// Create temp file with title and description.
	tmpFile, err := os.CreateTemp("", "bugreport-promote-*.md")
	if err != nil {
		m.err = fmt.Errorf("create temp file: %w", err)
		return m, nil
	}

	content := report.Title + "\n\n" + report.Description
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		m.err = fmt.Errorf("write temp file: %w", err)
		return m, nil
	}
	tmpFile.Close()

	// Determine editor.
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	tmpPath := tmpFile.Name()
	bugReportID := report.ID

	// Use tea.ExecProcess to spawn the editor.
	editorCmd := exec.Command(editor, tmpPath)
	return m, tea.ExecProcess(editorCmd, func(err error) tea.Msg {
		return editorFinishedMsg{tempFile: tmpPath, bugReportID: bugReportID, err: err}
	})
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

// loadBugReports returns a Cmd that loads bug reports from the service.
func (m Model) loadBugReports() tea.Cmd {
	svc := m.bugreportSvc
	projectID := m.projectID
	return func() tea.Msg {
		if svc == nil {
			return bugReportsLoadedMsg{reports: nil}
		}
		reports, err := svc.List(bugreport.ListFilters{
			ProjectID: &projectID,
		})
		if err != nil {
			return bugReportsLoadedMsg{reports: nil}
		}
		return bugReportsLoadedMsg{reports: reports}
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
