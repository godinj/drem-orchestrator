package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/bugreport"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// bugReportSvc is a package-local alias so that app.go can reference the type
// without importing the bugreport package directly (reducing the tui import count).
type bugReportSvc = bugreport.Service

// BugReportsModel manages the bug report list screen.
type BugReportsModel struct {
	db           *gorm.DB
	bugreportSvc *bugReportSvc
	projectID    uuid.UUID
	reports      []model.BugReport
	cursor       int
	scrollOffset int
	width        int
	height       int
	filters      bugReportFilters

	// Detail state (inline, not a separate sub-model)
	showDetail     bool
	selectedReport *model.BugReport
	comments       []model.BugReportComment
	detailScroll   int

	// Filter UI state
	filterMode   bool
	filterCursor int // which filter dimension is selected (0=category, 1=severity, 2=status)

	// Delete confirmation
	confirmDelete bool
}

// bugReportFilters holds the active filter selections for the bug report list.
type bugReportFilters struct {
	category      *model.BugReportCategory
	severity      *model.BugReportSeverity
	status        *model.BugReportStatus
	showDismissed bool // when false (default), dismissed reports are hidden
}

// NewBugReportsModel creates a BugReportsModel.
func NewBugReportsModel(db *gorm.DB, svc *bugreport.Service, projectID uuid.UUID) BugReportsModel {
	return BugReportsModel{
		db:           db,
		bugreportSvc: svc,
		projectID:    projectID,
	}
}

// filteredReports returns the reports matching the current filters.
func (b BugReportsModel) filteredReports() []model.BugReport {
	var result []model.BugReport
	for _, r := range b.reports {
		if !b.filters.showDismissed && r.Status == model.BugStatusDismissed {
			continue
		}
		if b.filters.category != nil && r.Category != *b.filters.category {
			continue
		}
		if b.filters.severity != nil && r.Severity != *b.filters.severity {
			continue
		}
		if b.filters.status != nil && r.Status != *b.filters.status {
			continue
		}
		result = append(result, r)
	}
	return result
}

// Selected returns the currently highlighted bug report, or nil if the list is empty.
func (b BugReportsModel) Selected() *model.BugReport {
	reports := b.filteredReports()
	if len(reports) == 0 {
		return nil
	}
	idx := b.cursor
	if idx >= len(reports) {
		idx = len(reports) - 1
	}
	if idx < 0 {
		return nil
	}
	r := reports[idx]
	return &r
}

// adjustScroll clamps scrollOffset so the cursor stays visible.
func (b *BugReportsModel) adjustScroll() {
	count := len(b.filteredReports())
	listHeight := b.listHeight()
	if listHeight <= 0 || count <= listHeight {
		b.scrollOffset = 0
		return
	}
	if b.cursor < b.scrollOffset {
		b.scrollOffset = b.cursor
	}
	if b.cursor >= b.scrollOffset+listHeight {
		b.scrollOffset = b.cursor - listHeight + 1
	}
	maxScroll := count - listHeight
	if b.scrollOffset > maxScroll {
		b.scrollOffset = maxScroll
	}
	if b.scrollOffset < 0 {
		b.scrollOffset = 0
	}
}

// listHeight returns the available line count for the list portion of the view.
func (b BugReportsModel) listHeight() int {
	h := b.height
	h -= 2 // header + filter bar
	if b.showDetail {
		h = h * 4 / 10 // 40% for list when detail is shown
	}
	if h < 3 {
		h = 3
	}
	return h
}

