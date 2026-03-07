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

// statusSortOrder controls the display order of tasks: actionable first,
// then human gates, then terminal states.
var statusSortOrder = map[model.TaskStatus]int{
	model.StatusInProgress:    0,
	model.StatusPlanning:      1,
	model.StatusMerging:       2,
	model.StatusBacklog:       3,
	model.StatusPlanReview:    4,
	model.StatusTestingReady: 5,
	model.StatusPaused:       6,
	model.StatusDone:         7,
	model.StatusFailed:       8,
}

// BoardModel renders the task list panel.
type BoardModel struct {
	tasks        []model.Task
	cursor       int
	selectedID   *uuid.UUID         // tracks cursor by task ID across re-sorts
	scrollOffset int                // first visible line, maintained for smooth scrolling
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
			tw -= len(annotation) + 1
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
