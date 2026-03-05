package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	tmuxpkg "github.com/godinj/drem-orchestrator/internal/tmux"
)

// Focus tracks which panel has keyboard focus.
type Focus int

const (
	// FocusBoard means the task list panel is focused.
	FocusBoard Focus = iota
	// FocusAgents means the agent list panel is focused.
	FocusAgents
	// FocusDetail means the detail panel is focused.
	FocusDetail
	// FocusCreate means the new-task form is focused.
	FocusCreate
	// FocusFeedback means the feedback dialog is focused.
	FocusFeedback
)

// EventMsg wraps an orchestrator Event as a tea.Msg.
type EventMsg orchestrator.Event

// tasksLoadedMsg is sent when the initial task load completes.
type tasksLoadedMsg struct {
	tasks []model.Task
}

// agentsLoadedMsg is sent when the initial agent load completes.
type agentsLoadedMsg struct {
	agents []model.Agent
}

// dataRefreshedMsg is sent after a data refresh from DB completes.
type dataRefreshedMsg struct {
	tasks    []model.Task
	agents   []model.Agent
	subtasks []model.Task
	agent    *model.Agent
	comments []model.TaskComment
}

// logCapturedMsg carries captured tmux pane output.
type logCapturedMsg struct {
	text string
	err  error
}

// orchLogCapturedMsg carries orchestrator log file content.
type orchLogCapturedMsg struct {
	text string
	err  error
}

// supervisorEvalMsg carries the result of an on-demand supervisor evaluation.
type supervisorEvalMsg struct {
	eval *supervisor.OnDemandEvaluation
	err  error
}

// feedbackAction tracks what action triggered the feedback dialog.
type feedbackAction int

const (
	feedbackNone       feedbackAction = iota
	feedbackAddComment                // add comment to task
)

// Model is the root Bubble Tea model that composes all TUI sub-models.
type Model struct {
	db        *gorm.DB
	orch      *orchestrator.Orchestrator
	tmux      *tmuxpkg.Manager
	projectID uuid.UUID
	events    <-chan orchestrator.Event

	board    BoardModel
	agents   AgentsModel
	detail   DetailModel
	create   CreateModel
	feedback FeedbackModel

	logPath        string
	focus          Focus
	feedbackAction feedbackAction
	keys           keyMap
	width          int
	height         int
	err            error
}

// NewModel creates the root TUI model.
func NewModel(
	db *gorm.DB,
	orch *orchestrator.Orchestrator,
	tmux *tmuxpkg.Manager,
	projectID uuid.UUID,
	events <-chan orchestrator.Event,
	logPath string,
) Model {
	return Model{
		db:        db,
		orch:      orch,
		tmux:      tmux,
		projectID: projectID,
		events:    events,
		logPath:   logPath,
		board:     NewBoardModel(),
		agents:    NewAgentsModel(),
		detail:    NewDetailModel(),
		create:    NewCreateModel(),
		feedback:  NewFeedbackModel("Feedback"),
		focus:     FocusBoard,
		keys:      defaultKeyMap(),
	}
}

// Init returns the initial commands: load tasks, load agents, listen for events.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadTasks(),
		m.loadAgents(),
		listenForEvents(m.events),
	)
}

// Update processes messages and returns the updated model and any commands.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updatePanelSizes()
		return m, nil

	case tasksLoadedMsg:
		m.board.tasks = msg.tasks
		m.clampCursor()
		m.updateDetail()
		return m, m.refreshData()

	case agentsLoadedMsg:
		m.agents.agents = msg.agents
		return m, nil

	case dataRefreshedMsg:
		m.board.tasks = msg.tasks
		m.agents.agents = msg.agents
		m.detail.subtasks = msg.subtasks
		m.detail.agent = msg.agent
		m.detail.comments = msg.comments
		m.clampCursor()
		m.updateDetail() // also refreshes agent task filter with new subtasks
		return m, nil

	case EventMsg:
		// Orchestrator event: refresh data and re-listen.
		return m, tea.Batch(m.refreshData(), listenForEvents(m.events))

	case logCapturedMsg:
		if msg.err != nil {
			m.detail.logText = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.detail.logText = msg.text
		}
		return m, nil

	case orchLogCapturedMsg:
		if msg.err != nil {
			m.detail.logText = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.detail.logText = msg.text
		}
		return m, nil

	case supervisorEvalMsg:
		if msg.err != nil {
			m.detail.supervisorText = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.detail.supervisorText = formatSupervisorEval(msg.eval)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// handleKey dispatches key messages based on the current focus.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global quit.
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.focus {
	case FocusCreate:
		return m.handleCreateKeys(msg)
	case FocusFeedback:
		return m.handleFeedbackKeys(msg)
	case FocusAgents:
		return m.handleAgentKeys(msg)
	default:
		return m.handleBoardKeys(msg)
	}
}

