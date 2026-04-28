package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/godinj/drem-orchestrator/internal/bridgeclient"
)

func (m *Model) renderView() string {
	if m.width == 0 {
		return "initializing..."
	}

	var b strings.Builder

	// Header line.
	b.WriteString(m.renderHeader())
	b.WriteByte('\n')

	// Tab bar.
	b.WriteString(m.renderTabs())
	b.WriteByte('\n')

	// Horizontal rule.
	b.WriteString(m.renderHRule())
	b.WriteByte('\n')

	// Message viewport.
	b.WriteString(m.viewport.View())
	b.WriteByte('\n')

	// Horizontal rule.
	b.WriteString(m.renderHRule())
	b.WriteByte('\n')

	// Quick actions + connection status.
	b.WriteString(m.renderQuickActions())
	b.WriteByte('\n')

	// Input.
	b.WriteString(m.input.View())

	return b.String()
}

func (m *Model) renderHeader() string {
	title := styleHeader.Render("C-Suite Chat")
	if m.mode == modeInboxQueue {
		title = styleHeader.Render(fmt.Sprintf("C-Suite Chat / Inbox: %s", m.inboxQueueAgent))
	} else if m.mode == modePersonaControl {
		title = styleHeader.Render("C-Suite Chat / Control")
	}

	var status string
	switch m.connState {
	case connConnected:
		status = styleConnected.Render("WS:OK")
	case connConnecting:
		status = styleConnecting.Render("WS:..")
	case connDisconnected:
		status = styleDisconnected.Render("WS:--")
	}

	gap := m.width - lipgloss.Width(title) - lipgloss.Width(status)
	if gap < 1 {
		gap = 1
	}

	return title + strings.Repeat(" ", gap) + status
}

func (m *Model) renderTabs() string {
	if len(m.agents) == 0 {
		return styleTabInactive.Render("(no agents)")
	}

	var tabs []string
	for i, agent := range m.agents {
		label := agent.Name
		if agent.AckCount > 0 {
			label += styleTimestamp.Render(fmt.Sprintf(" ack:%d", agent.AckCount))
		}
		if count := m.unread[agent.Name]; count > 0 {
			label += styleTabBadge.Render(fmt.Sprintf("(%d)", count))
		}
		if i == m.activeTab {
			tabs = append(tabs, styleTabActive.Render(label))
		} else {
			tabs = append(tabs, styleTabInactive.Render(label))
		}
	}
	return strings.Join(tabs, " ")
}

func (m *Model) renderHRule() string {
	return styleHRule.Render(strings.Repeat("─", m.width))
}

func (m *Model) renderQuickActions() string {
	if m.mode == modeInboxQueue {
		return m.renderInboxQueueActions()
	}
	if m.mode == modePersonaControl {
		return m.renderPersonaControlActions()
	}

	var parts []string
	for _, qa := range m.quickActions {
		label := fmt.Sprintf("%s:%s", qa.key.Help().Key, qa.label)
		parts = append(parts, styleQuickAction.Render(label))
	}

	line := strings.Join(parts, " ")

	if m.err != nil {
		errStr := lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf(" err: %s", truncate(m.err.Error(), 40)))
		line += errStr
	}

	return line
}

func (m *Model) renderPersonaControlActions() string {
	parts := []string{
		styleQuickAction.Render("r:refresh"),
		styleQuickAction.Render("↑/↓:select"),
		styleQuickAction.Render("s:stop"),
		styleQuickAction.Render("S:start"),
		styleQuickAction.Render("R:recreate"),
		styleQuickAction.Render("A:all"),
		styleQuickAction.Render("esc:chat"),
	}
	line := strings.Join(parts, " ")
	if m.controlPendingAction != "" {
		line += lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf(" press %s again to confirm", m.controlPendingActionKey()))
	}
	if m.err != nil {
		line += lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf(" err: %s", truncate(m.err.Error(), 40)))
	}
	return line
}

func (m *Model) controlPendingActionKey() string {
	switch m.controlPendingAction {
	case "start":
		return "S"
	case "recreate":
		return "R"
	default:
		return "s"
	}
}

func (m *Model) renderInboxQueueActions() string {
	parts := []string{
		styleQuickAction.Render("r:refresh"),
		styleQuickAction.Render("↑/↓:select"),
		styleQuickAction.Render("a:archive"),
		styleQuickAction.Render("x:ignore"),
		styleQuickAction.Render("esc:chat"),
	}
	line := strings.Join(parts, " ")
	if m.inboxPendingAction != "" {
		line += lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf(" press %s again to confirm", m.inboxPendingActionKey()))
	}
	if m.err != nil {
		line += lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf(" err: %s", truncate(m.err.Error(), 40)))
	}
	return line
}

func (m *Model) inboxPendingActionKey() string {
	if m.inboxPendingAction == "ignore" {
		return "x"
	}
	return "a"
}

// rebuildViewport reconstructs the viewport content from the current agent's
// messages.
func (m *Model) rebuildViewport() {
	if m.mode == modeInboxQueue {
		m.rebuildInboxQueueViewport()
		return
	}
	if m.mode == modePersonaControl {
		m.rebuildPersonaControlViewport()
		return
	}

	name := m.activeAgentName()
	msgs := m.messages[name]
	if len(msgs) == 0 {
		m.viewport.SetContent(styleTimestamp.Render("  (no messages yet)"))
		return
	}

	var b strings.Builder
	for _, msg := range msgs {
		b.WriteString(renderMessage(msg, m.width))
		b.WriteByte('\n')
	}

	m.viewport.SetContent(b.String())
}