// View renders the bug report screen.
func (b BugReportsModel) View() string {
	var sections []string

	// Header with title and filter indicators.
	header := titleStyle.Render("Bug Reports")
	filterIndicators := b.renderFilterIndicators()
	if filterIndicators != "" {
		header += "  " + filterIndicators
	}
	sections = append(sections, header)

	// Filter bar (when in filter mode).
	if b.filterMode {
		sections = append(sections, b.renderFilterBar())
	}

	// List view.
	reports := b.filteredReports()
	if len(reports) == 0 {
		sections = append(sections, subtitleStyle.Render("  No bug reports found."))
	} else {
		sections = append(sections, b.renderList(reports))
	}

	// Detail pane (when toggled).
	if b.showDetail && b.selectedReport != nil {
		sections = append(sections, "")
		sections = append(sections, b.renderDetail())
	}

	// Help bar.
	sections = append(sections, "")
	if b.confirmDelete {
		sections = append(sections, lipglossRender(colorDanger, "Delete bug report? [y]es  [n]o"))
	} else if b.filterMode {
		sections = append(sections, helpStyle.Render("[tab] cycle dimension  [j/k] cycle value  [enter] apply  [esc] cancel"))
	} else {
		sections = append(sections, helpStyle.Render("[j/k] navigate  [enter] detail  [a]ck  [p]romote  [D]ismiss  [x] delete  [c]omment  [/] filter  [b/esc] back"))
	}

	return strings.Join(sections, "\n")
}

// renderList renders the bug report list with columns.
func (b BugReportsModel) renderList(reports []model.BugReport) string {
	listHeight := b.listHeight()

	// Calculate column widths.
	// Format: severity(2) + category(14) + title(flex) + status(14) + timestamp(8)
	categoryWidth := 14
	statusWidth := 14
	timeWidth := 8
	fixedWidth := 2 + 1 + categoryWidth + 1 + statusWidth + 1 + timeWidth + 4 // separators + padding
	titleWidth := b.width - fixedWidth
	if titleWidth < 10 {
		titleWidth = 10
	}

	var lines []string
	for i, report := range reports {
		// Severity icon.
		sevIcon := bugSeverityIcon(report.Severity)

		// Category tag.
		cat := fmt.Sprintf("[%s]", report.Category)
		if len(cat) > categoryWidth {
			cat = cat[:categoryWidth-1] + "\u2026"
		}
		cat = fmt.Sprintf("%-*s", categoryWidth, cat)
		cat = bugCategoryStyle.Render(cat)

		// Title.
		title := report.Title
		if len(title) > titleWidth {
			title = title[:titleWidth-1] + "\u2026"
		}
		title = fmt.Sprintf("%-*s", titleWidth, title)

		// Status.
		statusStr := bugReportStatusBadge(report.Status)

		// Timestamp.
		ts := relativeTime(report.CreatedAt)

		line := fmt.Sprintf("  %s %s %s %s %s", sevIcon, cat, title, statusStr, ts)

		if i == b.cursor {
			line = selectedStyle.Width(b.width).Render(
				fmt.Sprintf("> %s %s %s %s %s", sevIcon, cat, title, statusStr, ts),
			)
		}

		lines = append(lines, line)
	}

	// Apply scrolling.
	visible := lines
	if listHeight > 0 && len(visible) > listHeight {
		start := b.scrollOffset
		if start > len(visible)-listHeight {
			start = len(visible) - listHeight
		}
		if start < 0 {
			start = 0
		}
		visible = visible[start : start+listHeight]
	}

	return strings.Join(visible, "\n")
}

