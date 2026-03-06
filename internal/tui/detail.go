package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// deleteItemKind distinguishes the type of item being selected for deletion.
type deleteItemKind int

const (
	deleteItemPlanStep deleteItemKind = iota
	deleteItemSubtask
	deleteItemComment
)

// deleteItem identifies one deletable entry in the detail view.
type deleteItem struct {
	kind  deleteItemKind
	index int // index within the source slice (plan steps, subtasks, or comments)
}

// DetailModel renders task details and available actions.
type DetailModel struct {
	task     *model.Task
	subtasks []model.Task
	agent    *model.Agent
	comments []model.TaskComment
	logText  string
	width    int
	height   int

	scrollOffset      int  // vertical scroll offset for detail content
	subtaskCursor     int  // selected subtask index (when subtasks exist)
	focused           bool // true when the detail panel has focus
	deleteMode        bool // true when selecting an item to delete
	deleteCursor      int  // index into deletableItems()
	subtasksCollapsed bool // true when subtask list is folded
}

// NewDetailModel creates an empty DetailModel.
func NewDetailModel() DetailModel {
	return DetailModel{subtasksCollapsed: true}
}

// deletableItems returns a flat list of all items that can be deleted in the
// current detail view, in display order: plan steps, subtasks, then comments.
func (d DetailModel) deletableItems() []deleteItem {
	var items []deleteItem

	// Plan steps (only in plan_review).
	if d.task != nil && d.task.Status == model.StatusPlanReview && d.task.Plan != nil {
		if subtasks, ok := d.task.Plan["subtasks"]; ok {
			if list, ok := subtasks.([]any); ok {
				for i := range list {
					items = append(items, deleteItem{kind: deleteItemPlanStep, index: i})
				}
			}
		}
	}

	// Subtasks (when parent has children).
	for i := range d.subtasks {
		items = append(items, deleteItem{kind: deleteItemSubtask, index: i})
	}

	// Comments.
	for i := range d.comments {
		items = append(items, deleteItem{kind: deleteItemComment, index: i})
	}

	return items
}

// selectedDeleteItem returns the deleteItem at the current cursor, or nil.
func (d DetailModel) selectedDeleteItem() *deleteItem {
	items := d.deletableItems()
	if d.deleteCursor < 0 || d.deleteCursor >= len(items) {
		return nil
	}
	return &items[d.deleteCursor]
}

// isDeleteTarget reports whether the item at (kind, index) is the current
// delete cursor target.
func (d DetailModel) isDeleteTarget(kind deleteItemKind, index int) bool {
	if !d.deleteMode {
		return false
	}
	item := d.selectedDeleteItem()
	return item != nil && item.kind == kind && item.index == index
}

// firstDeleteIndex returns the index into deletableItems() of the first item
// with the given kind, or -1 if none exists.
func (d DetailModel) firstDeleteIndex(kind deleteItemKind) int {
	for i, item := range d.deletableItems() {
		if item.kind == kind {
			return i
		}
	}
	return -1
}

// clampSubtaskCursor ensures the subtask cursor doesn't exceed the list length.
func (d *DetailModel) clampSubtaskCursor() {
	if len(d.subtasks) == 0 {
		d.subtaskCursor = 0
		return
	}
	if d.subtaskCursor >= len(d.subtasks) {
		d.subtaskCursor = len(d.subtasks) - 1
	}
	if d.subtaskCursor < 0 {
		d.subtaskCursor = 0
	}
}

// isDeleteSection reports whether the current cursor is within the given section.
func (d DetailModel) isDeleteSection(kind deleteItemKind) bool {
	if !d.deleteMode {
		return false
	}
	item := d.selectedDeleteItem()
	return item != nil && item.kind == kind
}

// Update handles messages for the detail panel.
func (d DetailModel) Update(msg tea.Msg) (DetailModel, tea.Cmd) {
	// The detail panel is mostly display-only; key actions are handled
	// by the parent Model.
	return d, nil
}

