package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
)

// depInfo holds a dependency task's title and status for display.
type depInfo struct {
	Title  string
	Status model.TaskStatus
}

// deleteItemKind distinguishes the type of item being selected for deletion.
type deleteItemKind int

const (
	deleteItemPlanStep deleteItemKind = iota
	deleteItemComment
	deleteItemTask
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

	deps []depInfo // resolved dependency tasks

	scrollOffset int  // vertical scroll offset for detail content
	focused      bool // true when the detail panel has focus
	deleteMode   bool // true when selecting an item to delete
	deleteCursor int  // index into deletableItems()
}

// NewDetailModel creates an empty DetailModel.
func NewDetailModel() DetailModel {
	return DetailModel{}
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

	// Comments.
	for i := range d.comments {
		items = append(items, deleteItem{kind: deleteItemComment, index: i})
	}

	// The task itself (always available as last item).
	if d.task != nil {
		items = append(items, deleteItem{kind: deleteItemTask, index: 0})
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
		if len(branch) > maxBranchDisplayLen {
			branch = "\u2026" + branch[len(branch)-maxBranchDisplayLen+1:]
		}
		infoParts = append(infoParts, fmt.Sprintf("Branch: %s", branch))
	}
	sections = append(sections, strings.Join(infoParts, "  |  "))

	// Dependencies.
	if len(d.deps) > 0 {
		depStyle := lipgloss.NewStyle().Foreground(colorWarning)
		sections = append(sections, depStyle.Render("Depends on:"))
		for _, dep := range d.deps {
			badge := StatusBadge(dep.Status)
			sections = append(sections, fmt.Sprintf("  %s  %s", badge, dep.Title))
		}
	}

	// Plan subtasks. Plans remain useful after approval, so show them for any
	// task status when the orchestrator has stored one.
	if d.task.Plan != nil {
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
						if desc, _ := m["description"].(string); desc != "" {
							sections = append(sections, subtitleStyle.Render("     "+desc))
						}
						if phase, _ := m["phase"].(string); phase != "" {
							sections = append(sections, subtitleStyle.Render("     phase: "+phase))
						}
						if files, ok := m["estimated_files"].([]any); ok && len(files) > 0 {
							var names []string
							for _, file := range files {
								if s, ok := file.(string); ok && s != "" {
									names = append(names, s)
								}
							}
							if len(names) > 0 {
								sections = append(sections, subtitleStyle.Render("     files: "+strings.Join(names, ", ")))
							}
						}
					}
				}
			}
		}
	}

	// Clarification questions (for needs_clarification status).
	if d.task.Status == model.StatusNeedsClarification && d.task.Context != nil {
		clarStyle := lipgloss.NewStyle().Foreground(colorWarning)
		sections = append(sections, clarStyle.Render("Clarification Needed:"))

		// Show all questions with answered/pending indicators.
		if questions, ok := d.task.Context["clarification_questions"].([]any); ok {
			for i, q := range questions {
				if qs, ok := q.(string); ok {
					prefix := "  ? "
					sections = append(sections, fmt.Sprintf("%s%d. %s", prefix, i+1, qs))
				}
			}
		}

		// Highlight the current question.
		if current, ok := d.task.Context["clarification_current_question"].(string); ok {
			currentStyle := lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
			sections = append(sections, "")
			sections = append(sections, currentStyle.Render("Current question:"))
			sections = append(sections, currentStyle.Render("  "+current))
			sections = append(sections, subtitleStyle.Render("  Press [c] to answer, or type /done to accept plan as-is"))
		}
	}

	// Review results from reviewer agent.
	if d.task.Context != nil {
		if review, ok := d.task.Context["review"].(map[string]any); ok {
			reviewStyle := lipgloss.NewStyle().Foreground(colorInfo)
			sections = append(sections, reviewStyle.Render("Review:"))

			if rec, ok := review["recommendation"].(string); ok {
				recStyle := lipgloss.NewStyle().Foreground(colorInfo)
				if rec == "reject" || rec == "needs_work" {
					recStyle = lipgloss.NewStyle().Foreground(colorDanger)
				} else if rec == "approve" {
					recStyle = lipgloss.NewStyle().Foreground(colorSuccess)
				}
				sections = append(sections, fmt.Sprintf("  Recommendation: %s", recStyle.Render(rec)))
			}
			if coverage, ok := review["coverage"].(string); ok {
				sections = append(sections, fmt.Sprintf("  Coverage: %s", coverage))
			}
			if buildPasses, ok := review["build_passes"].(bool); ok {
				sections = append(sections, fmt.Sprintf("  Build passes: %v", buildPasses))
			}
			if testsPasses, ok := review["tests_pass"].(bool); ok {
				sections = append(sections, fmt.Sprintf("  Tests pass: %v", testsPasses))
			}
			if issues, ok := review["issues"].([]any); ok && len(issues) > 0 {
				sections = append(sections, "  Issues:")
				for _, issue := range issues {
					if s, ok := issue.(string); ok {
						sections = append(sections, fmt.Sprintf("    - %s", s))
					}
				}
			}
		}
	}

	// Step scores.
	if d.task.Context != nil {
		if scoresRaw, ok := d.task.Context["scores"].(map[string]any); ok {
			scoreStr := renderScoreSection(scoresRaw)
			if scoreStr != "" {
				sections = append(sections, scoreStr)
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

	// Exit info from agent Config.
	if d.agent != nil && d.agent.Config != nil {
		if exitReason, ok := d.agent.Config["exit_reason"].(string); ok {
			exitStyle := lipgloss.NewStyle().Foreground(colorWarning)
			exitLine := fmt.Sprintf("Exit: %s", exitReason)
			if tool, ok := d.agent.Config["exit_last_tool"].(string); ok && tool != "" {
				exitLine += fmt.Sprintf(" (last: %s)", tool)
			}
			sections = append(sections, exitStyle.Render(exitLine))
			if summary, ok := d.agent.Config["exit_summary"].(string); ok && summary != "" {
				sections = append(sections, subtitleStyle.Render("  "+summary))
			}
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
			case deleteItemComment:
				target = "comment"
			}
		}
		return fmt.Sprintf("[j/k] navigate  [enter/y] delete %s  [esc] cancel", target)
	}

	var parts []string

	switch d.task.Status {
	case model.StatusPlanReview:
		parts = append(parts, "[a]pprove plan", "[r]eject plan", "[v]review", "[c]omment", "[d]elete")
	case model.StatusTestingReady:
		parts = append(parts, "[t]est pass", "[f]ail test", "[v]review", "[x]fix", "[c]omment", "[d]elete")
	case model.StatusInProgress:
		parts = append(parts, "[p]ause", "[x]fix", "[d]elete")
	case model.StatusPaused:
		parts = append(parts, "[p] resume", "[d]elete")
	case model.StatusNeedsClarification:
		parts = append(parts, "[c]larify (answer question)", "[p]ause")
	case model.StatusFailed:
		parts = append(parts, "[R]etry", "[x]fix", "[d]elete")
	}

	// Supervisor evaluation is always available.
	parts = append(parts, "[S]upervisor")

	// Agent-specific actions.
	if d.agent != nil {
		parts = append(parts, "[l] view log")
		if workeridentity.FromAgent(*d.agent).CanJumpToTmux() {
			parts = append(parts, "[g] jump to agent")
		}
	}

	return strings.Join(parts, "  ")
}

// renderScoreSection renders the step score block for the detail view.
func renderScoreSection(scores map[string]any) string {
	formatted, ok := scores["formatted"].(string)
	if !ok {
		return ""
	}
	style := lipgloss.NewStyle().Bold(true)
	tdd, _ := scores["tdd"].(float64)
	constitution, _ := scores["constitution"].(float64)
	docs, _ := scores["documentation"].(float64)

	color := colorSuccess
	if tdd < 0.5 || constitution < 0.5 {
		color = colorDanger
	} else if tdd < 0.8 || docs < 0.5 {
		color = colorWarning
	}
	return style.Foreground(color).Render("Scores: " + formatted)
}

// taskActionBindings returns the action bindings available for a given task
// based on its status and assigned agent.
func (m Model) taskActionBindings(task *model.Task) []helpBinding {
	var bindings []helpBinding

	switch task.Status {
	case model.StatusPlanReview:
		bindings = append(bindings,
			helpBinding{"a", "approve plan"},
			helpBinding{"r", "reject plan"},
			helpBinding{"v", "spawn reviewer"},
			helpBinding{"c", "add comment"},
			helpBinding{"d", "delete item"},
		)
	case model.StatusTestingReady:
		bindings = append(bindings,
			helpBinding{"t", "pass test"},
			helpBinding{"f", "fail test"},
			helpBinding{"v", "spawn reviewer"},
			helpBinding{"x", "spawn fixer"},
			helpBinding{"c", "add comment"},
			helpBinding{"d", "delete item"},
		)
	case model.StatusInProgress:
		bindings = append(bindings,
			helpBinding{"p", "pause"},
			helpBinding{"x", "spawn fixer"},
			helpBinding{"d", "delete item"},
		)
	case model.StatusPaused:
		bindings = append(bindings, helpBinding{"p", "resume"})
	case model.StatusFailed:
		bindings = append(bindings,
			helpBinding{"R", "retry"},
			helpBinding{"x", "spawn fixer"},
		)
	}

	bindings = append(bindings, helpBinding{"S", "supervisor eval"})

	if m.detail.agent != nil {
		bindings = append(bindings, helpBinding{"l", "view agent log"})
		bindings = append(bindings, helpBinding{"g", "jump to agent"})
	}

	bindings = append(bindings,
		helpBinding{"L", "orchestrator log"},
		helpBinding{"i", "open shell"},
	)

	return bindings
}

// renderStatusBar shows task counts per status.
func (m Model) renderStatusBar() string {
	counts := make(map[model.TaskStatus]int)
	for _, task := range m.board.tasks {
		counts[task.Status]++
	}

	order := []model.TaskStatus{
		model.StatusBacklog,
		model.StatusPlanning,
		model.StatusPlanReview,
		model.StatusInProgress,
		model.StatusTestingReady,
		model.StatusMerging,
		model.StatusDone,
		model.StatusFailed,
	}

	var badges []string
	for _, s := range order {
		c := counts[s]
		if c == 0 {
			continue
		}
		color, ok := statusColors[s]
		if !ok {
			color = lipgloss.Color("241")
		}
		badge := lipgloss.NewStyle().
			Foreground(color).
			Render(fmt.Sprintf("[%s: %d]", strings.ToTitle(string(s)), c))
		badges = append(badges, badge)
	}

	if len(badges) == 0 {
		return subtitleStyle.Render("  No tasks")
	}
	return "  " + strings.Join(badges, " ")
}
