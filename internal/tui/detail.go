package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// DetailModel renders task details and available actions.
type DetailModel struct {
	task     *model.Task
	subtasks []model.Task
	agent    *model.Agent
	logText  string
	width    int
	height   int
}

// NewDetailModel creates an empty DetailModel.
func NewDetailModel() DetailModel {
	return DetailModel{}
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
		desc := d.task.Description
		maxDesc := d.width - 4
		if maxDesc > 0 && len(desc) > maxDesc {
			desc = desc[:maxDesc-1] + "\u2026"
		}
		sections = append(sections, subtitleStyle.Render(desc))
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

	// Subtask progress.
	if len(d.subtasks) > 0 {
		done := 0
		for _, sub := range d.subtasks {
			if sub.Status == model.StatusDone {
				done++
			}
		}
		progressStyle := lipgloss.NewStyle().Foreground(colorInfo)
		sections = append(sections, progressStyle.Render(
			fmt.Sprintf("Subtasks: %d/%d complete", done, len(d.subtasks)),
		))
	}

	// Log preview.
	if d.logText != "" {
		logLines := strings.Split(d.logText, "\n")
		maxLines := 5
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

	return strings.Join(sections, "\n")
}

// availableActions returns a string describing the key actions available
// for the current task state.
func (d DetailModel) availableActions() string {
	if d.task == nil {
		return ""
	}

	var parts []string

	switch d.task.Status {
	case model.StatusPlanReview:
		parts = append(parts, "[a]pprove plan", "[r]eject plan")
	case model.StatusManualTesting:
		parts = append(parts, "[t]est pass", "[f]ail test")
	case model.StatusInProgress:
		parts = append(parts, "[p]ause")
	case model.StatusPaused:
		parts = append(parts, "[p] resume")
	case model.StatusFailed:
		parts = append(parts, "[R]etry")
	}

	// Agent-specific actions.
	if d.agent != nil && d.agent.TmuxWindow != "" {
		parts = append(parts, "[g] jump to agent", "[l] view log")
	}

	return strings.Join(parts, "  ")
}
