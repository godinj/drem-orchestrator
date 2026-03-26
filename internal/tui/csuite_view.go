package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	csuiteTUI "github.com/godinj/drem-orchestrator/internal/tui/csuite"
)

// csuiteStateSnapshot is a package-local alias so that app.go can reference
// the type without importing the csuite package directly (reducing the tui
// import count).
type csuiteStateSnapshot = csuite.StateSnapshot

// csuiteSnapshotMsg carries a new state snapshot from the csuite poller.
type csuiteSnapshotMsg struct {
	snapshot csuiteStateSnapshot
}

// viewMode represents the current view in CsuiteModel
type viewMode int

const (
	viewDashboard viewMode = iota
	viewList
	viewDetail
	viewCompose
)

// CsuiteModel renders the C-Suite agent dashboard screen and message views.
type CsuiteModel struct {
	snapshot          *csuite.StateSnapshot
	store             *csuite.Store
	cursor            int
	scrollOffset      int
	width             int
	height            int
	currentView       viewMode
	selectedAgentName string
	selectedMessageID *uuid.UUID

	// Child models
	messageList   csuiteTUI.MessageListModel
	messageDetail csuiteTUI.MessageDetailModel
	composeModel  *csuiteTUI.ComposeModel
}

// NewCsuiteModel creates an empty CsuiteModel.
func NewCsuiteModel() CsuiteModel {
	return CsuiteModel{
		currentView: viewDashboard,
	}
}

// SetStore sets the store for the CsuiteModel to enable message operations.
func (c *CsuiteModel) SetStore(store *csuite.Store) {
	c.store = store
}

// Init returns initial commands for the CsuiteModel.
func (c CsuiteModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input for navigation and view switching.
func (c *CsuiteModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch c.currentView {
		case viewDashboard:
			return c.updateDashboard(msg)
		case viewList:
			return c.updateList(msg)
		case viewDetail:
			return c.updateDetail(msg)
		case viewCompose:
			return c.updateCompose(msg)
		}
	}
	return nil
}

// updateDashboard handles keyboard input for the dashboard view.
func (c *CsuiteModel) updateDashboard(msg tea.KeyMsg) tea.Cmd {
	switch {
	case msg.Type == tea.KeyRunes && len(msg.Runes) > 0:
		switch msg.Runes[0] {
		case 'j':
			// Move cursor down
			if c.snapshot != nil && c.cursor < len(c.snapshot.AgentSummaries)-1 {
				c.cursor++
				c.adjustScroll()
			}
		case 'k':
			// Move cursor up
			if c.cursor > 0 {
				c.cursor--
				c.adjustScroll()
			}
		case 'm':
			// Open message list for selected agent
			if c.snapshot != nil && c.cursor < len(c.snapshot.AgentSummaries) {
				c.selectedAgentName = c.snapshot.AgentSummaries[c.cursor].Name
				if c.store != nil {
					c.messageList = csuiteTUI.NewMessageListModel(c.store, c.selectedAgentName)
					c.messageList.Width = c.width
					c.messageList.Height = c.height
					c.currentView = viewList
				}
			}
		}
	case msg.Type == tea.KeyEnter:
		// Open message list for selected agent
		if c.snapshot != nil && c.cursor < len(c.snapshot.AgentSummaries) {
			c.selectedAgentName = c.snapshot.AgentSummaries[c.cursor].Name
			if c.store != nil {
				c.messageList = csuiteTUI.NewMessageListModel(c.store, c.selectedAgentName)
				c.messageList.Width = c.width
				c.messageList.Height = c.height
				c.currentView = viewList
			}
		}
	}
	return nil
}

// updateList handles keyboard input for the message list view.
func (c *CsuiteModel) updateList(msg tea.KeyMsg) tea.Cmd {
	switch {
	case msg.Type == tea.KeyRunes && len(msg.Runes) > 0:
		switch msg.Runes[0] {
		case 'j':
			// Move cursor down
			if c.messageList.Cursor < len(c.messageList.Messages)-1 {
				c.messageList.Cursor++
				c.messageList.AdjustScroll()
			}
		case 'k':
			// Move cursor up
			if c.messageList.Cursor > 0 {
				c.messageList.Cursor--
				c.messageList.AdjustScroll()
			}
		case 'a':
			// Toggle archive visibility
			c.messageList.ShowArchived = !c.messageList.ShowArchived
			c.messageList.LoadMessages()
			c.messageList.Cursor = 0
			c.messageList.ScrollOffset = 0
		case 'c':
			// Open compose
			c.composeModel = csuiteTUI.NewComposeModel(c.store)
			c.composeModel.Width = c.width
			c.composeModel.Height = c.height
			c.currentView = viewCompose
		}
	case msg.Type == tea.KeyEnter:
		// Open message detail
		if c.messageList.Cursor < len(c.messageList.Messages) {
			msgID := c.messageList.Messages[c.messageList.Cursor].ID
			c.selectedMessageID = &msgID
			c.messageDetail = csuiteTUI.NewMessageDetailModel(c.store, msgID, c.selectedAgentName)
			c.messageDetail.Width = c.width
			c.messageDetail.Height = c.height
			c.currentView = viewDetail
		}
	case msg.Type == tea.KeyEsc:
		// Go back to dashboard
		c.currentView = viewDashboard
		c.selectedAgentName = ""
	}
	return nil
}

