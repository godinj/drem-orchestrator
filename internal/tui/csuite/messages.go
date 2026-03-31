package csuite

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// MessageListModel displays a scrollable list of inbox messages for the current agent.
// It supports j/k navigation, Enter to select, 'a' to toggle archive visibility, and 'c' to compose.
type MessageListModel struct {
	Store             *csuite.Store
	AgentName         string
	Messages          []csuite.CsuiteInboxMessage
	Cursor            int
	ScrollOffset      int
	Width             int
	Height            int
	ShowArchived      bool
	SelectedMessageID *uuid.UUID
	ComposeTriggered  bool
}

// NewMessageListModel creates a new MessageListModel for the given agent.
func NewMessageListModel(store *csuite.Store, agentName string) MessageListModel {
	m := MessageListModel{
		Store:        store,
		AgentName:    agentName,
		ShowArchived: false,
	}
	m.LoadMessages()
	return m
}

// LoadMessages fetches messages from the store for the current agent.
func (m *MessageListModel) LoadMessages() {
	msgs, err := m.Store.GetMessagesByAgent(m.AgentName)
	if err != nil {
		return
	}

	// Filter archived messages based on ShowArchived flag
	var filtered []csuite.CsuiteInboxMessage
	for _, msg := range msgs {
		if !msg.Archived || m.ShowArchived {
			filtered = append(filtered, msg)
		}
	}
	m.Messages = filtered
}

// Init returns initial commands for the model.
func (m MessageListModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input for navigation and actions.
func (m MessageListModel) Update(msg tea.Msg) (MessageListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case msg.Type == tea.KeyRunes && len(msg.Runes) > 0:
			switch msg.Runes[0] {
			case 'j':
				// Move cursor down
				if m.Cursor < len(m.Messages)-1 {
					m.Cursor++
				}
				m.AdjustScroll()
			case 'k':
				// Move cursor up
				if m.Cursor > 0 {
					m.Cursor--
				}
				m.AdjustScroll()
			case 'a':
				// Toggle archive visibility
				m.ShowArchived = !m.ShowArchived
				m.LoadMessages()
				m.Cursor = 0
				m.ScrollOffset = 0
			case 'c':
				// Trigger compose
				m.ComposeTriggered = true
				return m, nil
			case 'q':
				// Back to parent
				return m, nil
			}
		case msg.Type == tea.KeyEnter:
			// Select current message
			if m.Cursor < len(m.Messages) {
				msgID := m.Messages[m.Cursor].ID
				m.SelectedMessageID = &msgID
			}
		case msg.Type == tea.KeyEsc:
			// Return to parent view
			return m, nil
		}
	}
	return m, nil
}

// View renders the message list.
func (m MessageListModel) View() string {
	var sections []string

	// Title
	sections = append(sections, "Messages")

	// Empty state
	if len(m.Messages) == 0 {
		sections = append(sections, "  No messages")
		sections = append(sections, "")
		sections = append(sections, "[c] compose  [a] toggle archive  [esc] back")
		return strings.Join(sections, "\n")
	}

	// Message list
	for i, msg := range m.Messages {
		prefix := "  "
		if i == m.Cursor {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%-20s %s", prefix, msg.FromAgent, msg.Subject)
		sections = append(sections, line)
	}

	// Help bar
	sections = append(sections, "")
	sections = append(sections, "[j/k] navigate  [enter] select  [c] compose  [a] toggle archive  [esc] back")

	return strings.Join(sections, "\n")
}

// AdjustScroll keeps the cursor visible in the viewport.
func (m *MessageListModel) AdjustScroll() {
	listHeight := m.listHeight()
	if len(m.Messages) <= listHeight {
		m.ScrollOffset = 0
		return
	}
	if m.Cursor < m.ScrollOffset {
		m.ScrollOffset = m.Cursor
	}
	if m.Cursor >= m.ScrollOffset+listHeight {
		m.ScrollOffset = m.Cursor - listHeight + 1
	}
}

// listHeight returns the available height for the message list.
func (m MessageListModel) listHeight() int {
	h := m.Height
	h -= 5 // header, help, spacers
	if h < 3 {
		h = 3
	}
	return h
}

// MessageDetailModel displays the full content of a single message.
// It supports Esc to go back and 'r' for quick reply.
type MessageDetailModel struct {
	Store               *csuite.Store
	AgentName           string
	MessageID           uuid.UUID
	Message             *csuite.CsuiteInboxMessage
	Width               int
	Height              int
	BackTriggered       bool
	QuickReplyTriggered bool
	ReplyTo             string
	ReplySubject        string
}

// NewMessageDetailModel creates a new MessageDetailModel for the given message.
func NewMessageDetailModel(store *csuite.Store, messageID uuid.UUID, agentName string) MessageDetailModel {
	m := MessageDetailModel{
		Store:     store,
		MessageID: messageID,
		AgentName: agentName,
	}
	m.LoadMessage()
	return m
}

// LoadMessage fetches the message from the store by querying all messages
// for the agent and filtering by ID.
func (m *MessageDetailModel) LoadMessage() {
	msgs, err := m.Store.GetMessagesByAgent(m.AgentName)
	if err != nil {
		return
	}
	// Find the message with matching ID
	for i := range msgs {
		if msgs[i].ID == m.MessageID {
			m.Message = &msgs[i]
			return
		}
	}
}

// Init returns initial commands for the model.
func (m MessageDetailModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input for navigation and actions.
func (m MessageDetailModel) Update(msg tea.Msg) (MessageDetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			// Go back to list
			m.BackTriggered = true
			return m, nil
		case tea.KeyRunes:
			if len(msg.Runes) > 0 {
				switch msg.Runes[0] {
				case 'r':
					// Quick reply
					if m.Message != nil {
						m.QuickReplyTriggered = true
						m.ReplyTo = m.Message.FromAgent
						m.ReplySubject = "Re: " + m.Message.Subject
					}
					return m, nil
				case 'q':
					// Back to parent
					return m, nil
				}
			}
		}
	}
	return m, nil
}

// View renders the message detail.
func (m MessageDetailModel) View() string {
	var sections []string

	sections = append(sections, "Message Detail")

	if m.Message == nil {
		sections = append(sections, "  Loading message...")
		return strings.Join(sections, "\n")
	}

	// Frontmatter
	sections = append(sections, "")
	sections = append(sections, fmt.Sprintf("From: %s", m.Message.FromAgent))
	sections = append(sections, fmt.Sprintf("To: %s", m.Message.ToAgent))
	sections = append(sections, fmt.Sprintf("Subject: %s", m.Message.Subject))
	sections = append(sections, fmt.Sprintf("Priority: %s", m.Message.Priority))
	sections = append(sections, fmt.Sprintf("Type: %s", m.Message.Type))
	sections = append(sections, fmt.Sprintf("Date: %s", m.Message.CreatedAt.Format("2006-01-02 15:04:05")))

	// Body
	sections = append(sections, "")
	sections = append(sections, "---")
	sections = append(sections, "")
	sections = append(sections, m.Message.Body)

	// Help bar
	sections = append(sections, "")
	sections = append(sections, "---")
	sections = append(sections, "[esc] back  [r] quick reply")

	return strings.Join(sections, "\n")
}