func (m *Model) rebuildInboxQueueViewport() {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s pending: %d\n\n", m.inboxQueueAgent, len(m.inboxQueue))
	if len(m.inboxQueue) == 0 {
		b.WriteString(styleTimestamp.Render("  (no pending inbox items)"))
		m.viewport.SetContent(b.String())
		return
	}

	for i, item := range m.inboxQueue {
		cursor := "  "
		if i == m.inboxQueueCursor {
			cursor = "> "
		}
		createdAt := item.CreatedAt
		if createdAt == "" {
			createdAt = item.UpdatedAt
		}
		from := item.FromAgent
		if from == "" {
			from = "unknown"
		}
		subject := item.Subject
		if subject == "" {
			subject = "(no subject)"
		}

		line := fmt.Sprintf("%s%s  from:%s  subject:%s  file:%s  id:%s",
			cursor,
			styleTimestamp.Render(formatTime(createdAt)),
			from,
			truncate(subject, 40),
			truncate(item.Filename, 36),
			truncate(item.ID, 18),
		)
		if i == m.inboxQueueCursor {
			line = styleTabActive.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')

		preview := strings.Join(strings.Fields(item.Body), " ")
		if preview == "" {
			preview = "(empty body)"
		}
		b.WriteString("    ")
		b.WriteString(truncate(preview, max(m.width-6, 20)))
		b.WriteByte('\n')
		if m.inboxPendingAction != "" && m.inboxPendingItemID == item.ID {
			b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf("    press %s again to %s this item", m.inboxPendingActionKey(), m.inboxPendingAction)))
			b.WriteByte('\n')
		}
	}

	m.viewport.SetContent(b.String())
}

func (m *Model) rebuildPersonaControlViewport() {
	var b strings.Builder
	status := "available"
	if !m.personaControlReady {
		status = "unavailable"
	}
	fmt.Fprintf(&b, "  persona container control: %s", status)
	if m.personaControlReason != "" {
		fmt.Fprintf(&b, " (%s)", m.personaControlReason)
	}
	b.WriteString("\n\n")

	if len(m.personaContainers) == 0 {
		b.WriteString(styleTimestamp.Render("  (no persona containers returned)"))
		m.viewport.SetContent(b.String())
		return
	}

	for i, item := range m.personaContainers {
		cursor := "  "
		if i == m.personaControlCursor {
			cursor = "> "
		}
		service := item.Service
		if service == "" {
			service = "unknown"
		}
		status := item.Status
		if status == "" {
			status = "unknown"
		}

		line := fmt.Sprintf("%s%-6s  status:%-10s service:%s", cursor, item.Target, status, service)
		if item.Target == "all" {
			line += "  target:all-personas"
		}
		if i == m.personaControlCursor {
			line = styleTabActive.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')

		if item.Target != "all" && (status == "running" || status == "restarting" || status == "unknown") {
			b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Render("    stop/recreate may kill an in-flight turn"))
			b.WriteByte('\n')
		}
		if m.controlPendingAction != "" && m.controlPendingTarget == item.Target {
			b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf("    press %s again to %s %s", m.controlPendingActionKey(), m.controlPendingAction, item.Target)))
			b.WriteByte('\n')
		}
	}

	m.viewport.SetContent(b.String())
}

func renderMessage(msg bridgeclient.Message, width int) string {
	ts := formatTime(msg.CreatedAt)

	isOperator := msg.FromAgent == "operator"

	var nameStyle, bodyStyle lipgloss.Style
	var name string
	if isOperator {
		nameStyle = styleOperatorName
		bodyStyle = styleOperatorBody
		name = "you"
	} else {
		nameStyle = styleAgentName
		bodyStyle = styleAgentBody
		name = msg.FromAgent
	}

	// Format: "  name  HH:MM  body"
	prefix := fmt.Sprintf("  %s  %s  ",
		nameStyle.Render(fmt.Sprintf("%6s", name)),
		styleTimestamp.Render(ts),
	)

	prefixWidth := lipgloss.Width(prefix)
	bodyWidth := width - prefixWidth
	if bodyWidth < 20 {
		bodyWidth = 20
	}

	body := msg.Body
	lines := wrapText(body, bodyWidth)

	var result strings.Builder
	for i, line := range lines {
		if i == 0 {
			result.WriteString(prefix)
		} else {
			result.WriteString(strings.Repeat(" ", prefixWidth))
		}
		result.WriteString(bodyStyle.Render(line))
		if i < len(lines)-1 {
			result.WriteByte('\n')
		}
	}

	return result.String()
}

func formatTime(isoTime string) string {
	t, err := time.Parse(time.RFC3339Nano, isoTime)
	if err != nil {
		return "??:??"
	}
	return t.Local().Format("15:04")
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}

		current := words[0]
		for _, word := range words[1:] {
			if len(current)+1+len(word) > width {
				lines = append(lines, current)
				current = word
			} else {
				current += " " + word
			}
		}
		lines = append(lines, current)
	}
	return lines
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
