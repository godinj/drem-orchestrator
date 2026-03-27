package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// displayEntry is one row in the rendered task list, holding the task and
// optional tree-connector metadata for child tasks.
type displayEntry struct {
	task        model.Task
	isChild     bool
	connector   string // e.g. "├─ " or "└─ "
	hasChildren bool   // true if this root task has child entries
	collapsed   bool   // true if children are hidden
	childCount  int    // number of children (shown in collapsed indicator)
}

// statusSortOrder controls the display order of tasks: active/actionable first,
// then human gates, then failed, then paused/rejected, then done.
var statusSortOrder = map[model.TaskStatus]int{
	model.StatusInProgress:         0,
	model.StatusPlanning:           1,
	model.StatusTestWriting:        2,
	model.StatusMerging:            3,
	model.StatusClassifying:        4,
	model.StatusBacklog:            5,
	model.StatusPlanReview:         6,
	model.StatusTestReview:         7,
	model.StatusTestingReady:       8,
	model.StatusNeedsClarification: 9,
	model.StatusFailed:             10,
	model.StatusPaused:             11,
	model.StatusRejected:           12,
	model.StatusDone:               13,
}

// BoardModel renders the task list panel.
type BoardModel struct {
	tasks        []model.Task
	cursor       int
	selectedID   *uuid.UUID // tracks cursor by task ID across re-sorts
	scrollOffset int        // first visible line, maintained for smooth scrolling
	width        int
	height       int
	expanded     map[uuid.UUID]bool // parent task IDs whose children are shown (collapsed by default)
}

// NewBoardModel creates an empty BoardModel.
func NewBoardModel() BoardModel {
	return BoardModel{}
}

// buildDisplayList creates a flat display list where subtasks appear
// immediately after their parent with tree connectors (├─ / └─).
// Root tasks (ParentTaskID == nil) are sorted by status priority then
// priority. Each parent's children are sorted the same way.
func (b BoardModel) buildDisplayList() []displayEntry {
	if len(b.tasks) == 0 {
		return nil
	}

	// Separate roots from children.
	var roots []model.Task
	children := make(map[uuid.UUID][]model.Task) // parentID -> children
	for _, t := range b.tasks {
		if t.ParentTaskID == nil {
			roots = append(roots, t)
		} else {
			children[*t.ParentTaskID] = append(children[*t.ParentTaskID], t)
		}
	}

	taskSort := func(s []model.Task) {
		sort.SliceStable(s, func(i, j int) bool {
			oi := statusSortOrder[s[i].Status]
			oj := statusSortOrder[s[j].Status]
			if oi != oj {
				return oi < oj
			}
			return s[i].Priority > s[j].Priority
		})
	}

	taskSort(roots)
	for k := range children {
		c := children[k]
		taskSort(c)
		children[k] = c
	}

	var entries []displayEntry
	for _, root := range roots {
		kids := children[root.ID]
		isCollapsed := len(kids) > 0 && !b.expanded[root.ID]
		entries = append(entries, displayEntry{
			task:        root,
			hasChildren: len(kids) > 0,
			collapsed:   isCollapsed,
			childCount:  len(kids),
		})
		if !isCollapsed {
			for i, kid := range kids {
				connector := "├─ "
				if i == len(kids)-1 {
					connector = "└─ "
				}
				entries = append(entries, displayEntry{
					task:      kid,
					isChild:   true,
					connector: connector,
				})
			}
		}
	}

	// Append orphan subtasks (parent not in current task set) at the end.
	rootIDs := make(map[uuid.UUID]bool, len(roots))
	for _, r := range roots {
		rootIDs[r.ID] = true
	}
	for pid, kids := range children {
		if rootIDs[pid] {
			continue
		}
		for _, kid := range kids {
			entries = append(entries, displayEntry{task: kid})
		}
	}

	return entries
}

