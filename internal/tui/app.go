package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/csuite"
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
	// FocusCsuite means the C-Suite agent dashboard is focused.
	FocusCsuite
	// FocusExperiments means the experiment summary panel is focused.
	FocusExperiments
)

// Model is the root Bubble Tea model that composes all TUI sub-models.
type Model struct {
	db          *gorm.DB
	dataSource  DataSource // orchestrator HTTP API (tasks, workers, events, logs)
	orch        TUIOrchestrator
	tmux        TmuxManager
	projectID   uuid.UUID
	events      <-chan Event
	csuiteSnaps <-chan csuiteStateSnapshot

	board       BoardModel
	agents      AgentsModel
	detail      DetailModel
	create      CreateModel
	feedback    FeedbackModel
	bugreports  BugReportsModel
	csuite      CsuiteModel
	experiments ExperimentView

	bugreportSvc   *bugReportSvc
	logPath        string
	focus          Focus
	feedbackAction feedbackAction
	confirm        confirmAction // pending gate action awaiting y/n
	confirmTaskID  uuid.UUID     // task ID for pending confirmation
	keys           keyMap
	showHelp       bool
	width          int
	height         int
	err            error

	// dataErr holds the most recent failure from DataSource so the root
	// View can render a "connection lost — retrying" banner while keeping
	// the last successful snapshot visible. It is cleared the next time
	// a refresh succeeds.
	dataErr error
	// dataBackoff is the interval used for the next refresh attempt after
	// a failure. It advances on each consecutive failure and caps at 10s,
	// per the containerization prompt; a successful refresh resets it.
	dataBackoff time.Duration
}

// NewModel creates the root TUI model.
//
// dataSource is the read-only HTTP-backed view of the orchestrator used
// for tasks, workers, and events. The legacy db *gorm.DB parameter is
// retained for feature surfaces that the HTTP API does not yet cover
// (bug reports, experiments, task-detail enrichment such as comments and
// dependency titles); those call sites are documented in
// internal/tui/README.md as follow-ups.
func NewModel(
	db *gorm.DB,
	dataSource DataSource,
	orch TUIOrchestrator,
	tmux TmuxManager,
	projectID uuid.UUID,
	events <-chan Event,
	logPath string,
	bugreportSvc *bugReportSvc,
	csuiteSnaps <-chan csuiteStateSnapshot,
	csuiteStore *csuite.Store,
) Model {
	cs := NewCsuiteModel()
	if csuiteStore != nil {
		cs.store = csuiteStore
	}
	return Model{
		db:           db,
		dataSource:   dataSource,
		orch:         orch,
		tmux:         tmux,
		projectID:    projectID,
		events:       events,
		logPath:      logPath,
		bugreportSvc: bugreportSvc,
		csuiteSnaps:  csuiteSnaps,
		board:        NewBoardModel(),
		agents:       NewAgentsModel(),
		detail:       NewDetailModel(),
		create:       NewCreateModel(),
		feedback:     NewFeedbackModel("Feedback"),
		bugreports:   NewBugReportsModel(db, bugreportSvc, projectID),
		csuite:       cs,
		experiments:  NewExperimentView(db),
		focus:        FocusBoard,
		keys:         defaultKeyMap(),
	}
}