// renderDetail renders the detail pane for the selected bug report.
func (b BugReportsModel) renderDetail() string {
	r := b.selectedReport
	if r == nil {
		return ""
	}

	var sections []string

	// Separator.
	sep := lipgloss.NewStyle().Foreground(colorSecondary).Render(strings.Repeat("\u2500", b.width))
	sections = append(sections, sep)

	// Title.
	sections = append(sections, titleStyle.Render(r.Title))

	// Status line.
	var infoParts []string
	infoParts = append(infoParts, fmt.Sprintf("Status: %s", bugReportStatusBadge(r.Status)))
	infoParts = append(infoParts, fmt.Sprintf("Severity: %s", bugSeverityIcon(r.Severity)))
	infoParts = append(infoParts, fmt.Sprintf("Category: %s", r.Category))
	sections = append(sections, strings.Join(infoParts, "  |  "))

	// Description.
	if r.Description != "" {
		descWidth := b.width - 2
		if descWidth < 10 {
			descWidth = 10
		}
		sections = append(sections, "")
		sections = append(sections, subtitleStyle.Width(descWidth).Render(r.Description))
	}

	// Reproduction context.
	if r.ReproductionContext != "" {
		sections = append(sections, "")
		sections = append(sections, lipgloss.NewStyle().Foreground(colorWarning).Render("Reproduction Context:"))
		ctxWidth := b.width - 4
		if ctxWidth < 10 {
			ctxWidth = 10
		}
		sections = append(sections, subtitleStyle.Width(ctxWidth).Render("  "+r.ReproductionContext))
	}

	// Agent and task info.
	if r.AgentID != nil {
		sections = append(sections, subtitleStyle.Render(fmt.Sprintf("  Agent: %s", r.AgentID)))
	}
	if r.TaskID != nil {
		sections = append(sections, subtitleStyle.Render(fmt.Sprintf("  Task: %s", r.TaskID)))
	}
	if r.PromotedTaskID != nil {
		sections = append(sections, lipgloss.NewStyle().Foreground(colorSuccess).Render(
			fmt.Sprintf("  Promoted to task: %s", r.PromotedTaskID)))
	}

	// Timestamp.
	sections = append(sections, subtitleStyle.Render(
		fmt.Sprintf("  Filed: %s", r.CreatedAt.Format("2006-01-02 15:04:05"))))

	// Comments.
	if len(b.comments) > 0 {
		sections = append(sections, "")
		commentHeader := fmt.Sprintf("Comments (%d):", len(b.comments))
		sections = append(sections, lipgloss.NewStyle().Foreground(colorInfo).Render(commentHeader))
		commentWidth := b.width - 2
		if commentWidth < 10 {
			commentWidth = 10
		}
		for _, c := range b.comments {
			ts := relativeTime(c.CreatedAt)
			line := fmt.Sprintf("  [%s] %s  %s", c.Author, c.Body, ts)
			sections = append(sections, lipgloss.NewStyle().Width(commentWidth).Render(line))
		}
	}

	// Flatten to lines for scrolling.
	var lines []string
	for _, s := range sections {
		lines = append(lines, strings.Split(s, "\n")...)
	}

	// Calculate available detail height.
	detailHeight := b.height - b.listHeight() - 6 // 6 for header, filter, help, spacers
	if detailHeight < 3 {
		detailHeight = 3
	}

	// Apply scrolling.
	if detailHeight > 0 && len(lines) > detailHeight {
		if b.detailScroll > len(lines)-detailHeight {
			b.detailScroll = len(lines) - detailHeight
		}
		if b.detailScroll < 0 {
			b.detailScroll = 0
		}
		lines = lines[b.detailScroll:]
		if len(lines) > detailHeight {
			lines = lines[:detailHeight]
		}
	}

	return strings.Join(lines, "\n")
}

// renderFilterIndicators shows active filters in the header.
func (b BugReportsModel) renderFilterIndicators() string {
	var parts []string
	if b.filters.category != nil {
		parts = append(parts, fmt.Sprintf("cat:%s", *b.filters.category))
	}
	if b.filters.severity != nil {
		parts = append(parts, fmt.Sprintf("sev:%s", *b.filters.severity))
	}
	if b.filters.status != nil {
		parts = append(parts, fmt.Sprintf("status:%s", *b.filters.status))
	}
	if b.filters.showDismissed {
		parts = append(parts, "+dismissed")
	}
	if len(parts) == 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorWarning).Render("[" + strings.Join(parts, " ") + "]")
}

