// Package cli implements the headless CLI subcommand for the drem
// orchestrator. It provides programmatic read/write access to the
// orchestrator database for C-Suite agents and temp workers.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/tui"
)

// Run parses args (after "cli" is consumed) and dispatches to the
// appropriate handler. Returns an error for unknown subcommands or
// handler failures. If orch is provided, gate commands (approve, reject,
// answer, pass, fail) are also available.
func Run(db *gorm.DB, args []string, w io.Writer, jsonMode bool, orch tui.TUIOrchestrator) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli <subcommand> [options]\nsubcommands: tasks, task, agents, failures, stats, file-task, comment, approve, reject, answer, pass, fail")
	}

	sub := args[0]
	rest := args[1:]

	// Check for gate commands if orchestrator is provided
	if orch != nil {
		if handled, err := RegisterGateCommands(db, orch, sub, rest, w); handled {
			return err
		}
	}

	switch sub {
	case "tasks":
		return handleTasks(db, rest, w, jsonMode)
	case "task":
		return handleTask(db, rest, w, jsonMode)
	case "agents":
		return handleAgents(db, rest, w, jsonMode)
	case "failures":
		return handleFailures(db, rest, w, jsonMode)
	case "stats":
		return handleStats(db, rest, w, jsonMode)
	case "file-task":
		return handleFileTask(db, rest, w, jsonMode)
	case "comment":
		return handleComment(db, rest, w, jsonMode)
	default:
		return fmt.Errorf("unknown subcommand: %q", sub)
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// parseFlag scans args for --key=value or --key value. Returns the value
// and the remaining args with the flag removed.
func parseFlag(args []string, key string) (string, []string) {
	prefix := "--" + key + "="
	var remaining []string
	var value string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], prefix) {
			value = strings.TrimPrefix(args[i], prefix)
		} else if args[i] == "--"+key && i+1 < len(args) {
			i++
			value = args[i]
		} else {
			remaining = append(remaining, args[i])
		}
	}
	return value, remaining
}

// shortID returns the first 8 characters of a UUID string.
func shortID(id uuid.UUID) string {
	s := id.String()
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}

