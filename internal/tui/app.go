package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/bugreport"
	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
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
	// FocusBugReports means the bug report screen is focused.
	FocusBugReports
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
	tasks     []model.Task
	agents    []model.Agent
	forTaskID *uuid.UUID // which task the detail data was loaded for
	subtasks  []model.Task
	agent     *model.Agent
	comments  []model.TaskComment
	deps      []depInfo
}

// periodicRefreshMsg triggers a periodic data refresh from the DB.
type periodicRefreshMsg struct{}

// periodicRefreshInterval is how often the TUI re-reads agent data from the
// DB, so that continuously-updated fields like context_used_pct are visible
// without waiting for an orchestrator event.
const periodicRefreshInterval = 2 * time.Second

// logCapturedMsg carries captured tmux pane output.
type logCapturedMsg struct {
	forTaskID uuid.UUID
	text      string
	err       error
}

// orchLogCapturedMsg carries orchestrator log file content.
type orchLogCapturedMsg struct {
	forTaskID uuid.UUID
	text      string
	err       error
}

// supervisorSpawnedMsg carries the result of spawning a supervisor session.
type supervisorSpawnedMsg struct {
	sessionName string
	err         error
}

// reviewerSpawnedMsg carries the result of spawning a reviewer session.
type reviewerSpawnedMsg struct {
	sessionName string
	err         error
}

// fixerSpawnedMsg carries the result of spawning a fixer session.
type fixerSpawnedMsg struct {
	sessionName string
	err         error
}

// feedbackAction tracks what action triggered the feedback dialog.
type feedbackAction int

const (
	feedbackNone                feedbackAction = iota
	feedbackAddComment                         // add comment to task
	feedbackTestReviewReject                   // reject test review with feedback
	feedbackClarificationAnswer                // answer a clarification question
	feedbackBugReportComment                   // add comment to bug report
)

// Model is the root Bubble Tea model that composes all TUI sub-models.
type Model struct {
	db        *gorm.DB
	orch      *orchestrator.Orchestrator
	tmux      *tmuxpkg.Manager
	projectID uuid.UUID
	events    <-chan orchestrator.Event

	board      BoardModel
	agents     AgentsModel
	detail     DetailModel
	create     CreateModel
	feedback   FeedbackModel
	bugreports BugReportsModel

	bugreportSvc   *bugreport.Service
	logPath        string
	focus          Focus
	feedbackAction feedbackAction
	keys           keyMap
	showHelp       bool
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
	bugreportSvc *bugreport.Service,
) Model {
	return Model{
		db:           db,
		orch:         orch,
		tmux:         tmux,
		projectID:    projectID,
		events:       events,
		logPath:      logPath,
		bugreportSvc: bugreportSvc,
		board:        NewBoardModel(),
		agents:       NewAgentsModel(),
		detail:       NewDetailModel(),
		create:       NewCreateModel(),
		feedback:     NewFeedbackModel("Feedback"),
		bugreports:   NewBugReportsModel(db, bugreportSvc, projectID),
		focus:        FocusBoard,
		keys:         defaultKeyMap(),
	}
}