// Init returns the initial commands: load tasks, load agents, listen for events,
// start the periodic refresh tick, and listen for C-Suite snapshots.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.loadTasks(),
		m.loadAgents(),
		listenForEvents(m.events),
		tea.Tick(periodicRefreshInterval, func(time.Time) tea.Msg {
			return periodicRefreshMsg{}
		}),
	}
	if m.csuiteSnaps != nil {
		cmds = append(cmds, listenForCsuiteSnapshot(m.csuiteSnaps))
	}
	return tea.Batch(cmds...)
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
		m.dataErr = nil
		m.dataBackoff = 0
		return m, m.refreshData()

	case agentsLoadedMsg:
		m.agents.agents = msg.agents
		m.dataErr = nil
		m.dataBackoff = 0
		return m, nil

	case dataErrMsg:
		// Remember the failure for the banner, bump backoff for the next
		// retry, but keep the last-good snapshot visible. A retry tick is
		// scheduled so the poll loop doesn't stall after a failure; the
		// regular periodicRefreshMsg will also keep polling in parallel.
		m.dataErr = msg.err
		m.dataBackoff = nextDataBackoff(m.dataBackoff)
		return m, tea.Tick(m.dataBackoff, func(time.Time) tea.Msg {
			return periodicRefreshMsg{}
		})

	case dataRefreshedMsg:
		m.board.tasks = msg.tasks
		m.agents.agents = msg.agents
		m.board.relocateCursor()
		m.board.adjustScroll()
		m.dataErr = nil
		m.dataBackoff = 0
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

	case csuiteSnapshotMsg:
		m.csuite.snapshot = &msg.snapshot
		return m, listenForCsuiteSnapshot(m.csuiteSnaps)

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

	// C-Suite dashboard replaces the main dashboard.
	if m.focus == FocusCsuite {
		return m.renderCsuiteScreen()
	}

	// Experiments screen replaces the main dashboard.
	if m.focus == FocusExperiments {
		return m.renderExperimentsScreen()
	}

	// Title bar.
	titleBar := titleStyle.Render("Drem Orchestrator")

	// Status bar with task counts per status.
	statusBar := m.renderStatusBar()

	// Help bar at the bottom.
	helpBar := m.renderHelpBar()

	// Compute layout dimensions (shared with updatePanelSizes).
	d := m.computePanelDimensions()

	// Update panel sizes.
	m.board.width = d.tasksWidth - 4 // Account for panel border + padding.
	m.board.height = d.upperHeight - 2
	m.agents.width = d.agentsWidth - 4
	m.agents.height = d.upperHeight - 2
	m.detail.width = d.innerWidth - 4
	m.detail.height = d.detailHeight - 2

	// Render panels.
	boardLabel := " Tasks "
	if m.board.showAll {
		boardLabel = " Tasks [+all] "
	}
	if m.focus == FocusBoard {
		if m.board.showAll {
			boardLabel = " Tasks [+all] (active) "
		} else {
			boardLabel = " Tasks (active) "
		}
	}
	tasksPanel := panelStyle.
		Width(d.tasksWidth).
		Height(d.upperHeight).
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
		Width(d.agentsWidth).
		Height(d.upperHeight).
		BorderForeground(m.panelBorderColor(FocusAgents)).
		Render(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(agentsLabel) + "\n" + m.agents.View())

	upperRow := lipgloss.JoinHorizontal(lipgloss.Top, tasksPanel, agentsPanel)

	m.detail.focused = m.focus == FocusDetail
	detailPanel := panelStyle.
		Width(d.innerWidth).
		Height(d.detailHeight).
		BorderForeground(m.panelBorderColor(FocusDetail)).
		Render(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(" Detail ") + "\n" + m.detail.View())

	// Gate action confirmation prompt.
	confirmLine := ""
	if m.confirm != confirmNone {
		confirmLine = m.renderConfirmPrompt()
	}

	// Error line.
	errLine := ""
	if m.err != nil {
		errLine = lipglossRender(colorDanger, fmt.Sprintf("Error: %v", m.err))
	}

	// Data-source connection banner. Shown as a distinct warning line so
	// the last-good task/agent snapshot stays on screen while the TUI
	// retries in the background.
	connLine := ""
	if m.dataErr != nil {
		connLine = lipglossRender(colorDanger,
			fmt.Sprintf("connection lost — retrying (next in %s): %v",
				m.dataBackoff, m.dataErr))
	}

	// Compose.
	parts := []string{
		titleBar,
		statusBar,
		upperRow,
		detailPanel,
	}
	if confirmLine != "" {
		parts = append(parts, confirmLine)
	} else if errLine != "" {
		parts = append(parts, errLine)
	}
	if connLine != "" {
		parts = append(parts, connLine)
	}
	parts = append(parts, helpBar)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
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

// renderExperimentsScreen renders the experiments view.
func (m Model) renderExperimentsScreen() string {
	// Title bar.
	titleBar := titleStyle.Render("Drem Orchestrator - Experiments")

	// Status bar with task counts per status.
	statusBar := m.renderStatusBar()

	// Help bar at the bottom.
	helpBar := m.renderHelpBar()

	// Main content area
	content := m.experiments.View()

	// Create a layout with title, status bar, content, and help bar
	parts := []string{
		titleBar,
		statusBar,
		content,
		helpBar,
	}

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}