// relocateCursor finds the task matching selectedID in the current display
// list and moves the cursor to it. If the task is gone, the cursor is clamped
// and selectedID updated to whatever is now under the cursor.
func (b *BoardModel) relocateCursor() {
	entries := b.buildDisplayList()
	if len(entries) == 0 {
		b.cursor = 0
		b.selectedID = nil
		return
	}

	// Try to keep cursor on the same task.
	if b.selectedID != nil {
		for i, e := range entries {
			if e.task.ID == *b.selectedID {
				b.cursor = i
				return
			}
		}
	}

	// Task no longer in list; clamp cursor.
	if b.cursor >= len(entries) {
		b.cursor = len(entries) - 1
	}
	if b.cursor < 0 {
		b.cursor = 0
	}

	// Update selectedID to whatever is now at cursor.
	id := entries[b.cursor].task.ID
	b.selectedID = &id
}

// trackSelected sets selectedID to the task currently under the cursor.
func (b *BoardModel) trackSelected() {
	if t := b.Selected(); t != nil {
		id := t.ID
		b.selectedID = &id
	}
}

// adjustScroll clamps scrollOffset so the cursor stays within the visible
// window.  Must be called from Update (pointer receiver) so changes persist.
func (b *BoardModel) adjustScroll() {
	count := len(b.buildDisplayList())
	if b.height <= 0 || count <= b.height {
		b.scrollOffset = 0
		return
	}
	if b.cursor < b.scrollOffset {
		b.scrollOffset = b.cursor
	}
	if b.cursor >= b.scrollOffset+b.height {
		b.scrollOffset = b.cursor - b.height + 1
	}
	maxScroll := count - b.height
	if b.scrollOffset > maxScroll {
		b.scrollOffset = maxScroll
	}
	if b.scrollOffset < 0 {
		b.scrollOffset = 0
	}
}

// Update handles messages for the board panel.
func (b BoardModel) Update(msg tea.Msg) (BoardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		count := len(b.buildDisplayList())
		switch msg.String() {
		case "j", "down":
			if b.cursor < count-1 {
				b.cursor++
			}
		case "k", "up":
			if b.cursor > 0 {
				b.cursor--
			}
		}
		b.trackSelected()
		b.adjustScroll()
	}
	return b, nil
}

// View renders the task list.
func (b BoardModel) View() string {
	entries := b.buildDisplayList()
	if len(entries) == 0 {
		return subtitleStyle.Render("  No tasks yet. Press [n] to create one.")
	}

	// Determine available width for title.
	// Format: "  <icon> <STATUS>  <title>"
	// Reserve ~25 chars for icon + status badge.
	maxTitleWidth := b.width - 28
	if maxTitleWidth < 10 {
		maxTitleWidth = 10
	}

	// Child rows get 4 extra chars for the connector prefix.
	childTitleWidth := maxTitleWidth - 4
	if childTitleWidth < 10 {
		childTitleWidth = 10
	}

	var lines []string
	for i, entry := range entries {
		task := entry.task
		icon := statusIcons[task.Status]
		if icon == "" {
			icon = "?"
		}
		color, ok := statusColors[task.Status]
		if !ok {
			color = lipgloss.Color("241")
		}

		statusStr := lipgloss.NewStyle().
			Foreground(color).
			Width(16).
			Render(fmt.Sprintf("%s %s", icon, strings.ToUpper(string(task.Status))))

		tw := maxTitleWidth
		prefix := "  "
		if entry.isChild {
			tw = childTitleWidth
			prefix = "  " + entry.connector
		}

		title := task.Title

		// Show quickfix label for quick fix tasks.
		if task.Category.IsQuickFix() {
			title = lipgloss.NewStyle().Foreground(colorInfo).Render("[QF]") + " " + title
			tw -= 5 // account for "[QF] " prefix
		}

		// Show collapse/expand indicator for parent tasks with children.
		if entry.hasChildren {
			if entry.collapsed {
				title = fmt.Sprintf("\u25b8 %s [%d]", title, entry.childCount) // ▸
			} else {
				title = "\u25be " + title // ▾
			}
		}

		// Annotate tasks that have empty work or are being retried.
		annotation := taskAnnotation(task)
		if annotation != "" {
			// Use visible width, not byte length — annotation contains ANSI escapes.
			tw -= lipgloss.Width(annotation) + 1
		}
		if tw < 2 {
			tw = 2
		}
		if len(title) > tw {
			title = title[:tw-1] + "\u2026"
		}
		if annotation != "" {
			title = title + " " + annotation
		}

		line := fmt.Sprintf("%s%s  %s", prefix, statusStr, title)

		if i == b.cursor {
			cursorPrefix := "> "
			if entry.isChild {
				cursorPrefix = "> " + entry.connector
			}
			line = selectedStyle.Width(b.width).Render(
				fmt.Sprintf("%s%s  %s", cursorPrefix, statusStr, title),
			)
		}

		lines = append(lines, line)
	}

	// Limit visible lines to height using the scroll offset maintained by
	// adjustScroll (called from Update).
	visible := lines
	if b.height > 0 && len(visible) > b.height {
		start := b.scrollOffset
		if start > len(visible)-b.height {
			start = len(visible) - b.height
		}
		if start < 0 {
			start = 0
		}
		visible = visible[start : start+b.height]
	}

	return strings.Join(visible, "\n")
}

