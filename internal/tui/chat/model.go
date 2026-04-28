package chat

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/godinj/drem-orchestrator/internal/bridgeclient"
)

const (
	messagesPerPage    = 50
	agentRefreshRate   = 5 * time.Second
	messageRefreshRate = 5 * time.Second
	maxBackoff         = 30 * time.Second
)

type connState int

const (
	connConnecting connState = iota
	connConnected
	connDisconnected
)

// quickAction is a preconfigured message shortcut.
type quickAction struct {
	label   string
	payload string
	key     key.Binding
}

var defaultQuickActions = []quickAction{
	{label: "status", payload: "status", key: keys.Quick1},
	{label: "check", payload: "check", key: keys.Quick2},
	{label: "yes", payload: "yes", key: keys.Quick3},
}

// Model is the root bubbletea model for the chat TUI.
type Model struct {
	client *bridgeclient.Client
	wsURL  string

	// Connection state.
	ws               *bridgeclient.WSConn
	connState        connState
	reconnectBackoff time.Duration

	// Agent tabs.
	agents    []bridgeclient.Agent
	activeTab int
	unread    map[string]int

	// Messages per agent (oldest first for display).
	messages map[string][]bridgeclient.Message
	oldestID map[string]string
	hasMore  map[string]bool
	loaded   map[string]bool

	// UI components.
	viewport viewport.Model
	input    textinput.Model

	// Quick actions.
	quickActions []quickAction

	// Layout.
	width  int
	height int
	err    error
}

// New creates a new chat Model.
func New(client *bridgeclient.Client, wsURL string) Model {
	ti := textinput.New()
	ti.Placeholder = "type a message..."
	ti.Prompt = "> "
	ti.Focus()
	ti.CharLimit = 2000

	vp := viewport.New(80, 20)

	return Model{
		client:       client,
		wsURL:        wsURL,
		connState:    connConnecting,
		unread:       make(map[string]int),
		messages:     make(map[string][]bridgeclient.Message),
		oldestID:     make(map[string]string),
		hasMore:      make(map[string]bool),
		loaded:       make(map[string]bool),
		viewport:     vp,
		input:        ti,
		quickActions: defaultQuickActions,
	}
}

// Init starts the initial fetch of agents and WebSocket connection.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchAgents(m.client),
		connectWS(m.wsURL),
		tickRefreshAgents(agentRefreshRate),
		tickRefreshMessages(messageRefreshRate),
	)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	// --- Data messages ---

	case agentsLoadedMsg:
		m.agents = msg.agents
		m.err = nil
		for _, a := range m.agents {
			m.unread[a.Name] = a.UnreadCount
		}
		if len(m.agents) > 0 && !m.loaded[m.activeAgentName()] {
			cmds = append(cmds, fetchMessages(m.client, m.activeAgentName(), messagesPerPage, "", false))
		}

	case agentsErrMsg:
		m.err = msg.err

	case messagesLoadedMsg:
		m.handleMessagesLoaded(msg)

	case messagesErrMsg:
		m.err = msg.err

	case messageSentMsg:
		// The WebSocket broadcast will deliver this back to us as a
		// new_message event, so we don't append here to avoid duplicates.

	case messageSendErrMsg:
		m.err = msg.err

	// --- WebSocket messages ---

	case wsConnectedMsg:
		m.ws = msg.ws
		m.connState = connConnected
		m.reconnectBackoff = 0
		m.err = nil
		cmds = append(cmds, listenWS(m.ws))

	case wsEventMsg:
		cmd := m.handleWSEvent(msg.evt)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		// Keep listening.
		if m.ws != nil {
			cmds = append(cmds, listenWS(m.ws))
		}

	case wsDisconnectedMsg:
		m.connState = connDisconnected
		m.ws = nil
		m.reconnectBackoff = nextBackoff(m.reconnectBackoff)
		cmds = append(cmds, reconnectWS(m.wsURL, m.reconnectBackoff))

	// --- Ticks ---

	case switchTabMsg:
		return m.switchTab(msg.idx)

	case tickRefreshAgentsMsg:
		cmds = append(cmds, fetchAgents(m.client))
		cmds = append(cmds, tickRefreshAgents(agentRefreshRate))

	case tickRefreshMessagesMsg:
		if name := m.activeAgentName(); name != "" {
			cmds = append(cmds, fetchMessages(m.client, name, messagesPerPage, "", false))
		}
		cmds = append(cmds, tickRefreshMessages(messageRefreshRate))
	}

	return m, tea.Batch(cmds...)
}

// View renders the UI.
func (m Model) View() string {
	return m.renderView()
}

