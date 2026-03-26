package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
)

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

// bugReportsLoadedMsg is sent when the bug report list load completes.
type bugReportsLoadedMsg struct {
	reports []model.BugReport
}

// bugReportDetailLoadedMsg is sent when a bug report's detail data is loaded.
type bugReportDetailLoadedMsg struct {
	report   *model.BugReport
	comments []model.BugReportComment
}

// ---------------------------------------------------------------------------
// Data commands (tea.Cmd factories)
// ---------------------------------------------------------------------------

// listenForEvents returns a Cmd that blocks on the events channel and wraps
// the received Event as a tea.Msg.
func listenForEvents(events <-chan Event) tea.Cmd {
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
