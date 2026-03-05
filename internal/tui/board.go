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
	task      model.Task
	isChild   bool
	connector string // e.g. "├─ " or "└─ "
}

// statusSortOrder controls the display order of tasks: actionable first,
// then human gates, then terminal states.
var statusSortOrder = map[model.TaskStatus]int{
	model.StatusInProgress:    0,
	model.StatusPlanning:      1,
	model.StatusMerging:       2,
	model.StatusBacklog:       3,
	model.StatusPlanReview:    4,
	model.StatusTestingReady:  5,
	model.StatusManualTesting: 6,
	model.StatusPaused:        7,
	model.StatusDone:          8,
	model.StatusFailed:        9,
}

// BoardModel renders the task list panel.
type BoardModel struct {
	tasks  []model.Task
	cursor int
	width  int
	height int
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
		entries = append(entries, displayEntry{task: root})
		kids := children[root.ID]
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
		if len(title) > tw {
			title = title[:tw-1] + "\u2026"
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

	// Limit visible lines to height.
	visible := lines
	if b.height > 0 && len(visible) > b.height {
		// Ensure the cursor is visible by scrolling.
		start := 0
		if b.cursor >= b.height {
			start = b.cursor - b.height + 1
		}
		end := start + b.height
		if end > len(visible) {
			end = len(visible)
			start = end - b.height
			if start < 0 {
				start = 0
			}
		}
		visible = visible[start:end]
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