// handleBoardKeys handles keys when the board panel is focused.
func (m Model) handleBoardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit

	case "j", "down", "k", "up":
		var cmd tea.Cmd
		m.board, cmd = m.board.Update(msg)
		m.updateDetail()
		return m, cmd

	case "tab":
		m.focus = FocusAgents
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
		return m.handleDeleteComment()
	case "S":
		return m.handleSupervisorEval()
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

// handleAgentKeys handles keys when the agent panel is focused.
func (m Model) handleAgentKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit

	case "j", "down", "k", "up":
		var cmd tea.Cmd
		m.agents, cmd = m.agents.Update(msg)
		return m, cmd

	case "tab":
		m.focus = FocusBoard
		return m, nil

	case "g":
		// Jump to the selected agent's tmux session.
		if ag := m.agents.Selected(); ag != nil && ag.TmuxSession != "" {
			_ = m.tmux.FocusAgentSession(ag.TmuxSession)
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

// handleCreateKeys handles keys when the create form is focused.
func (m Model) handleCreateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.focus = FocusBoard
		return m, nil

	case "enter":
		title, desc := m.create.Value()
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
		m.feedback.Hide()
		m.feedbackAction = feedbackNone
		m.focus = FocusBoard
		return m, nil

	case "enter":
		body := m.feedback.Value()
		selected := m.board.Selected()
		if selected == nil || body == "" {
			m.feedback.Hide()
			m.feedbackAction = feedbackNone
			m.focus = FocusBoard
			return m, nil
		}

		if m.feedbackAction == feedbackAddComment {
			if err := m.orch.AddComment(selected.ID, "user", body); err != nil {
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

// handleApprove approves a plan (PLAN_REVIEW) or starts testing (TESTING_READY).
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
	case model.StatusTestingReady:
		if err := m.orch.HandleStartTesting(selected.ID); err != nil {
			m.err = err
		}
	default:
		return m, nil
	}
	return m, m.refreshData()
}

// handleReject rejects the plan and transitions back to PLANNING.
func (m Model) handleReject() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || selected.Status != model.StatusPlanReview {
		return m, nil
	}
	if err := m.orch.HandlePlanRejected(selected.ID); err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handleTestPass passes a test if the selected task is in MANUAL_TESTING.
func (m Model) handleTestPass() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || selected.Status != model.StatusManualTesting {
		return m, nil
	}
	if err := m.orch.HandleTestPassed(selected.ID); err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handleTestFail fails the test and transitions back to IN_PROGRESS.
func (m Model) handleTestFail() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || selected.Status != model.StatusManualTesting {
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

// handleJump focuses the tmux window of the selected task's agent.
func (m Model) handleJump() (tea.Model, tea.Cmd) {
	ag := m.detail.agent
	if ag == nil || ag.TmuxSession == "" {
		if m.detail.task != nil && m.detail.task.Status == model.StatusPlanReview {
			m.err = fmt.Errorf("agent session ended; plan is shown in the detail panel")
		} else {
			m.err = fmt.Errorf("no agent assigned to this task")
		}
		return m, nil
	}
	// Verify the session still exists before attempting to focus it.
	alive, err := m.tmux.IsAgentSessionAlive(ag.TmuxSession)
	if err != nil || !alive {
		m.err = fmt.Errorf("agent session %q no longer exists", ag.TmuxSession)
		return m, nil
	}
	if err := m.tmux.FocusAgentSession(ag.TmuxSession); err != nil {
		m.err = fmt.Errorf("jump to agent: %w", err)
	}
	return m, nil
}

// handleLog captures the pane output from the selected task's agent.
func (m Model) handleLog() (tea.Model, tea.Cmd) {
	if m.detail.agent == nil || m.detail.agent.TmuxSession == "" {
		return m, nil
	}
	sessionName := m.detail.agent.TmuxSession
	tmuxMgr := m.tmux
	return m, func() tea.Msg {
		text, err := tmuxMgr.CaptureAgentPane(sessionName, 50)
		return logCapturedMsg{text: text, err: err}
	}
}

// handleOrchLog reads the tail of the orchestrator log file.
func (m Model) handleOrchLog() (tea.Model, tea.Cmd) {
	logPath := m.logPath
	return m, func() tea.Msg {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return orchLogCapturedMsg{err: err}
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > 50 {
			lines = lines[len(lines)-50:]
		}
		return orchLogCapturedMsg{text: strings.Join(lines, "\n")}
	}
}

// handleAddComment opens the feedback dialog to add a comment.
func (m Model) handleAddComment() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || !selected.Status.IsHumanGate() {
		return m, nil
	}
	m.feedback = NewFeedbackModel("Add Comment")
	m.feedback.Show()
	m.feedbackAction = feedbackAddComment
	m.focus = FocusFeedback
	return m, nil
}

// handleDeleteComment deletes the last comment in the thread (LIFO).
func (m Model) handleDeleteComment() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil || !selected.Status.IsHumanGate() {
		return m, nil
	}
	if len(m.detail.comments) == 0 {
		return m, nil
	}
	last := m.detail.comments[len(m.detail.comments)-1]
	if err := m.orch.DeleteComment(last.ID); err != nil {
		m.err = err
	}
	return m, m.refreshData()
}

