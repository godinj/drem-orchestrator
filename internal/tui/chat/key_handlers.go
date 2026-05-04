package chat

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/godinj/drem-orchestrator/internal/bridgeclient"
)

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Quit) {
		if m.ws != nil {
			m.ws.Close() //nolint:errcheck
		}
		return m, tea.Quit
	}

	if m.mode == modeInboxQueue {
		return m.handleInboxQueueKey(msg)
	}
	if m.mode == modePersonaControl {
		return m.handlePersonaControlKey(msg)
	}
	if m.mode == modeModelSelector {
		return m.handleModelSelectorKey(msg)
	}

	if isOpenInboxKey(msg) {
		return m.openInboxQueue()
	}
	if isOpenControlKey(msg) {
		return m.openPersonaControl()
	}
	if isOpenModelKey(msg) {
		return m.openModelSelector()
	}

	if key.Matches(msg, keys.NextTab) {
		return m.switchTab((m.activeTab + 1) % max(len(m.agents), 1))
	}
	if key.Matches(msg, keys.PrevTab) {
		prev := m.activeTab - 1
		if prev < 0 {
			prev = max(len(m.agents)-1, 0)
		}
		return m, m.switchTabCmd(prev)
	}

	// Number keys for direct tab selection.
	for i, binding := range []key.Binding{keys.Tab1, keys.Tab2, keys.Tab3, keys.Tab4} {
		if key.Matches(msg, binding) && i < len(m.agents) {
			return m.switchTab(i)
		}
	}

	// Quick actions.
	for _, qa := range m.quickActions {
		if key.Matches(msg, qa.key) {
			return m, m.sendQuickAction(qa.payload)
		}
	}

	if key.Matches(msg, keys.Send) {
		text := strings.TrimSpace(m.input.Value())
		switch text {
		case "/inbox":
			m.input.SetValue("")
			return m.openInboxQueue()
		case "/control":
			m.input.SetValue("")
			return m.openPersonaControl()
		case "/model":
			m.input.SetValue("")
			return m.openModelSelector()
		}
		if text != "" && len(m.agents) > 0 {
			m.input.SetValue("")
			return m, sendMessage(m.client, bridgeclient.SendRequest{
				FromAgent: "operator",
				ToAgent:   m.activeAgentName(),
				Subject:   "chat",
				Body:      text,
				Priority:  "normal",
				Type:      "request",
			})
		}
		return m, nil
	}

	// Scroll — check if we need to load more.
	if key.Matches(msg, keys.ScrollUp) {
		m.viewport.HalfViewUp()
		if m.viewport.AtTop() && m.hasMore[m.activeAgentName()] {
			name := m.activeAgentName()
			return m, fetchMessages(m.client, name, messagesPerPage, m.oldestID[name], true)
		}
		return m, nil
	}
	if key.Matches(msg, keys.ScrollDown) {
		m.viewport.HalfViewDown()
		return m, nil
	}
	if key.Matches(msg, keys.LineUp) {
		m.viewport.LineUp(1)
		if m.viewport.AtTop() && m.hasMore[m.activeAgentName()] {
			name := m.activeAgentName()
			return m, fetchMessages(m.client, name, messagesPerPage, m.oldestID[name], true)
		}
		return m, nil
	}
	if key.Matches(msg, keys.LineDown) {
		m.viewport.LineDown(1)
		return m, nil
	}

	// Pass to text input.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handleModelSelectorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Cancel) {
		m.mode = modeChat
		m.modelSetting = false
		m.rebuildViewport()
		m.viewport.GotoBottom()
		return m, nil
	}
	if key.Matches(msg, keys.Refresh) {
		return m, fetchPersonaModels(m.client)
	}
	if key.Matches(msg, keys.LineUp) {
		m.modelSelectorIndex--
		if m.modelSelectorIndex < 0 {
			m.modelSelectorIndex = len(personaModelOptions) - 1
		}
		m.rebuildViewport()
		return m, nil
	}
	if key.Matches(msg, keys.LineDown) {
		m.modelSelectorIndex = (m.modelSelectorIndex + 1) % len(personaModelOptions)
		m.rebuildViewport()
		return m, nil
	}
	if msg.String() == "1" || msg.String() == "2" {
		m.modelSelectorIndex = int(msg.String()[0] - '1')
		return m.chooseActivePersonaModel()
	}
	if key.Matches(msg, keys.Send) {
		return m.chooseActivePersonaModel()
	}
	return m, nil
}

func (m Model) handleInboxQueueKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Cancel) {
		m.mode = modeChat
		m.clearInboxPendingAction()
		m.rebuildViewport()
		m.viewport.GotoBottom()
		return m, nil
	}
	if key.Matches(msg, keys.Refresh) {
		m.clearInboxPendingAction()
		return m, fetchInboxQueue(m.client, m.inboxQueueAgent, inboxQueueLimit)
	}
	if key.Matches(msg, keys.LineUp) {
		if m.inboxQueueCursor > 0 {
			m.inboxQueueCursor--
			m.clearInboxPendingAction()
			m.rebuildViewport()
		}
		return m, nil
	}
	if key.Matches(msg, keys.LineDown) {
		if m.inboxQueueCursor < len(m.inboxQueue)-1 {
			m.inboxQueueCursor++
			m.clearInboxPendingAction()
			m.rebuildViewport()
		}
		return m, nil
	}
	if key.Matches(msg, keys.Archive) {
		return m.confirmOrRunInboxAction("archive")
	}
	if key.Matches(msg, keys.Ignore) {
		return m.confirmOrRunInboxAction("ignore")
	}
	return m, nil
}

func (m Model) handlePersonaControlKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Cancel) {
		m.mode = modeChat
		m.clearControlPendingAction()
		m.rebuildViewport()
		m.viewport.GotoBottom()
		return m, nil
	}
	if key.Matches(msg, keys.Refresh) {
		m.clearControlPendingAction()
		return m, fetchPersonaContainers(m.client)
	}
	if key.Matches(msg, keys.LineUp) {
		if m.personaControlCursor > 0 {
			m.personaControlCursor--
			m.clearControlPendingAction()
			m.rebuildViewport()
		}
		return m, nil
	}
	if key.Matches(msg, keys.LineDown) {
		if m.personaControlCursor < len(m.personaContainers)-1 {
			m.personaControlCursor++
			m.clearControlPendingAction()
			m.rebuildViewport()
		}
		return m, nil
	}
	if key.Matches(msg, keys.SelectAllControl) {
		for i, item := range m.personaContainers {
			if item.Target == "all" {
				m.personaControlCursor = i
				m.clearControlPendingAction()
				m.rebuildViewport()
				break
			}
		}
		return m, nil
	}
	if key.Matches(msg, keys.StopPersona) {
		return m.confirmOrRunControlAction("stop")
	}
	if key.Matches(msg, keys.StartPersona) {
		return m.confirmOrRunControlAction("start")
	}
	if key.Matches(msg, keys.RecreatePersona) {
		return m.confirmOrRunControlAction("recreate")
	}
	return m, nil
}