// View renders the task detail panel.
func (d DetailModel) View() string {
	if d.task == nil {
		return subtitleStyle.Render("  Select a task to view details.")
	}

	var sections []string

	// Title and description.
	title := titleStyle.Render(d.task.Title)
	sections = append(sections, title)

	if d.task.Description != "" {
		descWidth := d.width - 2
		if descWidth < 10 {
			descWidth = 10
		}
		sections = append(sections, subtitleStyle.Width(descWidth).Render(d.task.Description))
	}

	// Status line: status, agent, branch.
	var infoParts []string
	infoParts = append(infoParts, fmt.Sprintf("Status: %s", StatusBadge(d.task.Status)))
	if d.agent != nil {
		infoParts = append(infoParts, fmt.Sprintf("Agent: %s", d.agent.Name))
	}
	if d.task.WorktreeBranch != "" {
		branch := d.task.WorktreeBranch
		if len(branch) > 30 {
			branch = "\u2026" + branch[len(branch)-29:]
		}
		infoParts = append(infoParts, fmt.Sprintf("Branch: %s", branch))
	}
	sections = append(sections, strings.Join(infoParts, "  |  "))

	// Plan subtasks (for plan_review, show the proposed plan).
	if d.task.Status == model.StatusPlanReview && d.task.Plan != nil {
		if subtasks, ok := d.task.Plan["subtasks"]; ok {
			if items, ok := subtasks.([]any); ok && len(items) > 0 {
				planHeader := "Plan:"
				planStyle := lipgloss.NewStyle().Foreground(colorWarning)
				if d.isDeleteSection(deleteItemPlanStep) {
					planHeader = "Plan: (select step to delete)"
					planStyle = planStyle.Foreground(colorDanger).Bold(true)
				}
				sections = append(sections, planStyle.Render(planHeader))
				deleteHighlight := lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
				for i, item := range items {
					if m, ok := item.(map[string]any); ok {
						title, _ := m["title"].(string)
						maxTitle := d.width - 8
						if maxTitle > 0 && len(title) > maxTitle {
							title = title[:maxTitle-1] + "\u2026"
						}
						prefix := "  "
						if d.isDeleteTarget(deleteItemPlanStep, i) {
							prefix = "X "
						}
						line := fmt.Sprintf("%s%d. %s", prefix, i+1, title)
						if d.isDeleteTarget(deleteItemPlanStep, i) {
							line = deleteHighlight.Render(line)
						}
						sections = append(sections, line)
					}
				}
			}
		}
	}

	// Subtask progress.
	if len(d.subtasks) > 0 {
		done := 0
		for _, sub := range d.subtasks {
			if sub.Status == model.StatusDone {
				done++
			}
		}
		arrow := "\u25be" // ▾ expanded
		if d.subtasksCollapsed {
			arrow = "\u25b8" // ▸ collapsed
		}
		subtaskHeader := fmt.Sprintf("%s Subtasks: %d/%d complete", arrow, done, len(d.subtasks))
		progressStyle := lipgloss.NewStyle().Foreground(colorInfo)
		if d.isDeleteSection(deleteItemSubtask) {
			subtaskHeader += " (select subtask to delete)"
			progressStyle = progressStyle.Foreground(colorDanger).Bold(true)
		}
		sections = append(sections, progressStyle.Render(subtaskHeader))
		if !d.subtasksCollapsed {
			deleteHighlight := lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
			cursorHighlight := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
			for i, sub := range d.subtasks {
				prefix := "  "
				if d.isDeleteTarget(deleteItemSubtask, i) {
					prefix = "X "
				} else if d.focused && !d.deleteMode && i == d.subtaskCursor {
					prefix = "> "
				}
				line := fmt.Sprintf("%s- [%s] %s", prefix, sub.Status, sub.Title)
				maxLine := d.width - 4
				if maxLine > 0 && len(line) > maxLine {
					line = line[:maxLine-1] + "\u2026"
				}
				if d.isDeleteTarget(deleteItemSubtask, i) {
					line = deleteHighlight.Render(line)
				} else if d.focused && !d.deleteMode && i == d.subtaskCursor {
					line = cursorHighlight.Render(line)
				}
				sections = append(sections, line)
			}
		}
	}

	// Warnings from task context.
	if d.task.Context != nil {
		warnStyle := lipgloss.NewStyle().Foreground(colorDanger)
		if _, ok := d.task.Context["empty_feature"]; ok {
			sections = append(sections, warnStyle.Render(
				"\u26a0 Feature branch has no changes — subtasks completed without producing code"))
		}
		if _, ok := d.task.Context["empty_work"]; ok {
			sections = append(sections, warnStyle.Render(
				"\u26a0 Agent completed without committing any changes"))
		}
		if reason, ok := d.task.Context["failure_reason"].(string); ok && d.task.Status == model.StatusFailed {
			sections = append(sections, warnStyle.Render("Reason: "+reason))
		}
		if diag, ok := d.task.Context["failure_diagnosis"].(string); ok {
			sections = append(sections, subtitleStyle.Render("Diagnosis: "+diag))
		}
	}

	// Comment thread.
	if len(d.comments) > 0 {
		commentHeader := fmt.Sprintf("Comments (%d):", len(d.comments))
		commentStyle := lipgloss.NewStyle().Foreground(colorInfo)
		if d.isDeleteSection(deleteItemComment) {
			commentHeader += " (select comment to delete)"
			commentStyle = commentStyle.Foreground(colorDanger).Bold(true)
		}
		sections = append(sections, commentStyle.Render(commentHeader))
		deleteHighlight := lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
		commentWidth := d.width - 2
		if commentWidth < 10 {
			commentWidth = 10
		}
		for i, c := range d.comments {
			prefix := "  "
			if d.isDeleteTarget(deleteItemComment, i) {
				prefix = "X "
			} else if i == len(d.comments)-1 {
				prefix = "> "
			}
			line := fmt.Sprintf("%s[%s] %s", prefix, c.Author, c.Body)
			if d.isDeleteTarget(deleteItemComment, i) {
				line = deleteHighlight.Width(commentWidth).Render(line)
			} else {
				line = lipgloss.NewStyle().Width(commentWidth).Render(line)
			}
			sections = append(sections, line)
		}
	}

	// Log preview — fill remaining available height.
	if d.logText != "" {
		usedLines := len(sections) + 2 // +2 for actions line and padding
		maxLines := d.height - usedLines
		if maxLines < 3 {
			maxLines = 3
		}
		logLines := strings.Split(d.logText, "\n")
		if len(logLines) > maxLines {
			logLines = logLines[len(logLines)-maxLines:]
		}
		logPreview := subtitleStyle.Render(strings.Join(logLines, "\n"))
		sections = append(sections, "Log: "+logPreview)
	}

	// Available actions based on status.
	actions := d.availableActions()
	if actions != "" {
		sections = append(sections, "")
		sections = append(sections, helpStyle.Render(actions))
	}

	// Flatten sections into individual lines for per-line scrolling.
	var lines []string
	for _, s := range sections {
		lines = append(lines, strings.Split(s, "\n")...)
	}

	// Apply vertical scrolling.
	if d.height > 0 && len(lines) > d.height {
		if d.scrollOffset > len(lines)-d.height {
			d.scrollOffset = len(lines) - d.height
		}
		lines = lines[d.scrollOffset:]
		if len(lines) > d.height {
			lines = lines[:d.height]
		}
	}

	return strings.Join(lines, "\n")
}