// Init returns the initial commands: load tasks, load agents, listen for events,
// and start the periodic refresh tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadTasks(),
		m.loadAgents(),
		listenForEvents(m.events),
		tea.Tick(periodicRefreshInterval, func(time.Time) tea.Msg {
			return periodicRefreshMsg{}
		}),
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
		m.board.relocateCursor()
		m.board.adjustScroll()
		m.updateDetail()
		return m, m.refreshData()

	case agentsLoadedMsg:
		m.agents.agents = msg.agents
		return m, nil

	case dataRefreshedMsg:
		m.board.tasks = msg.tasks
		m.agents.agents = msg.agents
		m.board.relocateCursor()
		m.board.adjustScroll()
		// Only apply detail data if the selection hasn't moved since the
		// refresh was initiated; otherwise discard stale detail results.
		selected := m.board.Selected()
		if selected != nil && msg.forTaskID != nil && selected.ID == *msg.forTaskID {
			m.detail.subtasks = msg.subtasks
			m.detail.agent = msg.agent
			m.detail.comments = msg.comments
			m.detail.deps = msg.deps
		}
		m.updateDetail() // also refreshes agent task filter with new subtasks
		return m, nil

	case EventMsg:
		// Orchestrator event: refresh data and re-listen.
		return m, tea.Batch(m.refreshData(), listenForEvents(m.events))

	case periodicRefreshMsg:
		// Periodic refresh: re-read DB (picks up context usage updates)
		// and schedule the next tick.
		return m, tea.Batch(m.refreshData(), tea.Tick(periodicRefreshInterval, func(time.Time) tea.Msg {
			return periodicRefreshMsg{}
		}))

	case logCapturedMsg:
		// Discard stale log capture if the selection has moved.
		if selected := m.board.Selected(); selected == nil || selected.ID != msg.forTaskID {
			return m, nil
		}
		if msg.err != nil {
			m.detail.logText = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.detail.logText = msg.text
		}
		return m, nil

	case orchLogCapturedMsg:
		// Discard stale log capture if the selection has moved.
		if selected := m.board.Selected(); selected == nil || selected.ID != msg.forTaskID {
			return m, nil
		}
		if msg.err != nil {
			m.detail.logText = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.detail.logText = msg.text
		}
		return m, nil

	case supervisorSpawnedMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("supervisor: %w", msg.err)
		} else {
			_ = m.tmux.FocusAgentSession(msg.sessionName)
		}
		return m, nil

	case reviewerSpawnedMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("reviewer: %w", msg.err)
		}
		return m, m.refreshData()

	case fixerSpawnedMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("fixer: %w", msg.err)
		}
		return m, m.refreshData()

	case reapMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("reap sessions: %w", msg.err)
		} else {
			m.err = fmt.Errorf("reaped %d dead sessions", msg.reaped)
		}
		return m, m.refreshData()

	case deleteResultMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.refreshData()

	case bugReportsLoadedMsg:
		m.bugreports.reports = msg.reports
		// Clamp cursor to the new filtered list size.
		filtered := m.bugreports.filteredReports()
		if m.bugreports.cursor >= len(filtered) {
			m.bugreports.cursor = len(filtered) - 1
		}
		if m.bugreports.cursor < 0 {
			m.bugreports.cursor = 0
		}
		m.bugreports.adjustScroll()
		return m, nil

	case bugReportActionMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		// Refresh the bug report list after an action.
		return m, m.loadBugReports()

	case bugReportDetailLoadedMsg:
		m.bugreports.selectedReport = msg.report
		m.bugreports.comments = msg.comments
		return m, nil

	case editorFinishedMsg:
		return m.handleEditorFinished(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// View renders the entire TUI layout.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	// Help overlay takes priority over everything.
	if m.showHelp {
		return m.renderHelpOverlay()
	}

	// If the create form or feedback dialog is visible, show them as overlays.
	if m.focus == FocusCreate {
		return m.renderOverlay(m.create.View())
	}
	if m.focus == FocusFeedback {
		return m.renderOverlay(m.feedback.View())
	}

	// Bug reports screen replaces the main dashboard.
	if m.focus == FocusBugReports {
		return m.renderBugReportsScreen()
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
	m.board.width = tasksWidth - 4 // Account for panel border + padding.
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

	m.detail.focused = m.focus == FocusDetail
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

// panelBorderColor returns the border color for a panel based on focus.
func (m Model) panelBorderColor(panel Focus) lipgloss.Color {
	if m.focus == panel {
		return colorPrimary
	}
	return lipgloss.Color("238")
}

// updatePanelSizes recalculates panel dimensions after a window resize.
// These are persisted on the sub-models so that scroll offsets can be
// maintained correctly between Update and View calls.
func (m *Model) updatePanelSizes() {
	overhead := 8
	availableHeight := m.height - overhead
	if availableHeight < 4 {
		availableHeight = 4
	}
	upperHeight := availableHeight * 6 / 10
	detailHeight := availableHeight - upperHeight
	if upperHeight < 3 {
		upperHeight = 3
	}
	if detailHeight < 3 {
		detailHeight = 3
	}

	innerWidth := m.width - 2
	if innerWidth < 10 {
		innerWidth = 10
	}
	tasksWidth := innerWidth * 6 / 10
	agentsWidth := innerWidth - tasksWidth

	m.board.width = tasksWidth - 4
	m.board.height = upperHeight - 2
	m.agents.width = agentsWidth - 4
	m.agents.height = upperHeight - 2
	m.detail.width = innerWidth - 4
	m.detail.height = detailHeight - 2

	// Size overlay text inputs to fit the dialog container.
	// The overlay is 2/3 of screen width; subtract 4 for border (2) + padding (2).
	overlayInnerWidth := m.width*2/3 - 4
	m.create.SetWidth(overlayInnerWidth)
	m.feedback.SetWidth(overlayInnerWidth)
}

// updateDetail refreshes the detail panel based on the currently selected task.
func (m *Model) updateDetail() {
	selected := m.board.Selected()
	if selected == nil || m.detail.task == nil || selected.ID != m.detail.task.ID {
		m.detail.scrollOffset = 0
		// Clear stale detail data from the previous task.
		m.detail.comments = nil
		m.detail.subtasks = nil
		m.detail.agent = nil
		m.detail.deps = nil
	}
	m.detail.task = selected
	m.detail.logText = ""
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

// toggleBoardCollapse toggles the collapsed state of the selected task's
// children in the board panel. If the cursor is on a parent, toggle it.
// If on a child, toggle the parent.
func (m *Model) toggleBoardCollapse() {
	entries := m.board.buildDisplayList()
	if m.board.cursor < 0 || m.board.cursor >= len(entries) {
		return
	}
	entry := entries[m.board.cursor]

	if m.board.expanded == nil {
		m.board.expanded = make(map[uuid.UUID]bool)
	}

	if entry.hasChildren {
		// Toggle this parent.
		m.board.expanded[entry.task.ID] = !m.board.expanded[entry.task.ID]
	} else if entry.isChild && entry.task.ParentTaskID != nil {
		// Toggle the parent; move cursor to the parent row.
		pid := *entry.task.ParentTaskID
		m.board.expanded[pid] = !m.board.expanded[pid]
		// If we just collapsed, find the parent row and move cursor there.
		if !m.board.expanded[pid] {
			newEntries := m.board.buildDisplayList()
			for i, e := range newEntries {
				if e.task.ID == pid {
					m.board.cursor = i
					break
				}
			}
		}
	}
	m.board.trackSelected()
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

	// Capture the task ID so the receiver can detect stale results.
	var forTaskID *uuid.UUID
	if selectedTask != nil {
		id := selectedTask.ID
		forTaskID = &id
	}

	return func() tea.Msg {
		var tasks []model.Task
		db.Where("project_id = ?", projectID).
			Order("priority desc, created_at").
			Find(&tasks)

		var agents []model.Agent
		db.Where("project_id = ?", projectID).Find(&agents)

		// Enrich working agents with context usage from transcript if
		// the runner's contextMonitorLoop hasn't populated it (e.g.
		// after a drem restart when pre-existing agents aren't in the
		// runner's in-memory map).
		for i := range agents {
			ag := &agents[i]
			if ag.Status != model.AgentWorking || ag.WorktreePath == "" {
				continue
			}
			if ag.Config != nil {
				if _, ok := ag.Config["context_used_pct"]; ok {
					continue
				}
			}
			usage, err := ctxmon.ReadTranscriptUsage(ag.WorktreePath)
			if err != nil || usage == nil {
				continue
			}
			if ag.Config == nil {
				ag.Config = make(model.JSONField)
			}
			ag.Config["context_used_pct"] = float64(usage.UsedPercent)
			ag.Config["context_window_size"] = float64(usage.ContextWindowSize)
		}

		var subtasks []model.Task
		var detailAgent *model.Agent
		var comments []model.TaskComment
		var deps []depInfo

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

			// Load dependency tasks for display.
			// Cast to []string to avoid JSONArray's driver.Valuer
			// serialising the slice as a JSON string in the IN clause.
			if len(selectedTask.DependencyIDs) > 0 {
				var depTasks []model.Task
				depIDs := []string(selectedTask.DependencyIDs)
				db.Where("id IN ?", depIDs).Select("id, title, status").Find(&depTasks)
				for _, dt := range depTasks {
					deps = append(deps, depInfo{Title: dt.Title, Status: dt.Status})
				}
			}
		}

		return dataRefreshedMsg{
			tasks:     tasks,
			agents:    agents,
			forTaskID: forTaskID,
			subtasks:  subtasks,
			agent:     detailAgent,
			comments:  comments,
			deps:      deps,
		}
	}
}