// Selected returns the currently highlighted task, or nil if there are no tasks.
func (b BoardModel) Selected() *model.Task {
	entries := b.buildDisplayList()
	if len(entries) == 0 {
		return nil
	}

	idx := b.cursor
	if idx >= len(entries) {
		idx = len(entries) - 1
	}
	if idx < 0 {
		return nil
	}
	t := entries[idx].task
	return &t
}

// taskAnnotation returns a short annotation string for tasks with notable
// context flags (e.g. empty work, retries, empty feature branch).
func taskAnnotation(t model.Task) string {
	// Root tasks stuck in backlog with dependencies are pending.
	if t.Status == model.StatusBacklog && t.ParentTaskID == nil && len(t.DependencyIDs) > 0 {
		return lipgloss.NewStyle().Foreground(colorWarning).Render(
			fmt.Sprintf("\u23f3 pending %d", len(t.DependencyIDs)),
		)
	}
	if t.Context == nil {
		return ""
	}
	if _, ok := t.Context["empty_feature"]; ok {
		return lipgloss.NewStyle().Foreground(colorDanger).Render("\u26a0 no changes")
	}
	if _, ok := t.Context["empty_work"]; ok {
		if rc, ok := t.Context["retry_count"].(float64); ok && rc > 0 {
			return lipgloss.NewStyle().Foreground(colorWarning).Render(
				fmt.Sprintf("\u21bb retry %d", int(rc)),
			)
		}
		return lipgloss.NewStyle().Foreground(colorWarning).Render("\u26a0 no commits")
	}
	return ""
}

// Color palette used throughout the TUI.
var (
	colorPrimary   = lipgloss.Color("62")  // Purple
	colorSecondary = lipgloss.Color("241") // Gray
	colorSuccess   = lipgloss.Color("42")  // Green
	colorWarning   = lipgloss.Color("214") // Orange
	colorDanger    = lipgloss.Color("196") // Red
	colorInfo      = lipgloss.Color("39")  // Blue
)

// statusColors maps each TaskStatus to a display color.
var statusColors = map[model.TaskStatus]lipgloss.Color{
	model.StatusClassifying:        lipgloss.Color("39"),
	model.StatusBacklog:            lipgloss.Color("241"),
	model.StatusPlanning:           lipgloss.Color("39"),
	model.StatusNeedsClarification: lipgloss.Color("214"),
	model.StatusPlanReview:         lipgloss.Color("214"),
	model.StatusTestWriting:        lipgloss.Color("14"),
	model.StatusTestReview:         lipgloss.Color("214"),
	model.StatusInProgress:         lipgloss.Color("62"),
	model.StatusTestingReady:       lipgloss.Color("214"),
	model.StatusMerging:            lipgloss.Color("62"),
	model.StatusPaused:             lipgloss.Color("241"),
	model.StatusDone:               lipgloss.Color("42"),
	model.StatusFailed:             lipgloss.Color("196"),
	model.StatusRejected:           lipgloss.Color("196"),
}