// --- Key handling ---

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Quit) {
		if m.ws != nil {
			m.ws.Close() //nolint:errcheck
		}
		return m, tea.Quit
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

// --- Helpers ---

func (m *Model) handleMessagesLoaded(msg messagesLoadedMsg) {
	m.loaded[msg.agent] = true
	active := msg.agent == m.activeAgentName()
	wasAtBottom := active && m.viewport.AtBottom()

	// Server returns newest-first; reverse for display (oldest first).
	reversed := make([]bridgeclient.Message, len(msg.msgs))
	for i, m := range msg.msgs {
		reversed[len(msg.msgs)-1-i] = m
	}

	if msg.prepend {
		// Prepend older messages.
		m.messages[msg.agent] = mergeMessages(reversed, m.messages[msg.agent])
	} else {
		m.messages[msg.agent] = mergeMessages(m.messages[msg.agent], reversed)
	}

	// Track pagination cursor.
	if len(m.messages[msg.agent]) > 0 {
		m.oldestID[msg.agent] = m.messages[msg.agent][0].ID
	}
	m.hasMore[msg.agent] = len(msg.msgs) >= messagesPerPage

	// Rebuild viewport if this is the active agent.
	if active {
		m.rebuildViewport()
		if !msg.prepend && (wasAtBottom || len(m.messages[msg.agent]) == len(reversed)) {
			m.viewport.GotoBottom()
		}
	}
}

func (m *Model) handleWSEvent(evt bridgeclient.WSEvent) tea.Cmd {
	if evt.Type != "new_message" {
		return nil
	}
	msg, err := parseWSMessage(evt.Data)
	if err != nil {
		return nil
	}

	// Determine which agent this message belongs to.
	agent := msg.FromAgent
	if agent == "operator" {
		agent = msg.ToAgent
	}

	if messageExists(m.messages[agent], msg) {
		return nil
	}
	m.messages[agent] = mergeMessages(m.messages[agent], []bridgeclient.Message{msg})

	if agent == m.activeAgentName() {
		m.rebuildViewport()
		m.viewport.GotoBottom()
	} else {
		m.unread[agent]++
	}
	return nil
}

func (m Model) activeAgentName() string {
	if m.activeTab >= 0 && m.activeTab < len(m.agents) {
		return m.agents[m.activeTab].Name
	}
	return ""
}

func (m Model) switchTab(idx int) (tea.Model, tea.Cmd) {
	if idx == m.activeTab || idx >= len(m.agents) {
		return m, nil
	}
	m.activeTab = idx
	name := m.activeAgentName()
	m.unread[name] = 0

	m.rebuildViewport()
	m.viewport.GotoBottom()
	return m, fetchMessages(m.client, name, messagesPerPage, "", false)
}

func (m Model) switchTabCmd(idx int) tea.Cmd {
	return func() tea.Msg {
		// This is a no-op msg; the actual switching happens in switchTab.
		// We use this pattern to avoid returning (Model, Cmd) from a non-Update context.
		return switchTabMsg{idx: idx}
	}
}

type switchTabMsg struct{ idx int }

func (m Model) sendQuickAction(payload string) tea.Cmd {
	if len(m.agents) == 0 {
		return nil
	}
	return sendMessage(m.client, bridgeclient.SendRequest{
		FromAgent: "operator",
		ToAgent:   m.activeAgentName(),
		Subject:   "chat",
		Body:      payload,
		Priority:  "normal",
		Type:      "request",
	})
}

func (m *Model) recalcLayout() {
	// Reserve: 1 header, 1 tab bar, 1 hr, 1 quick actions, 1 hr, 1 input, 1 bottom border.
	viewportHeight := m.height - 7
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = viewportHeight
	m.input.Width = m.width - 4 // account for prompt "> "
	m.rebuildViewport()
}

func nextBackoff(current time.Duration) time.Duration {
	if current == 0 {
		return time.Second
	}
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

func mergeMessages(existing, incoming []bridgeclient.Message) []bridgeclient.Message {
	if len(existing) == 0 {
		return append([]bridgeclient.Message(nil), incoming...)
	}
	if len(incoming) == 0 {
		return append([]bridgeclient.Message(nil), existing...)
	}

	byID := make(map[string]bridgeclient.Message, len(existing)+len(incoming))
	for _, msg := range existing {
		byID[messageKey(msg)] = msg
	}
	for _, msg := range incoming {
		byID[messageKey(msg)] = msg
	}

	merged := make([]bridgeclient.Message, 0, len(byID))
	for _, msg := range byID {
		merged = append(merged, msg)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return messageBefore(merged[i], merged[j])
	})
	return merged
}

func messageKey(msg bridgeclient.Message) string {
	if msg.ID != "" {
		return msg.ID
	}
	return msg.CreatedAt + "\x00" + msg.FromAgent + "\x00" + msg.ToAgent + "\x00" + msg.Body
}

func messageBefore(a, b bridgeclient.Message) bool {
	at, aErr := time.Parse(time.RFC3339Nano, a.CreatedAt)
	bt, bErr := time.Parse(time.RFC3339Nano, b.CreatedAt)
	if aErr == nil && bErr == nil && !at.Equal(bt) {
		return at.Before(bt)
	}
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
}

func messageExists(messages []bridgeclient.Message, msg bridgeclient.Message) bool {
	key := messageKey(msg)
	for _, existing := range messages {
		if messageKey(existing) == key {
			return true
		}
	}
	return false
}
