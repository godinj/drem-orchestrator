package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/model"
)

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

// Update handles messages for the board panel.
func (b BoardModel) Update(msg tea.Msg) (BoardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if b.cursor < len(b.tasks)-1 {
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
	if len(b.tasks) == 0 {
		return subtitleStyle.Render("  No tasks yet. Press [n] to create one.")
	}

	// Sort tasks: actionable first, then human gates, then done/failed.
	sorted := make([]model.Task, len(b.tasks))
	copy(sorted, b.tasks)
	sort.SliceStable(sorted, func(i, j int) bool {
		oi := statusSortOrder[sorted[i].Status]
		oj := statusSortOrder[sorted[j].Status]
		if oi != oj {
			return oi < oj
		}
		// Within the same status group, higher priority first.
		return sorted[i].Priority > sorted[j].Priority
	})

	// Determine available width for title.
	// Format: "  <icon> <STATUS>  <title>"
	// Reserve ~25 chars for icon + status badge.
	maxTitleWidth := b.width - 28
	if maxTitleWidth < 10 {
		maxTitleWidth = 10
	}

	var lines []string
	for i, task := range sorted {
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

		title := task.Title
		if len(title) > maxTitleWidth {
			title = title[:maxTitleWidth-1] + "\u2026"
		}

		line := fmt.Sprintf("  %s  %s", statusStr, title)

		if i == b.cursor {
			line = selectedStyle.Width(b.width).Render(
				fmt.Sprintf("> %s  %s", statusStr, title),
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
	if len(b.tasks) == 0 {
		return nil
	}

	// We need to return from the sorted order (same as View), since the
	// cursor tracks the display position.
	sorted := make([]model.Task, len(b.tasks))
	copy(sorted, b.tasks)
	sort.SliceStable(sorted, func(i, j int) bool {
		oi := statusSortOrder[sorted[i].Status]
		oj := statusSortOrder[sorted[j].Status]
		if oi != oj {
			return oi < oj
		}
		return sorted[i].Priority > sorted[j].Priority
	})

	idx := b.cursor
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if idx < 0 {
		return nil
	}
	t := sorted[idx]
	return &t
}
