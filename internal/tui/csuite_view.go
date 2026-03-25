package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// csuiteSnapshotMsg carries a new state snapshot from the csuite poller.
type csuiteSnapshotMsg struct {
	snapshot csuite.StateSnapshot
}

// CsuiteModel renders the C-Suite agent dashboard screen.
type CsuiteModel struct {
	snapshot     *csuite.StateSnapshot
	cursor       int
	scrollOffset int
	width        int
	height       int
}

// NewCsuiteModel creates an empty CsuiteModel.
func NewCsuiteModel() CsuiteModel {
	return CsuiteModel{}
}

// View renders the C-Suite dashboard screen.
func (c CsuiteModel) View() string {
	var sections []string

	// Header.
	sections = append(sections, titleStyle.Render("C-Suite Dashboard"))

	if c.snapshot == nil {
		sections = append(sections, subtitleStyle.Render("  Loading C-Suite data..."))
		sections = append(sections, "")
		sections = append(sections, helpStyle.Render("[w/esc] back to dashboard"))
		return strings.Join(sections, "\n")
	}

	// Agent health table.
	sections = append(sections, c.renderAgentHealthTable())

	// Pipeline summary.
	sections = append(sections, "")
	sections = append(sections, c.renderPipelineSummary())

	// Timestamp.
	sections = append(sections, "")
	ts := c.snapshot.Timestamp.Format("15:04:05")
	sections = append(sections, subtitleStyle.Render(fmt.Sprintf("  Last refresh: %s", ts)))

	// Help bar.
	sections = append(sections, "")
	sections = append(sections, helpStyle.Render("[j/k] navigate  [w/esc] back to dashboard  [?] help"))

	return strings.Join(sections, "\n")
}

// renderAgentHealthTable renders the agent health table with status, heartbeat,
// context usage, inbox count, and current activity.
func (c CsuiteModel) renderAgentHealthTable() string {
	agents := c.snapshot.AgentSummaries
	if len(agents) == 0 {
		return subtitleStyle.Render("  No C-Suite agents registered.")
	}

	// Column headers.
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorInfo)
	header := fmt.Sprintf("  %-20s %-10s %-12s %-8s %-8s %s",
		"Agent", "Status", "Heartbeat", "Ctx%", "Inbox", "Activity")
	var lines []string
	lines = append(lines, headerStyle.Render(header))
	lines = append(lines, subtitleStyle.Render("  "+strings.Repeat("─", c.width-4)))

	for i, summary := range agents {
		// Name.
		name := summary.Name
		if len(name) > 20 {
			name = name[:19] + "…"
		}

		// Status badge.
		statusStr := agentMonStatusBadge(summary.Status)

		// Heartbeat age.
		heartbeat := heartbeatAge(summary.HeartbeatAt)

		// Context %.
		ctxPct := fmt.Sprintf("%d%%", summary.ContextPercent)
		ctxStyle := subtitleStyle
		if summary.ContextPercent > 90 {
			ctxStyle = lipgloss.NewStyle().Foreground(colorDanger)
		} else if summary.ContextPercent > 75 {
			ctxStyle = lipgloss.NewStyle().Foreground(colorWarning)
		}
		ctxPct = ctxStyle.Render(ctxPct)

		// Inbox count.
		inboxCount := c.snapshot.InboxCounts[summary.Name]
		inboxStr := fmt.Sprintf("%d", inboxCount)
		if inboxCount > 0 {
			inboxStr = lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render(inboxStr)
		}

		// Activity.
		activity := summary.CurrentActivity
		maxActivity := c.width - 70
		if maxActivity < 10 {
			maxActivity = 10
		}
		if len(activity) > maxActivity {
			activity = activity[:maxActivity-1] + "…"
		}

		line := fmt.Sprintf("  %-20s %-10s %-12s %-8s %-8s %s",
			name, statusStr, heartbeat, ctxPct, inboxStr, activity)

		if i == c.cursor {
			line = selectedStyle.Width(c.width).Render(
				fmt.Sprintf("> %-19s %-10s %-12s %-8s %-8s %s",
					name, statusStr, heartbeat, ctxPct, inboxStr, activity))
		}

		lines = append(lines, line)
	}

	// Apply scrolling.
	listHeight := c.listHeight()
	visible := lines
	if listHeight > 0 && len(visible) > listHeight {
		// Keep header always visible — scroll only data rows.
		headerLines := visible[:2]
		dataLines := visible[2:]
		start := c.scrollOffset
		if start > len(dataLines)-listHeight+2 {
			start = len(dataLines) - listHeight + 2
		}
		if start < 0 {
			start = 0
		}
		end := start + listHeight - 2
		if end > len(dataLines) {
			end = len(dataLines)
		}
		visible = append(headerLines, dataLines[start:end]...)
	}

	return strings.Join(visible, "\n")
}