// relativeTime formats a time relative to now, e.g. "2m ago".
func relativeTime(t *time.Time) string {
	if t == nil {
		return "—"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// resolveTaskByPrefix finds a task whose ID starts with the given prefix
// (minimum 8 chars). Returns an error if no match or ambiguous.
func resolveTaskByPrefix(db *gorm.DB, prefix string) (model.Task, error) {
	if len(prefix) < 8 {
		return model.Task{}, fmt.Errorf("task ID prefix must be at least 8 characters, got %d", len(prefix))
	}
	var tasks []model.Task
	if err := db.Where("id LIKE ?", prefix+"%").Limit(2).Find(&tasks).Error; err != nil {
		return model.Task{}, fmt.Errorf("query task: %w", err)
	}
	if len(tasks) == 0 {
		return model.Task{}, fmt.Errorf("no task found matching prefix %q", prefix)
	}
	if len(tasks) > 1 {
		return model.Task{}, fmt.Errorf("ambiguous task ID prefix %q matches multiple tasks", prefix)
	}
	return tasks[0], nil
}

// ── tasks ────────────────────────────────────────────────────────────

func handleTasks(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	statusStr, _ := parseFlag(args, "status")

	query := db.Model(&model.Task{}).Order("updated_at DESC")
	if statusStr != "" {
		ts, err := model.ParseTaskStatus(statusStr)
		if err != nil {
			return err
		}
		query = query.Where("status = ?", ts)
	}

	var tasks []model.Task
	if err := query.Find(&tasks).Error; err != nil {
		return fmt.Errorf("query tasks: %w", err)
	}

	if jsonMode {
		return writeJSON(w, tasks)
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tCATEGORY\tTITLE")
	for _, t := range tasks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			shortID(t.ID), t.Status, t.Category, t.Title)
	}
	return tw.Flush()
}

// ── task detail ──────────────────────────────────────────────────────

type taskDetailJSON struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Description    string          `json:"description"`
	Status         string          `json:"status"`
	Category       string          `json:"category"`
	Complexity     int             `json:"complexity"`
	Priority       int             `json:"priority"`
	Labels         []string        `json:"labels"`
	WorktreeBranch string          `json:"worktree_branch"`
	Phase          string          `json:"phase"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Subtasks       []subtaskJSON   `json:"subtasks,omitempty"`
	Agent          *agentBriefJSON `json:"agent,omitempty"`
	Comments       []commentJSON   `json:"comments,omitempty"`
}

type subtaskJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
}

type agentBriefJSON struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type commentJSON struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	Body      string    `json:"body"`
}

func handleTask(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	_, remaining := parseFlag(args, "json") // consume stray --json
	if len(remaining) == 0 {
		return fmt.Errorf("usage: drem cli task <id>")
	}
	idStr := remaining[0]

	task, err := resolveTaskByPrefix(db, idStr)
	if err != nil {
		return err
	}

	// Load associations
	db.Where("parent_task_id = ?", task.ID).Order("created_at ASC").Find(&task.Subtasks)
	if task.AssignedAgentID != nil {
		var agent model.Agent
		db.Where("id = ?", *task.AssignedAgentID).First(&agent)
		task.AssignedAgent = &agent
	}
	db.Where("task_id = ?", task.ID).Order("created_at ASC").Find(&task.Comments)

	if jsonMode {
		detail := taskDetailJSON{
			ID:             task.ID.String(),
			Title:          task.Title,
			Description:    task.Description,
			Status:         string(task.Status),
			Category:       string(task.Category),
			Complexity:     task.ComplexityScore,
			Priority:       task.Priority,
			Labels:         []string(task.Labels),
			WorktreeBranch: task.WorktreeBranch,
			Phase:          task.Phase,
			CreatedAt:      task.CreatedAt,
			UpdatedAt:      task.UpdatedAt,
		}
		if detail.Labels == nil {
			detail.Labels = []string{}
		}
		for _, st := range task.Subtasks {
			detail.Subtasks = append(detail.Subtasks, subtaskJSON{
				ID:     shortID(st.ID),
				Status: string(st.Status),
				Title:  st.Title,
			})
		}
		if task.AssignedAgent != nil {
			detail.Agent = &agentBriefJSON{
				Type:   string(task.AssignedAgent.AgentType),
				Status: string(task.AssignedAgent.Status),
			}
		}
		for _, c := range task.Comments {
			detail.Comments = append(detail.Comments, commentJSON{
				Author:    c.Author,
				CreatedAt: c.CreatedAt,
				Body:      c.Body,
			})
		}
		return writeJSON(w, detail)
	}

	// Human-readable output
	fmt.Fprintf(w, "ID:              %s\n", task.ID.String())
	fmt.Fprintf(w, "Title:           %s\n", task.Title)
	fmt.Fprintf(w, "Description:     %s\n", task.Description)
	fmt.Fprintf(w, "Status:          %s\n", task.Status)
	fmt.Fprintf(w, "Category:        %s\n", task.Category)
	fmt.Fprintf(w, "Complexity:      %d\n", task.ComplexityScore)
	fmt.Fprintf(w, "Priority:        %d\n", task.Priority)
	labels := "—"
	if len(task.Labels) > 0 {
		labels = strings.Join([]string(task.Labels), ", ")
	}
	fmt.Fprintf(w, "Labels:          %s\n", labels)
	fmt.Fprintf(w, "Worktree Branch: %s\n", task.WorktreeBranch)
	fmt.Fprintf(w, "Phase:           %s\n", task.Phase)
	fmt.Fprintf(w, "Created:         %s\n", task.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "Updated:         %s\n", task.UpdatedAt.Format(time.RFC3339))

	if len(task.Subtasks) > 0 {
		fmt.Fprintln(w, "\nSubtasks:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, st := range task.Subtasks {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", shortID(st.ID), st.Status, st.Title)
		}
		tw.Flush()
	}

	if task.AssignedAgent != nil {
		fmt.Fprintf(w, "\nAgent: %s (%s)\n",
			task.AssignedAgent.AgentType, task.AssignedAgent.Status)
	}

	if len(task.Comments) > 0 {
		fmt.Fprintln(w, "\nComments:")
		for _, c := range task.Comments {
			fmt.Fprintf(w, "  [%s] %s:\n    %s\n",
				c.CreatedAt.Format(time.RFC3339), c.Author, c.Body)
		}
	}

	return nil
}

// ── agents ───────────────────────────────────────────────────────────

func handleAgents(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	statusStr, _ := parseFlag(args, "status")

	query := db.Model(&model.Agent{}).Order("updated_at DESC")
	if statusStr != "" {
		status := model.AgentStatus(statusStr)
		if status != model.AgentIdle && status != model.AgentWorking &&
			status != model.AgentBlocked && status != model.AgentDead {
			return fmt.Errorf("unknown agent status: %q", statusStr)
		}
		query = query.Where("status = ?", status)
	}

	var agents []model.Agent
	if err := query.Find(&agents).Error; err != nil {
		return fmt.Errorf("query agents: %w", err)
	}

	if jsonMode {
		return writeJSON(w, agents)
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tSTATUS\tTASK\tHEARTBEAT")
	for _, a := range agents {
		taskCol := "—"
		if a.CurrentTaskID != nil {
			taskCol = shortID(*a.CurrentTaskID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			shortID(a.ID), a.AgentType, a.Status, taskCol, relativeTime(a.HeartbeatAt))
	}
	return tw.Flush()
}

// ── failures ─────────────────────────────────────────────────────────

type failureEntry struct {
	Kind      string    `json:"kind"` // "task" or "agent"
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Timestamp time.Time `json:"timestamp"`
}

func handleFailures(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	sinceStr, _ := parseFlag(args, "since")
	since := 24 * time.Hour
	if sinceStr != "" {
		d, err := time.ParseDuration(sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since duration: %w", err)
		}
		since = d
	}
	cutoff := time.Now().Add(-since)

	var entries []failureEntry

	// Failed tasks
	var failedTasks []model.Task
	db.Where("status = ? AND updated_at >= ?", model.StatusFailed, cutoff).
		Order("updated_at DESC").
		Find(&failedTasks)

	for _, t := range failedTasks {
		// Get last event for context
		var lastEvent model.TaskEvent
		detail := ""
		if err := db.Where("task_id = ?", t.ID).Order("created_at DESC").First(&lastEvent).Error; err == nil {
			detail = fmt.Sprintf("%s: %s → %s", lastEvent.EventType, lastEvent.OldValue, lastEvent.NewValue)
		}
		entries = append(entries, failureEntry{
			Kind:      "task",
			ID:        shortID(t.ID),
			Title:     t.Title,
			Detail:    detail,
			Timestamp: t.UpdatedAt,
		})
	}

	// Dead agents
	var deadAgents []model.Agent
	db.Where("status = ? AND updated_at >= ?", model.AgentDead, cutoff).
		Order("updated_at DESC").
		Find(&deadAgents)

	for _, a := range deadAgents {
		title := string(a.AgentType)
		if a.CurrentTaskID != nil {
			var task model.Task
			if err := db.Where("id = ?", *a.CurrentTaskID).First(&task).Error; err == nil {
				title = task.Title
			}
		}
		entries = append(entries, failureEntry{
			Kind:      "agent",
			ID:        shortID(a.ID),
			Title:     title,
			Detail:    fmt.Sprintf("type=%s", a.AgentType),
			Timestamp: a.UpdatedAt,
		})
	}

	if jsonMode {
		return writeJSON(w, entries)
	}

	if len(entries) == 0 {
		fmt.Fprintln(w, "No failures found.")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tID\tTITLE\tDETAIL")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Kind, e.ID, e.Title, e.Detail)
	}
	return tw.Flush()
}

// ── stats ────────────────────────────────────────────────────────────

type statsJSON struct {
	TasksByStatus    map[string]int `json:"tasks_by_status"`
	AgentsByStatus   map[string]int `json:"agents_by_status"`
	TotalTasks       int            `json:"total_tasks"`
	FailureRate      float64        `json:"failure_rate"`
	CompletedLast24h int64          `json:"completed_last_24h"`
	AgentsSpawned24h int64          `json:"agents_spawned_last_24h"`
}

func handleStats(db *gorm.DB, _ []string, w io.Writer, jsonMode bool) error {
	// Tasks by status
	type statusCount struct {
		Status string
		Count  int
	}
	var taskCounts []statusCount
	db.Model(&model.Task{}).Select("status, count(*) as count").Group("status").Scan(&taskCounts)

	tasksByStatus := make(map[string]int)
	total := 0
	failed := 0
	for _, sc := range taskCounts {
		tasksByStatus[sc.Status] = sc.Count
		total += sc.Count
		if sc.Status == string(model.StatusFailed) {
			failed = sc.Count
		}
	}

	// Agents by status
	var agentCounts []statusCount
	db.Model(&model.Agent{}).Select("status, count(*) as count").Group("status").Scan(&agentCounts)

	agentsByStatus := make(map[string]int)
	for _, sc := range agentCounts {
		agentsByStatus[sc.Status] = sc.Count
	}

	// Failure rate
	var failureRate float64
	if total > 0 {
		failureRate = float64(failed) / float64(total) * 100
	}

	// Last 24h metrics
	cutoff24h := time.Now().Add(-24 * time.Hour)
	var completed24h int64
	db.Model(&model.Task{}).Where("status = ? AND updated_at >= ?", model.StatusDone, cutoff24h).Count(&completed24h)

	var spawned24h int64
	db.Model(&model.Agent{}).Where("created_at >= ?", cutoff24h).Count(&spawned24h)

	if jsonMode {
		return writeJSON(w, statsJSON{
			TasksByStatus:    tasksByStatus,
			AgentsByStatus:   agentsByStatus,
			TotalTasks:       total,
			FailureRate:      failureRate,
			CompletedLast24h: completed24h,
			AgentsSpawned24h: spawned24h,
		})
	}

	fmt.Fprintln(w, "Tasks by status:")
	for status, count := range tasksByStatus {
		fmt.Fprintf(w, "  %-20s %d\n", status, count)
	}
	fmt.Fprintf(w, "\nTotal tasks:           %d\n", total)
	fmt.Fprintf(w, "Failure rate:          %.1f%%\n", failureRate)
	fmt.Fprintf(w, "Completed (24h):       %d\n", completed24h)

	fmt.Fprintln(w, "\nAgents by status:")
	for status, count := range agentsByStatus {
		fmt.Fprintf(w, "  %-20s %d\n", status, count)
	}
	fmt.Fprintf(w, "Spawned (24h):         %d\n", spawned24h)

	return nil
}

// ── file-task ────────────────────────────────────────────────────────

func handleFileTask(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	title, args := parseFlag(args, "title")
	desc, args := parseFlag(args, "description")
	projectIDStr, _ := parseFlag(args, "project-id")

	if title == "" {
		return fmt.Errorf("--title is required")
	}
	if desc == "" {
		return fmt.Errorf("--description is required")
	}

	var projectID uuid.UUID
	if projectIDStr != "" {
		var err error
		projectID, err = uuid.Parse(projectIDStr)
		if err != nil {
			return fmt.Errorf("invalid --project-id: %w", err)
		}
	} else {
		var project model.Project
		if err := db.First(&project).Error; err != nil {
			return fmt.Errorf("no project found in database; use --project-id to specify one")
		}
		projectID = project.ID
	}

	task := model.Task{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Title:       title,
		Description: desc,
		Status:      model.StatusClassifying,
		Category:    model.CategoryStandard,
	}
	if err := db.Create(&task).Error; err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	if jsonMode {
		return writeJSON(w, map[string]string{"id": task.ID.String()})
	}
	fmt.Fprintf(w, "Created task %s\n", task.ID.String())
	return nil
}

// ── comment ──────────────────────────────────────────────────────────

func handleComment(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	body, remaining := parseFlag(args, "body")

	if len(remaining) == 0 {
		return fmt.Errorf("usage: drem cli comment <task-id> --body=BODY")
	}
	taskPrefix := remaining[0]

	if body == "" {
		return fmt.Errorf("--body is required")
	}

	task, err := resolveTaskByPrefix(db, taskPrefix)
	if err != nil {
		return err
	}

	comment := model.TaskComment{
		ID:     uuid.New(),
		TaskID: task.ID,
		Author: "csuite",
		Body:   body,
	}
	if err := db.Create(&comment).Error; err != nil {
		return fmt.Errorf("create comment: %w", err)
	}

	if jsonMode {
		return writeJSON(w, map[string]string{"id": comment.ID.String(), "task_id": task.ID.String()})
	}
	fmt.Fprintf(w, "Added comment %s to task %s\n", comment.ID.String(), shortID(task.ID))
	return nil
}

// ── JSON output ──────────────────────────────────────────────────────

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