// updateDetail handles keyboard input for the message detail view.
func (c *CsuiteModel) updateDetail(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		// Go back to list
		c.currentView = viewList
		c.selectedMessageID = nil
	case tea.KeyRunes:
		if len(msg.Runes) > 0 {
			switch msg.Runes[0] {
			case 'r':
				// Quick reply
				if c.messageDetail.Message != nil {
					c.composeModel = csuiteTUI.NewComposeModel(c.store,
						csuiteTUI.WithQuickReply(c.messageDetail.Message.FromAgent, c.messageDetail.Message.Subject))
					c.composeModel.Width = c.width
					c.composeModel.Height = c.height
					c.currentView = viewCompose
				}
			}
		}
	}
	return nil
}

// updateCompose handles keyboard input for the compose view.
func (c *CsuiteModel) updateCompose(msg tea.KeyMsg) tea.Cmd {
	if c.composeModel == nil {
		return nil
	}

	switch msg.Type {
	case tea.KeyEsc:
		// Cancel and go back to list
		c.composeModel.Cancel()
		c.currentView = viewList
		c.composeModel = nil
	case tea.KeyCtrlS, tea.KeyEnter:
		// Submit the form
		// Determine the "from" agent based on context
		// For now, we'll use a placeholder - in a real app, this would come from the session
		fromAgent := "operator"
		if err := c.composeModel.Submit(fromAgent); err == nil {
			// Reload messages and return to list
			c.messageList.LoadMessages()
			c.currentView = viewList
			c.composeModel = nil
		}
	}
	return nil
}

// View renders the appropriate view based on currentView.
func (c *CsuiteModel) View() string {
	// Update child model sizes
	c.messageList.Width = c.width
	c.messageList.Height = c.height
	c.messageDetail.Width = c.width
	c.messageDetail.Height = c.height
	if c.composeModel != nil {
		c.composeModel.Width = c.width
		c.composeModel.Height = c.height
	}

	switch c.currentView {
	case viewList:
		return c.messageList.View()
	case viewDetail:
		return c.messageDetail.View()
	case viewCompose:
		if c.composeModel != nil {
			return c.composeModel.View()
		}
	}

	// Default: render dashboard
	return c.renderDashboard()
}

// renderDashboard renders the C-Suite dashboard screen.
func (c CsuiteModel) renderDashboard() string {
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
	sections = append(sections, helpStyle.Render("[j/k] navigate  [enter/m] messages  [?] help"))

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

// renderCsuiteScreen renders the full-screen C-Suite dashboard view.
func (m Model) renderCsuiteScreen() string {
	// Update csuite panel sizes.
	innerWidth := m.width - 4 // account for border + padding
	if innerWidth < 10 {
		innerWidth = 10
	}
	m.csuite.width = innerWidth
	m.csuite.height = m.height - 4 // account for border

	content := m.csuite.View()

	// Error line.
	if m.err != nil {
		content += "\n" + lipglossRender(colorDanger, fmt.Sprintf("Error: %v", m.err))
	}

	return panelStyle.
		Width(m.width - 2).
		Height(m.height - 2).
		BorderForeground(colorPrimary).
		Render(content)
}

// listenForCsuiteSnapshot returns a Cmd that blocks on the C-Suite snapshot
// channel and wraps each StateSnapshot as a csuiteSnapshotMsg.
func listenForCsuiteSnapshot(snaps <-chan csuite.StateSnapshot) tea.Cmd {
	if snaps == nil {
		return nil
	}
	return func() tea.Msg {
		snap, ok := <-snaps
		if !ok {
			return nil
		}
		return csuiteSnapshotMsg{snapshot: snap}
	}
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