// renderPipelineSummary shows aggregate inbox counts across all agents.
func (c CsuiteModel) renderPipelineSummary() string {
	totalUnread := 0
	for _, count := range c.snapshot.InboxCounts {
		totalUnread += count
	}

	summaryStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	parts := []string{
		summaryStyle.Render("  Pipeline Summary"),
	}

	agentCount := len(c.snapshot.AgentSummaries)
	onlineCount := 0
	staleCount := 0
	offlineCount := 0
	for _, a := range c.snapshot.AgentSummaries {
		switch a.Status {
		case csuite.AgentMonOnline:
			onlineCount++
		case csuite.AgentMonStale:
			staleCount++
		case csuite.AgentMonOffline:
			offlineCount++
		}
	}

	var badges []string
	badges = append(badges,
		lipglossRender(colorInfo, fmt.Sprintf("[Agents: %d]", agentCount)),
	)
	if onlineCount > 0 {
		badges = append(badges,
			lipglossRender(colorSuccess, fmt.Sprintf("[Online: %d]", onlineCount)),
		)
	}
	if staleCount > 0 {
		badges = append(badges,
			lipglossRender(colorWarning, fmt.Sprintf("[Stale: %d]", staleCount)),
		)
	}
	if offlineCount > 0 {
		badges = append(badges,
			lipglossRender(colorSecondary, fmt.Sprintf("[Offline: %d]", offlineCount)),
		)
	}
	badges = append(badges,
		lipglossRender(colorWarning, fmt.Sprintf("[Unread: %d]", totalUnread)),
	)

	parts = append(parts, "  "+strings.Join(badges, " "))

	return strings.Join(parts, "\n")
}

// listHeight returns the available line count for the agent list.
func (c CsuiteModel) listHeight() int {
	h := c.height
	h -= 8 // header, pipeline summary, timestamp, help, spacers
	if h < 5 {
		h = 5
	}
	return h
}

// adjustScroll clamps scrollOffset so the cursor stays visible.
func (c *CsuiteModel) adjustScroll() {
	if c.snapshot == nil {
		c.scrollOffset = 0
		return
	}
	count := len(c.snapshot.AgentSummaries)
	listHeight := c.listHeight() - 2 // subtract header lines
	if listHeight <= 0 || count <= listHeight {
		c.scrollOffset = 0
		return
	}
	if c.cursor < c.scrollOffset {
		c.scrollOffset = c.cursor
	}
	if c.cursor >= c.scrollOffset+listHeight {
		c.scrollOffset = c.cursor - listHeight + 1
	}
	maxScroll := count - listHeight
	if c.scrollOffset > maxScroll {
		c.scrollOffset = maxScroll
	}
	if c.scrollOffset < 0 {
		c.scrollOffset = 0
	}
}

// agentMonStatusBadge returns a colored status badge for an agent monitoring status.
func agentMonStatusBadge(status csuite.AgentMonStatus) string {
	var color lipgloss.Color
	switch status {
	case csuite.AgentMonOnline:
		color = colorSuccess
	case csuite.AgentMonStale:
		color = colorWarning
	case csuite.AgentMonOffline:
		color = colorSecondary
	default:
		color = colorSecondary
	}
	return lipgloss.NewStyle().Foreground(color).Render(string(status))
}

// heartbeatAge returns a human-readable string for how long ago the heartbeat was.
func heartbeatAge(t *time.Time) string {
	if t == nil {
		return "never"
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
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	}
}
