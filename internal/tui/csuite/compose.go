package csuite

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// ComposeModel represents a form for composing messages in the C-Suite TUI.
// It manages form state, navigation, and submission through the csuite.Store.
type ComposeModel struct {
	store          *csuite.Store
	to             string
	subject        string
	body           string
	priority       csuite.InboxPriority
	messageType    csuite.InboxMessageType
	focusedField   int // 0=to, 1=subject, 2=priority, 3=type, 4=body
	toInitial      string
	subjectInitial string
	Width          int
	Height         int
}

// ComposeOption represents an optional configuration for NewComposeModel.
type ComposeOption func(*ComposeModel)

// WithTo pre-fills the To field.
func WithTo(to string) ComposeOption {
	return func(m *ComposeModel) {
		m.to = to
		m.toInitial = to
	}
}

// WithSubjectPrefix pre-fills the Subject field with a prefix.
func WithSubjectPrefix(prefix string) ComposeOption {
	return func(m *ComposeModel) {
		m.subject = prefix
		m.subjectInitial = prefix
	}
}

// WithQuickReply pre-fills To and adds "Re: " prefix to Subject.
func WithQuickReply(from, originalSubject string) ComposeOption {
	return func(m *ComposeModel) {
		m.to = from
		m.toInitial = from
		m.subject = "Re: " + originalSubject
		m.subjectInitial = m.subject
	}
}