// renderFilterBar renders the interactive filter bar.
func (b BugReportsModel) renderFilterBar() string {
	dimensions := []string{"Category", "Severity", "Status", "Dismissed"}
	var parts []string
	for i, dim := range dimensions {
		var val string
		switch i {
		case 0:
			if b.filters.category != nil {
				val = string(*b.filters.category)
			} else {
				val = "all"
			}
		case 1:
			if b.filters.severity != nil {
				val = string(*b.filters.severity)
			} else {
				val = "all"
			}
		case 2:
			if b.filters.status != nil {
				val = string(*b.filters.status)
			} else {
				val = "all"
			}
		case 3:
			if b.filters.showDismissed {
				val = "yes"
			} else {
				val = "no"
			}
		}
		label := fmt.Sprintf("%s: %s", dim, val)
		if i == b.filterCursor {
			label = lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Render("> " + label)
		} else {
			label = subtitleStyle.Render("  " + label)
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

// bugSeverityIcon returns a colored icon for the severity level.
func bugSeverityIcon(sev model.BugReportSeverity) string {
	switch sev {
	case model.BugSeverityBlocking:
		return lipgloss.NewStyle().Foreground(colorDanger).Bold(true).Render("!")
	case model.BugSeverityDegraded:
		return lipgloss.NewStyle().Foreground(colorWarning).Render("~")
	case model.BugSeverityInformational:
		return lipgloss.NewStyle().Foreground(colorSecondary).Render("\u00b7") // middle dot
	default:
		return "?"
	}
}

// bugReportStatusBadge returns a colored status badge for a bug report status.
func bugReportStatusBadge(status model.BugReportStatus) string {
	var color lipgloss.Color
	switch status {
	case model.BugStatusOpen:
		color = lipgloss.Color("252") // white
	case model.BugStatusAcknowledged:
		color = lipgloss.Color("14") // cyan
	case model.BugStatusPromoted:
		color = colorSuccess
	case model.BugStatusDismissed:
		color = colorSecondary
	default:
		color = colorSecondary
	}
	return lipgloss.NewStyle().Foreground(color).Render(string(status))
}

// relativeTime returns a human-readable relative time string.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	}
}

// allBugCategories returns all bug report categories for filter cycling.
var allBugCategories = []model.BugReportCategory{
	model.BugCategoryTooling,
	model.BugCategoryMergeConflict,
	model.BugCategoryRequirements,
	model.BugCategoryConstraintViolation,
	model.BugCategoryUpstreamCode,
	model.BugCategoryTestFailure,
	model.BugCategoryEnvironment,
	model.BugCategoryOther,
}

// allBugSeverities returns all bug report severities for filter cycling.
var allBugSeverities = []model.BugReportSeverity{
	model.BugSeverityBlocking,
	model.BugSeverityDegraded,
	model.BugSeverityInformational,
}

// allBugStatuses returns all bug report statuses for filter cycling.
var allBugStatuses = []model.BugReportStatus{
	model.BugStatusOpen,
	model.BugStatusAcknowledged,
	model.BugStatusPromoted,
	model.BugStatusDismissed,
}

// loadBugReports returns a Cmd that loads bug reports from the service.
func (m Model) loadBugReports() tea.Cmd {
	svc := m.bugreportSvc
	projectID := m.projectID
	return func() tea.Msg {
		if svc == nil {
			return bugReportsLoadedMsg{reports: nil}
		}
		reports, err := svc.List(bugreport.ListFilters{
			ProjectID: &projectID,
		})
		if err != nil {
			return bugReportsLoadedMsg{reports: nil}
		}
		return bugReportsLoadedMsg{reports: reports}
	}
}

// handleBugReportPromote starts the promotion workflow: create temp file, spawn editor.
func (m Model) handleBugReportPromote(report *model.BugReport) (tea.Model, tea.Cmd) {
	// Create temp file with title and description.
	tmpFile, err := os.CreateTemp("", "bugreport-promote-*.md")
	if err != nil {
		m.err = fmt.Errorf("create temp file: %w", err)
		return m, nil
	}

	content := report.Title + "\n\n" + report.Description
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		m.err = fmt.Errorf("write temp file: %w", err)
		return m, nil
	}
	tmpFile.Close()

	// Determine editor.
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	tmpPath := tmpFile.Name()
	bugReportID := report.ID

	// Use tea.ExecProcess to spawn the editor.
	editorCmd := exec.Command(editor, tmpPath)
	return m, tea.ExecProcess(editorCmd, func(err error) tea.Msg {
		return editorFinishedMsg{tempFile: tmpPath, bugReportID: bugReportID, err: err}
	})
}
