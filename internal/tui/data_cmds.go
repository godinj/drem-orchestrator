package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

// tasksLoadedMsg is sent when the initial task load completes.
type tasksLoadedMsg struct {
	tasks []model.Task
}

// agentsLoadedMsg is sent when the initial agent load completes.
type agentsLoadedMsg struct {
	agents []model.Agent
}

// dataRefreshedMsg is sent after a data refresh completes. The message
// carries the rendering-oriented model.Task / model.Agent types that the
// rest of the TUI already knows; the HTTP datasource adapter populates
// them from pkg/orchdto DTOs.
type dataRefreshedMsg struct {
	tasks     []model.Task
	agents    []model.Agent
	forTaskID *uuid.UUID // which task the detail data was loaded for
	subtasks  []model.Task
	agent     *model.Agent
	comments  []model.TaskComment
	deps      []depInfo
}

// dataErrMsg carries a data-source failure so the root Model can render
// the "connection lost — retrying" banner without clobbering the last
// good snapshot. The banner is cleared on the next successful
// dataRefreshedMsg.
type dataErrMsg struct {
	err error
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

// dataFetchTimeout caps how long a single DataSource call may run before
// we treat it as a failure and show the retry banner. The orchestrator
// runs on local loopback by default, so a generous 5s leaves room for
// scheduler hiccups without stalling the UI when the server is truly down.
const dataFetchTimeout = 5 * time.Second

// dataBackoffCap is the ceiling for the retry interval used by
// nextDataBackoff; it matches the cadence called out in the
// containerization prompt ("1s -> 2s -> 5s -> 10s cap").
const dataBackoffCap = 10 * time.Second

// canDispatch is the pure gate predicate used by dispatchTasksRefresh
// and dispatchAgentsRefresh. It returns true iff a refresh is allowed
// to fire RIGHT NOW given the single-flight gate (inflight) and the
// backoff hold (nextAllowed). A zero nextAllowed means "no hold."
//
// Extracting this keeps the test matrix (retry_storm_test.go) pure:
// tests exercise the gate logic without building tea.Cmd plumbing, at
// the same altitude as nextDataBackoff / datasource_backoff_test.go.
func canDispatch(inflight bool, nextAllowed, now time.Time) bool {
	if inflight {
		return false
	}
	if nextAllowed.IsZero() {
		return true
	}
	return !now.Before(nextAllowed)
}

// dispatchTasksRefresh is the single-flight-gated entry point used by
// the periodicRefreshMsg and EventMsg branches of Update. It returns
// nil (not an error — a nil tea.Cmd is a no-op) if a /tasks fetch is
// already in flight OR if we are inside a backoff hold window;
// otherwise it sets the gate and returns the existing loadTasks Cmd.
//
// Invariant: the returned Cmd ALWAYS emits either tasksLoadedMsg or
// dataErrMsg — there is no third path. That invariant keeps the gate
// from stranding on error (see Update's dataErrMsg handler which
// explicitly clears tasksInflight).
//
// Takes a pointer receiver so the gate bit is mutated in place on the
// Update-local value. Safe because Update's `m Model` is addressable.
func (m *Model) dispatchTasksRefresh() tea.Cmd {
	if !canDispatch(m.tasksInflight, m.nextAllowedRefresh, m.now()) {
		return nil
	}
	m.tasksInflight = true
	return m.loadTasks()
}

// dispatchAgentsRefresh mirrors dispatchTasksRefresh for the /workers
// endpoint. Per-endpoint gating (not a global gate) preserves today's
// semantics where tasks and agents fetch independently — a slow
// /tasks does not stall /workers and vice versa.
func (m *Model) dispatchAgentsRefresh() tea.Cmd {
	if !canDispatch(m.agentsInflight, m.nextAllowedRefresh, m.now()) {
		return nil
	}
	m.agentsInflight = true
	return m.loadAgents()
}

// schedulePeriodicTick returns the singleton periodic tick Cmd used
// by Init and the periodicRefreshMsg branch. Expressed as a helper so
// there is exactly one call site per "re-arm" event; if you add a
// second, you have re-introduced the tick-compounding bug.
func schedulePeriodicTick() tea.Cmd {
	return tea.Tick(periodicRefreshInterval, func(time.Time) tea.Msg {
		return periodicRefreshMsg{}
	})
}

// now is the clock accessor honoured by the dispatch helpers. It
// prefers the injected nowFunc (tests) and falls back to time.Now.
func (m *Model) now() time.Time {
	if m.nowFunc != nil {
		return m.nowFunc()
	}
	return time.Now()
}

// nextDataBackoff advances the backoff ladder one step. The ladder is
// 1s -> 2s -> 5s -> 10s -> 10s...; it is exposed as a pure function so
// backoff behaviour stays testable without exercising tea.Cmd plumbing.
func nextDataBackoff(current time.Duration) time.Duration {
	switch {
	case current <= 0:
		return 1 * time.Second
	case current < 2*time.Second:
		return 2 * time.Second
	case current < 5*time.Second:
		return 5 * time.Second
	default:
		return dataBackoffCap
	}
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

// loadTasks returns a Cmd that fetches tasks via the orchestrator HTTP API.
// The datasource may be nil in tests that construct Model literally; in
// that case the Cmd emits an empty result instead of panicking.
func (m Model) loadTasks() tea.Cmd {
	ds := m.dataSource
	return func() tea.Msg {
		if ds == nil {
			return tasksLoadedMsg{tasks: nil}
		}
		ctx, cancel := context.WithTimeout(context.Background(), dataFetchTimeout)
		defer cancel()
		dtos, err := ds.ListTasks(ctx, orchclient.TaskFilter{})
		if err != nil {
			return dataErrMsg{err: err}
		}
		return tasksLoadedMsg{tasks: TasksFromDTOs(dtos)}
	}
}

// loadAgents returns a Cmd that fetches workers (rendered as agents) via
// the orchestrator HTTP API.
func (m Model) loadAgents() tea.Cmd {
	ds := m.dataSource
	return func() tea.Msg {
		if ds == nil {
			return agentsLoadedMsg{agents: nil}
		}
		ctx, cancel := context.WithTimeout(context.Background(), dataFetchTimeout)
		defer cancel()
		dtos, err := ds.ListWorkers(ctx)
		if err != nil {
			return dataErrMsg{err: err}
		}
		return agentsLoadedMsg{agents: AgentsFromDTOs(dtos)}
	}
}

// refreshData returns a Cmd that reloads tasks, agents, and detail context.
// Tasks and agents come from the HTTP API; detail enrichments (subtasks,
// comments, dependency titles, and the assigned agent snapshot) still
// come from the local GORM DB when one is available. Those surfaces are
// out of scope of the public HTTP API in this release
// (docs/prd-containerization.md scope section).
func (m Model) refreshData() tea.Cmd {
	ds := m.dataSource
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
		var (
			tasks  []model.Task
			agents []model.Agent
		)
		if ds != nil {
			ctx, cancel := context.WithTimeout(context.Background(), dataFetchTimeout)
			defer cancel()
			taskDTOs, err := ds.ListTasks(ctx, orchclient.TaskFilter{})
			if err != nil {
				return dataErrMsg{err: err}
			}
			workerDTOs, err := ds.ListWorkers(ctx)
			if err != nil {
				return dataErrMsg{err: err}
			}
			tasks = TasksFromDTOs(taskDTOs)
			agents = AgentsFromDTOs(workerDTOs)
		}

		// Enrich working agents with context usage from transcript if the
		// runner's contextMonitorLoop hasn't populated it. Harmless when
		// the HTTP-sourced WorktreePath is empty: the usage lookup is
		// skipped and the loop is a no-op.
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

		// Detail enrichment is still DB-backed; skip cleanly if no DB
		// handle is available (e.g. tests, or a future TUI invoked
		// against a remote orchestrator without local SQLite).
		if db != nil && selectedTask != nil {
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

			// Load dependency tasks for display. Cast to []string to
			// avoid JSONArray's driver.Valuer serialising the slice as
			// a JSON string in the IN clause.
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
