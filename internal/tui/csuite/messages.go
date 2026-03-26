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
	store             *csuite.Store
	agentName         string
	messages          []csuite.CsuiteInboxMessage
	cursor            int
	scrollOffset      int
	width             int
	height            int
	showArchived      bool
	selectedMessageID *uuid.UUID
	composeTriggered  bool
}

// NewMessageListModel creates a new MessageListModel for the given agent.
func NewMessageListModel(store *csuite.Store, agentName string) MessageListModel {
	m := MessageListModel{
		store:        store,
		agentName:    agentName,
		showArchived: false,
	}
	m.loadMessages()
	return m
}

// loadMessages fetches messages from the store for the current agent.
func (m *MessageListModel) loadMessages() {
	msgs, err := m.store.GetMessagesByAgent(m.agentName)
	if err != nil {
		return
	}

	// Filter archived messages based on showArchived flag
	var filtered []csuite.CsuiteInboxMessage
	for _, msg := range msgs {
		if !msg.Archived || m.showArchived {
			filtered = append(filtered, msg)
		}
	}
	m.messages = filtered
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
				if m.cursor < len(m.messages)-1 {
					m.cursor++
				}
				m.adjustScroll()
			case 'k':
				// Move cursor up
				if m.cursor > 0 {
					m.cursor--
				}
				m.adjustScroll()
			case 'a':
				// Toggle archive visibility
				m.showArchived = !m.showArchived
				m.loadMessages()
				m.cursor = 0
				m.scrollOffset = 0
			case 'c':
				// Trigger compose
				m.composeTriggered = true
				return m, quitCmd()
			case 'q':
				// Quit
				return m, quitCmd()
			}
		case msg.Type == tea.KeyEnter:
			// Select current message
			if m.cursor < len(m.messages) {
				msgID := m.messages[m.cursor].ID
				m.selectedMessageID = &msgID
			}
		case msg.Type == tea.KeyEsc:
			// Return to parent view
			return m, quitCmd()
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
	if len(m.messages) == 0 {
		sections = append(sections, "  No messages")
		sections = append(sections, "")
		sections = append(sections, "[c] compose  [a] toggle archive  [q] quit")
		return strings.Join(sections, "\n")
	}

	// Message list
	for i, msg := range m.messages {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%-20s %s", prefix, msg.FromAgent, msg.Subject)
		sections = append(sections, line)
	}

	// Help bar
	sections = append(sections, "")
	sections = append(sections, "[j/k] navigate  [enter] select  [c] compose  [a] toggle archive  [q] quit")

	return strings.Join(sections, "\n")
}

// adjustScroll keeps the cursor visible in the viewport.
func (m *MessageListModel) adjustScroll() {
	listHeight := m.listHeight()
	if len(m.messages) <= listHeight {
		m.scrollOffset = 0
		return
	}
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+listHeight {
		m.scrollOffset = m.cursor - listHeight + 1
	}
}

// listHeight returns the available height for the message list.
func (m MessageListModel) listHeight() int {
	h := m.height
	h -= 5 // header, help, spacers
	if h < 3 {
		h = 3
	}
	return h
}

// MessageDetailModel displays the full content of a single message.
// It supports Esc to go back and 'r' for quick reply.
type MessageDetailModel struct {
	store               *csuite.Store
	agentName           string
	messageID           uuid.UUID
	message             *csuite.CsuiteInboxMessage
	width               int
	height              int
	backTriggered       bool
	quickReplyTriggered bool
	replyTo             string
	replySubject        string
}

// NewMessageDetailModel creates a new MessageDetailModel for the given message.
func NewMessageDetailModel(store *csuite.Store, messageID uuid.UUID, agentName string) MessageDetailModel {
	m := MessageDetailModel{
		store:     store,
		messageID: messageID,
		agentName: agentName,
	}
	m.loadMessage()
	return m
}

// loadMessage fetches the message from the store by querying all messages
// for the agent and filtering by ID.
func (m *MessageDetailModel) loadMessage() {
	msgs, err := m.store.GetMessagesByAgent(m.agentName)
	if err != nil {
		return
	}
	// Find the message with matching ID
	for i := range msgs {
		if msgs[i].ID == m.messageID {
			m.message = &msgs[i]
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
			m.backTriggered = true
			return m, quitCmd()
		case tea.KeyRunes:
			if len(msg.Runes) > 0 {
				switch msg.Runes[0] {
				case 'r':
					// Quick reply
					if m.message != nil {
						m.quickReplyTriggered = true
						m.replyTo = m.message.FromAgent
						m.replySubject = "Re: " + m.message.Subject
					}
					return m, quitCmd()
				case 'q':
					// Quit
					return m, quitCmd()
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

	if m.message == nil {
		sections = append(sections, "  Loading message...")
		return strings.Join(sections, "\n")
	}

	// Frontmatter
	sections = append(sections, "")
	sections = append(sections, fmt.Sprintf("From: %s", m.message.FromAgent))
	sections = append(sections, fmt.Sprintf("To: %s", m.message.ToAgent))
	sections = append(sections, fmt.Sprintf("Subject: %s", m.message.Subject))
	sections = append(sections, fmt.Sprintf("Priority: %s", m.message.Priority))
	sections = append(sections, fmt.Sprintf("Type: %s", m.message.Type))
	sections = append(sections, fmt.Sprintf("Date: %s", m.message.CreatedAt.Format("2006-01-02 15:04:05")))

	// Body
	sections = append(sections, "")
	sections = append(sections, "---")
	sections = append(sections, "")
	sections = append(sections, m.message.Body)

	// Help bar
	sections = append(sections, "")
	sections = append(sections, "---")
	sections = append(sections, "[esc] back  [r] quick reply  [q] quit")

	return strings.Join(sections, "\n")
}

// quitCmd returns a command that quits the current model.
func quitCmd() tea.Cmd {
	return tea.Quit
}