// NewComposeModel creates a new ComposeModel with the given store.
func NewComposeModel(store *csuite.Store, opts ...ComposeOption) *ComposeModel {
	m := &ComposeModel{
		store:        store,
		priority:     csuite.PriorityNormal,
		messageType:  csuite.MessageTypeStatus,
		focusedField: 0, // Start focused on "to" field
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ---------------------------------------------------------------------------
// Field accessors
// ---------------------------------------------------------------------------

// To returns the recipient field.
func (m *ComposeModel) To() string {
	return m.to
}

// Subject returns the subject field.
func (m *ComposeModel) Subject() string {
	return m.subject
}

// Body returns the body field.
func (m *ComposeModel) Body() string {
	return m.body
}

// Priority returns the priority field.
func (m *ComposeModel) Priority() csuite.InboxPriority {
	return m.priority
}

// MessageType returns the message type field.
func (m *ComposeModel) MessageType() csuite.InboxMessageType {
	return m.messageType
}

// ---------------------------------------------------------------------------
// Field setters
// ---------------------------------------------------------------------------

func (m *ComposeModel) SetTo(to string) {
	m.to = to
}

func (m *ComposeModel) SetSubject(subject string) {
	m.subject = subject
}

func (m *ComposeModel) SetBody(body string) {
	m.body = body
}

func (m *ComposeModel) SetPriority(priority csuite.InboxPriority) {
	m.priority = priority
}

func (m *ComposeModel) SetMessageType(messageType csuite.InboxMessageType) {
	m.messageType = messageType
}

// ---------------------------------------------------------------------------
// Field validation
// ---------------------------------------------------------------------------

// Validate returns true if all required fields are filled.
func (m *ComposeModel) Validate() bool {
	return m.to != "" && m.subject != "" && m.body != ""
}

// ---------------------------------------------------------------------------
// Focus management
// ---------------------------------------------------------------------------

// FocusedField returns the name of the currently focused field.
func (m *ComposeModel) FocusedField() string {
	fields := []string{"to", "subject", "priority", "type", "body"}
	if m.focusedField < 0 || m.focusedField >= len(fields) {
		return fields[0]
	}
	return fields[m.focusedField]
}

// FocusNext moves focus to the next field (wraps around).
func (m *ComposeModel) FocusNext() {
	m.focusedField = (m.focusedField + 1) % 5
}

// FocusPrev moves focus to the previous field (wraps around).
func (m *ComposeModel) FocusPrev() {
	m.focusedField = (m.focusedField - 1 + 5) % 5
}

// ---------------------------------------------------------------------------
// Input handling
// ---------------------------------------------------------------------------

// HandleInput adds input to the currently focused field.
func (m *ComposeModel) HandleInput(input string) {
	switch m.FocusedField() {
	case "to":
		m.to += input
	case "subject":
		m.subject += input
	case "body":
		m.body += input
	case "priority":
		m.CycleNextPriority()
	case "type":
		m.CycleNextType()
	}
}

// HandleBackspace removes the last character from the currently focused field.
func (m *ComposeModel) HandleBackspace() {
	switch m.FocusedField() {
	case "to":
		if len(m.to) > 0 {
			m.to = m.to[:len(m.to)-1]
		}
	case "subject":
		if len(m.subject) > 0 {
			m.subject = m.subject[:len(m.subject)-1]
		}
	case "body":
		if len(m.body) > 0 {
			m.body = m.body[:len(m.body)-1]
		}
	case "priority":
		m.CycleNextPriority()
	case "type":
		m.CycleNextType()
	}
}

// CycleNextPriority moves to the next priority level (wraps around).
func (m *ComposeModel) CycleNextPriority() {
	switch m.priority {
	case csuite.PriorityLow:
		m.priority = csuite.PriorityNormal
	case csuite.PriorityNormal:
		m.priority = csuite.PriorityHigh
	case csuite.PriorityHigh:
		m.priority = csuite.PriorityLow
	default:
		m.priority = csuite.PriorityNormal
	}
}

// CycleNextType moves to the next message type (wraps around).
func (m *ComposeModel) CycleNextType() {
	switch m.messageType {
	case csuite.MessageTypeStatus:
		m.messageType = csuite.MessageTypeRequest
	case csuite.MessageTypeRequest:
		m.messageType = csuite.MessageTypeAlert
	case csuite.MessageTypeAlert:
		m.messageType = csuite.MessageTypeStatus
	default:
		m.messageType = csuite.MessageTypeStatus
	}
}

// ---------------------------------------------------------------------------
// Submission and cancellation
// ---------------------------------------------------------------------------

// Submit validates the form and creates a message in the store.
// fromAgent is the name of the agent sending the message.
func (m *ComposeModel) Submit(fromAgent string) error {
	if !m.Validate() {
		return ErrFormValidation
	}

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: fromAgent,
		ToAgent:   m.to,
		Subject:   m.subject,
		Body:      m.body,
		Priority:  m.priority,
		Type:      m.messageType,
	}

	if err := m.store.CreateMessage(msg); err != nil {
		return err
	}

	// Reset form after successful submission
	m.resetForm()
	return nil
}

// Cancel clears the form and resets focus.
func (m *ComposeModel) Cancel() {
	m.resetForm()
}

// resetForm clears all fields and resets focus.
func (m *ComposeModel) resetForm() {
	m.to = m.toInitial
	m.subject = m.subjectInitial
	m.body = ""
	m.priority = csuite.PriorityNormal
	m.messageType = csuite.MessageTypeStatus
	m.focusedField = 0
}

// ---------------------------------------------------------------------------
// Bubble Tea interface
// ---------------------------------------------------------------------------

// Init initializes the model.
func (m *ComposeModel) Init() tea.Cmd {
	return nil
}

// Update handles key and message inputs.
func (m *ComposeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyTab:
			m.FocusNext()
			return m, nil
		case tea.KeyShiftTab:
			m.FocusPrev()
			return m, nil
		case tea.KeyEsc:
			m.Cancel()
			return m, tea.Quit
		case tea.KeyBackspace, tea.KeyDelete:
			m.HandleBackspace()
			return m, nil
		case tea.KeySpace:
			m.HandleInput(" ")
			return m, nil
		case tea.KeyCtrlS:
			// Submit will be handled by parent model
			return m, nil
		case tea.KeyEnter:
			// In body field, add newline; otherwise move to next field
			if m.FocusedField() == "body" {
				m.HandleInput("\n")
			} else {
				m.FocusNext()
			}
			return m, nil
		case tea.KeyRunes:
			for _, r := range msg.Runes {
				m.HandleInput(string(r))
			}
			return m, nil
		}
	}
	return m, nil
}

// View renders the compose form.
func (m *ComposeModel) View() string {
	return "Compose Form\n" +
		"To: " + m.to + "\n" +
		"Subject: " + m.subject + "\n" +
		"Priority: " + string(m.priority) + "\n" +
		"Type: " + string(m.messageType) + "\n" +
		"Body: " + m.body
}