// handleSupervisorEval triggers an on-demand supervisor evaluation for the
// selected task.
func (m Model) handleSupervisorEval() (tea.Model, tea.Cmd) {
	selected := m.board.Selected()
	if selected == nil {
		return m, nil
	}
	m.detail.supervisorText = "Evaluating..."
	orch := m.orch
	taskID := selected.ID
	return m, func() tea.Msg {
		eval, err := orch.SupervisorEvaluate(taskID)
		return supervisorEvalMsg{eval: eval, err: err}
	}
}

// formatSupervisorEval formats a supervisor evaluation for display.
func formatSupervisorEval(eval *supervisor.OnDemandEvaluation) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("[%s] %s", strings.ToUpper(eval.Severity), eval.Summary))
	if len(eval.Issues) > 0 {
		lines = append(lines, "Issues:")
		for _, issue := range eval.Issues {
			lines = append(lines, "  - "+issue)
		}
	}
	if len(eval.RecommendedSteps) > 0 {
		lines = append(lines, "Steps:")
		for i, step := range eval.RecommendedSteps {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, step))
		}
	}
	return strings.Join(lines, "\n")
}

// View renders the entire TUI layout.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	// If the create form or feedback dialog is visible, show them as overlays.
	if m.focus == FocusCreate {
		return m.renderOverlay(m.create.View())
	}
	if m.focus == FocusFeedback {
		return m.renderOverlay(m.feedback.View())
	}

	// Title bar.
	titleBar := titleStyle.Render("Drem Orchestrator")

	// Status bar with task counts per status.
	statusBar := m.renderStatusBar()

	// Help bar at the bottom.
	helpBar := m.renderHelpBar()

	// Calculate panel heights.
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

	// Update panel sizes.
	m.board.width = tasksWidth - 4  // Account for panel border + padding.
	m.board.height = upperHeight - 2
	m.agents.width = agentsWidth - 4
	m.agents.height = upperHeight - 2
	m.detail.width = innerWidth - 4
	m.detail.height = detailHeight - 2

	// Render panels.
	boardLabel := " Tasks "
	if m.focus == FocusBoard {
		boardLabel = " Tasks (active) "
	}
	tasksPanel := panelStyle.
		Width(tasksWidth).
		Height(upperHeight).
		BorderForeground(m.panelBorderColor(FocusBoard)).
		Render(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(boardLabel) + "\n" + m.board.View())

	agentsLabel := " Agents "
	var tags []string
	if m.agents.showArchived {
		tags = append(tags, "+archived")
	}
	if !m.agents.autoFilter {
		tags = append(tags, "all")
	}
	if len(tags) > 0 {
		agentsLabel = fmt.Sprintf(" Agents [%s] ", strings.Join(tags, " "))
	}
	if m.focus == FocusAgents {
		agentsLabel = strings.TrimSuffix(agentsLabel, " ") + " (active) "
	}
	agentsPanel := panelStyle.
		Width(agentsWidth).
		Height(upperHeight).
		BorderForeground(m.panelBorderColor(FocusAgents)).
		Render(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(agentsLabel) + "\n" + m.agents.View())

	upperRow := lipgloss.JoinHorizontal(lipgloss.Top, tasksPanel, agentsPanel)

	detailPanel := panelStyle.
		Width(innerWidth).
		Height(detailHeight).
		BorderForeground(m.panelBorderColor(FocusDetail)).
		Render(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(" Detail ") + "\n" + m.detail.View())

	// Error line.
	errLine := ""
	if m.err != nil {
		errLine = lipglossRender(colorDanger, fmt.Sprintf("Error: %v", m.err))
	}

	// Compose.
	parts := []string{
		titleBar,
		statusBar,
		upperRow,
		detailPanel,
	}
	if errLine != "" {
		parts = append(parts, errLine)
	}
	parts = append(parts, helpBar)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
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
		model.StatusManualTesting,
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