// agentStatusColors maps each AgentStatus to a display color.
var agentStatusColors = map[model.AgentStatus]lipgloss.Color{
	model.AgentIdle:    lipgloss.Color("241"),
	model.AgentWorking: lipgloss.Color("42"),
	model.AgentBlocked: lipgloss.Color("214"),
	model.AgentDead:    lipgloss.Color("196"),
}

// statusIcons maps each TaskStatus to a Unicode icon.
var statusIcons = map[model.TaskStatus]string{
	model.StatusClassifying:        "\u2699", // ⚙
	model.StatusBacklog:            "\u25cb", // ○
	model.StatusPlanning:           "\u25cc", // ◌
	model.StatusNeedsClarification: "\u2753", // ❓
	model.StatusPlanReview:         "\u25c9", // ◉
	model.StatusTestWriting:        "\u270e", // ✎
	model.StatusTestReview:         "\u2690", // ⚐
	model.StatusInProgress:         "\u25cf", // ●
	model.StatusTestingReady:       "\u25c8", // ◈
	model.StatusMerging:            "\u27f3", // ⟳
	model.StatusPaused:             "\u23f8", // ⏸
	model.StatusDone:               "\u2713", // ✓
	model.StatusFailed:             "\u2717", // ✗
	model.StatusRejected:           "\u2718", // ✘
}

// Component styles used across the TUI.
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	subtitleStyle = lipgloss.NewStyle().Foreground(colorSecondary)
	selectedStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236"))
	statusBadge   = lipgloss.NewStyle().Padding(0, 1)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	helpStyle     = lipgloss.NewStyle().Foreground(colorSecondary)

	// Bug report screen styles.
	bugCategoryStyle = lipgloss.NewStyle().Foreground(colorInfo)
)

// StatusBadge renders a colored status badge for a task status.
func StatusBadge(status model.TaskStatus) string {
	color, ok := statusColors[status]
	if !ok {
		color = lipgloss.Color("241")
	}
	icon, ok := statusIcons[status]
	if !ok {
		icon = "?"
	}
	return statusBadge.Foreground(color).Render(icon + " " + string(status))
}

// AgentStatusBadge renders a colored agent status indicator.
func AgentStatusBadge(status model.AgentStatus) string {
	color, ok := agentStatusColors[status]
	if !ok {
		color = lipgloss.Color("241")
	}
	return statusBadge.Foreground(color).Render(string(status))
}

const (
	maxBranchDisplayLen = 30
	ctxDangerPct        = 90 // context usage % shown in red
	ctxWarningPct       = 75 // context usage % shown in orange
)

// AgentsModel renders the agent sidebar.
type AgentsModel struct {
	agents       []model.Agent
	cursor       int
	width        int
	height       int
	showArchived bool        // A toggle: show dead + idle agents
	filterTaskID *uuid.UUID  // selected board task (nil = no filter)
	subtaskIDs   []uuid.UUID // subtask IDs of selected task
	autoFilter   bool        // F toggle: auto-filter by task (default true)
}

// visibleAgents returns the agents that should be displayed based on the
// showArchived toggle and task filter. When showArchived is false, dead and
// idle agents are hidden. When autoFilter is true and a task is selected,
// only agents working on that task or its subtasks are shown.
func (a AgentsModel) visibleAgents() []model.Agent {
	var visible []model.Agent
	for _, ag := range a.agents {
		// Filter out dead + idle unless showArchived is on.
		if !a.showArchived && (ag.Status == model.AgentDead || ag.Status == model.AgentIdle) {
			continue
		}
		// Task-based filter: keep only agents related to the selected task.
		if a.autoFilter && a.filterTaskID != nil {
			if ag.CurrentTaskID == nil {
				continue
			}
			if *ag.CurrentTaskID != *a.filterTaskID && !a.isSubtaskID(*ag.CurrentTaskID) {
				continue
			}
		}
		visible = append(visible, ag)
	}
	return visible
}

// isSubtaskID checks whether the given ID is one of the subtask IDs.
func (a AgentsModel) isSubtaskID(id uuid.UUID) bool {
	for _, sid := range a.subtaskIDs {
		if sid == id {
			return true
		}
	}
	return false
}