// availableActions returns a string describing the key actions available
// for the current task state.
func (d DetailModel) availableActions() string {
	if d.task == nil {
		return ""
	}

	if d.deleteMode {
		target := "item"
		if item := d.selectedDeleteItem(); item != nil {
			switch item.kind {
			case deleteItemPlanStep:
				target = "plan step"
			case deleteItemSubtask:
				target = "subtask (+ agent)"
			case deleteItemComment:
				target = "comment"
			}
		}
		return fmt.Sprintf("[j/k] navigate  [enter/y] delete %s  [esc] cancel", target)
	}

	var parts []string

	switch d.task.Status {
	case model.StatusPlanReview:
		parts = append(parts, "[a]pprove plan", "[r]eject plan", "[c]omment", "[d]elete")
	case model.StatusTestingReady:
		parts = append(parts, "[c]omment", "[d]elete")
	case model.StatusManualTesting:
		parts = append(parts, "[t]est pass", "[f]ail test", "[c]omment", "[d]elete")
	case model.StatusInProgress:
		parts = append(parts, "[p]ause", "[d]elete")
	case model.StatusPaused:
		parts = append(parts, "[p] resume")
	case model.StatusFailed:
		parts = append(parts, "[R]etry")
	}

	// Supervisor evaluation is always available.
	parts = append(parts, "[S]upervisor")

	// Agent-specific actions.
	if d.agent != nil && d.agent.TmuxSession != "" {
		parts = append(parts, "[g] jump to agent", "[l] view log")
	}

	return strings.Join(parts, "  ")
}