// renderHelpBar shows the available key bindings.
func (m Model) renderHelpBar() string {
	return helpStyle.Render("  j/k:navigate  tab:panel  a:approve  r:reject  t:pass  f:fail  c:comment  d:del-comment  p:pause  R:retry  S:supervisor  g:jump  l:log  L:orch-log  A:archive  F:filter  n:new  q:quit")
}

// renderOverlay renders content as a centered overlay on a blank screen.
func (m Model) renderOverlay(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		panelStyle.Width(m.width*2/3).Render(content),
	)
}

// panelBorderColor returns the border color for a panel based on focus.
func (m Model) panelBorderColor(panel Focus) lipgloss.Color {
	if m.focus == panel {
		return colorPrimary
	}
	return lipgloss.Color("238")
}

// updatePanelSizes recalculates panel dimensions after a window resize.
func (m *Model) updatePanelSizes() {
	// Sizes are computed dynamically in View(), so this is a placeholder
	// for any future pre-computation.
}

// updateDetail refreshes the detail panel based on the currently selected task.
func (m *Model) updateDetail() {
	selected := m.board.Selected()
	m.detail.task = selected
	m.detail.logText = ""
	m.detail.supervisorText = ""
	// Update agent task filter from selected task and known subtasks.
	var taskID *uuid.UUID
	var subtaskIDs []uuid.UUID
	if selected != nil {
		taskID = &selected.ID
		for _, st := range m.detail.subtasks {
			subtaskIDs = append(subtaskIDs, st.ID)
		}
	}
	m.agents.setTaskFilter(taskID, subtaskIDs)
}

// clampCursor ensures the board cursor doesn't exceed the display list length.
func (m *Model) clampCursor() {
	count := len(m.board.buildDisplayList())
	if m.board.cursor >= count {
		m.board.cursor = count - 1
	}
	if m.board.cursor < 0 {
		m.board.cursor = 0
	}
}

// listenForEvents returns a Cmd that blocks on the events channel and wraps
// the received orchestrator Event as a tea.Msg.
func listenForEvents(events <-chan orchestrator.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-events
		if !ok {
			return nil
		}
		return EventMsg(e)
	}
}

// loadTasks returns a Cmd that queries all tasks for the project from DB.
func (m Model) loadTasks() tea.Cmd {
	db := m.db
	projectID := m.projectID
	return func() tea.Msg {
		var tasks []model.Task
		db.Where("project_id = ?", projectID).
			Order("priority desc, created_at").
			Find(&tasks)
		return tasksLoadedMsg{tasks: tasks}
	}
}

// loadAgents returns a Cmd that queries all agents for the project from DB.
func (m Model) loadAgents() tea.Cmd {
	db := m.db
	projectID := m.projectID
	return func() tea.Msg {
		var agents []model.Agent
		db.Where("project_id = ?", projectID).Find(&agents)
		return agentsLoadedMsg{agents: agents}
	}
}

// refreshData returns a Cmd that reloads tasks, agents, and detail context from DB.
func (m Model) refreshData() tea.Cmd {
	db := m.db
	projectID := m.projectID
	selectedTask := m.board.Selected()

	return func() tea.Msg {
		var tasks []model.Task
		db.Where("project_id = ?", projectID).
			Order("priority desc, created_at").
			Find(&tasks)

		var agents []model.Agent
		db.Where("project_id = ?", projectID).Find(&agents)

		var subtasks []model.Task
		var detailAgent *model.Agent
		var comments []model.TaskComment

		if selectedTask != nil {
			db.Where("parent_task_id = ?", selectedTask.ID).Find(&subtasks)
			db.Where("task_id = ?", selectedTask.ID).Order("created_at asc").Find(&comments)
			if selectedTask.AssignedAgentID != nil {
				var ag model.Agent
				if err := db.First(&ag, "id = ?", selectedTask.AssignedAgentID).Error; err == nil {
					detailAgent = &ag
				}
			}
			// Fallback for plan_review tasks whose assignment was cleared:
			// find the project's planner agent so the user can still jump
			// to its window (if it exists).
			if detailAgent == nil && selectedTask.Status == model.StatusPlanReview {
				var ag model.Agent
				if err := db.Where("project_id = ? AND agent_type = ? AND tmux_session != ''",
					projectID, model.AgentPlanner).
					Order("updated_at desc").First(&ag).Error; err == nil {
					detailAgent = &ag
				}
			}
		}

		return dataRefreshedMsg{
			tasks:    tasks,
			agents:   agents,
			subtasks: subtasks,
			agent:    detailAgent,
			comments: comments,
		}
	}
}