// setTaskFilter updates the task filter from the board selection.
func (a *AgentsModel) setTaskFilter(taskID *uuid.UUID, subtaskIDs []uuid.UUID) {
	a.filterTaskID = taskID
	a.subtaskIDs = subtaskIDs
	a.clampAgentCursor()
}

// clampAgentCursor ensures the agent cursor doesn't exceed the visible list length.
func (a *AgentsModel) clampAgentCursor() {
	n := len(a.visibleAgents())
	if a.cursor >= n {
		a.cursor = n - 1
	}
	if a.cursor < 0 {
		a.cursor = 0
	}
}

// NewAgentsModel creates an empty AgentsModel.
func NewAgentsModel() AgentsModel {
	return AgentsModel{autoFilter: true}
}

// Update handles messages for the agent panel.
func (a AgentsModel) Update(msg tea.Msg) (AgentsModel, tea.Cmd) {
	visible := a.visibleAgents()
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if a.cursor < len(visible)-1 {
				a.cursor++
			}
		case "k", "up":
			if a.cursor > 0 {
				a.cursor--
			}
		}
	}
	return a, nil
}

// View renders the agent list.
func (a AgentsModel) View() string {
	visible := a.visibleAgents()
	if len(visible) == 0 {
		if len(a.agents) > 0 {
			return subtitleStyle.Render("  No matching agents. A:archived  F:filter")
		}
		return subtitleStyle.Render("  No agents running.")
	}

	var lines []string
	for i, ag := range visible {
		badge := AgentStatusBadge(ag.Status)

		// Truncate name if needed.
		name := ag.Name
		maxName := a.width - 16
		if maxName < 8 {
			maxName = 8
		}
		if len(name) > maxName {
			name = name[:maxName-1] + "\u2026"
		}

		header := fmt.Sprintf("  %s  %s", name, badge)
		if i == a.cursor {
			header = selectedStyle.Width(a.width).Render(
				fmt.Sprintf("> %s  %s", name, badge),
			)
		}

		lines = append(lines, header)

		// Indented details.
		if ag.TmuxSession != "" {
			lines = append(lines, subtitleStyle.Render(
				fmt.Sprintf("    session: %s", ag.TmuxSession),
			))
		}
		if ag.WorktreeBranch != "" {
			lines = append(lines, subtitleStyle.Render(
				fmt.Sprintf("    branch: %s", ag.WorktreeBranch),
			))
		}

		// Exit reason for dead/idle agents.
		if (ag.Status == model.AgentDead || ag.Status == model.AgentIdle) && ag.Config != nil {
			if exitReason, ok := ag.Config["exit_reason"].(string); ok {
				lines = append(lines, subtitleStyle.Render(
					fmt.Sprintf("    exit: %s", exitReason),
				))
			}
		}

		// Show activity indicator if available.
		if tool, ok := ag.Config["activity_tool"].(string); ok && tool != "" {
			target, _ := ag.Config["activity_target"].(string)
			progress, _ := ag.Config["activity_file_progress"].(string)
			committed, _ := ag.Config["activity_committed"].(bool)
			phase, _ := ag.Config["activity_phase"].(string)
			stuckScore := 0
			if ss, ok := ag.Config["activity_stuck_score"].(float64); ok {
				stuckScore = int(ss)
			}

			// Truncate target for display.
			if len(target) > maxBranchDisplayLen {
				target = "\u2026" + target[len(target)-maxBranchDisplayLen+1:]
			}

			actLine := fmt.Sprintf("    \u25b8 %s %s", tool, target)
			if progress != "" {
				actLine += " | " + progress
			}
			if committed {
				actLine += " | committed"
			} else {
				actLine += " | no commit"
			}

			// Color by phase.
			actStyle := subtitleStyle
			switch phase {
			case "exploring":
				actStyle = lipgloss.NewStyle().Foreground(colorInfo)
			case "implementing":
				actStyle = lipgloss.NewStyle().Foreground(colorWarning)
			case "testing":
				actStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
			case "fixing":
				actStyle = lipgloss.NewStyle().Foreground(colorDanger)
			}
			if stuckScore > 0 {
				actStyle = lipgloss.NewStyle().Foreground(colorDanger)
			}

			// Append ctx% inline if available.
			if pct, ok := ag.Config["context_used_pct"].(float64); ok {
				ctxStyle := subtitleStyle
				switch {
				case pct >= ctxDangerPct:
					ctxStyle = lipgloss.NewStyle().Foreground(colorDanger)
				case pct >= ctxWarningPct:
					ctxStyle = lipgloss.NewStyle().Foreground(colorWarning)
				}
				ctxSuffix := fmt.Sprintf(" | ctx: %d%%", int(pct))
				lines = append(lines, actStyle.Render(actLine)+ctxStyle.Render(ctxSuffix))
			} else {
				lines = append(lines, actStyle.Render(actLine))
			}
		} else if pct, ok := ag.Config["context_used_pct"].(float64); ok {
			// Fallback: show ctx standalone when no activity data.
			ctxStyle := subtitleStyle
			switch {
			case pct >= ctxDangerPct:
				ctxStyle = lipgloss.NewStyle().Foreground(colorDanger)
			case pct >= ctxWarningPct:
				ctxStyle = lipgloss.NewStyle().Foreground(colorWarning)
			}
			lines = append(lines, ctxStyle.Render(
				fmt.Sprintf("    ctx: %d%%", int(pct)),
			))
		}

		// Display model ID and cost.
		modelID := extractModelID(&ag)
		lines = append(lines, subtitleStyle.Render(
			fmt.Sprintf("    model: %s", modelID),
		))

		cost := extractCost(&ag)
		costStyle := subtitleStyle
		switch {
		case cost != "-":
			// Parse the cost value for styling
			if costVal, ok := ag.Config["total_cost_usd"].(float64); ok {
				switch {
				case costVal > 1.00:
					costStyle = lipgloss.NewStyle().Foreground(colorDanger)
				case costVal > 0.50:
					costStyle = lipgloss.NewStyle().Foreground(colorWarning)
				}
			}
		}
		lines = append(lines, costStyle.Render(
			fmt.Sprintf("    cost: %s", cost),
		))
	}

	// Limit visible lines to height, scrolling to keep the cursor visible.
	if a.height > 0 && len(lines) > a.height {
		// Calculate the actual start line for the selected agent.
		cursorStart := 0
		cursorEnd := 0
		lineIdx := 0
		for i, ag := range visible {
			blockLen := 1 // header line
			if ag.TmuxSession != "" {
				blockLen++
			}
			if ag.WorktreeBranch != "" {
				blockLen++
			}
			if (ag.Status == model.AgentDead || ag.Status == model.AgentIdle) && ag.Config != nil {
				if _, ok := ag.Config["exit_reason"].(string); ok {
					blockLen++
				}
			}
			_, hasCTX := ag.Config["context_used_pct"].(float64)
			hasActivity := false
			if tool, ok := ag.Config["activity_tool"].(string); ok && tool != "" {
				hasActivity = true
				blockLen++
			}
			if !hasActivity && hasCTX {
				blockLen++
			}
			// Model ID and cost lines (always present)
			blockLen += 2
			if i == a.cursor {
				cursorStart = lineIdx
				cursorEnd = lineIdx + blockLen
				break
			}
			lineIdx += blockLen
		}

		// Determine the scroll window that keeps the cursor block visible.
		start := 0
		if cursorEnd > a.height {
			start = cursorEnd - a.height
		}
		if cursorStart < start {
			start = cursorStart
		}
		end := start + a.height
		if end > len(lines) {
			end = len(lines)
			start = end - a.height
			if start < 0 {
				start = 0
			}
		}
		lines = lines[start:end]
	}

	return strings.Join(lines, "\n")
}

// Selected returns the currently highlighted agent, or nil if there are no visible agents.
func (a AgentsModel) Selected() *model.Agent {
	visible := a.visibleAgents()
	if len(visible) == 0 {
		return nil
	}
	idx := a.cursor
	if idx >= len(visible) {
		idx = len(visible) - 1
	}
	if idx < 0 {
		return nil
	}
	ag := visible[idx]
	return &ag
}
