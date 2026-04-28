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

// rebuildViewport reconstructs the viewport content from the current agent's
// messages.
func (m *Model) rebuildViewport() {
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
