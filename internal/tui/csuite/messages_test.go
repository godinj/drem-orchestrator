package csuite

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// MessageListModel View rendering tests
// ---------------------------------------------------------------------------

func TestMessageListView_EmptyList(t *testing.T) {
	store := csuite.NewStore(testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{}))
	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	// Should show title and empty state
	if !strings.Contains(view, "Messages") {
		t.Error("view should contain title with 'Messages'")
	}
	if !strings.Contains(view, "No messages") {
		t.Error("view should show empty state message")
	}
}

func TestMessageListView_RendersSingleMessage(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test Subject",
		Body:      "Test body content",
		Priority:  csuite.PriorityHigh,
		Type:      csuite.MessageTypeAlert,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	// Should show message subject
	if !strings.Contains(view, "Test Subject") {
		t.Error("view should contain message subject")
	}
	// Should show sender
	if !strings.Contains(view, "kyle") {
		t.Error("view should contain sender name")
	}
}

func TestMessageListView_RendersPriorityBadges(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	// Create messages with different priorities
	for _, priority := range []csuite.InboxPriority{csuite.PriorityLow, csuite.PriorityNormal, csuite.PriorityHigh} {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   "test-agent",
			Subject:   "Subject " + string(priority),
			Priority:  priority,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	// Should render all priorities
	if !strings.Contains(view, "Subject low") {
		t.Error("view should render low priority message")
	}
	if !strings.Contains(view, "Subject normal") {
		t.Error("view should render normal priority message")
	}
	if !strings.Contains(view, "Subject high") {
		t.Error("view should render high priority message")
	}
}

func TestMessageListView_ShowsActiveMessages(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	// Create active message
	active := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Active Message",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(active); err != nil {
		t.Fatalf("create active message: %v", err)
	}

	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	// Active message should be visible
	if !strings.Contains(view, "Active Message") {
		t.Error("view should show active messages")
	}
}

func TestMessageListView_ArchiveToggleFlag(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 40

	// Initially showArchived should be false
	if m.showArchived {
		t.Error("showArchived should be false by default")
	}

	// After toggling, should be true
	m.showArchived = true
	if !m.showArchived {
		t.Error("showArchived should be true after toggle")
	}
}

func TestMessageListView_IndicatesCursorPosition(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	for i := 0; i < 3; i++ {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   "test-agent",
			Subject:   "Message " + string(rune('A'+i)),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 40
	m.cursor = 1

	view := m.View()

	// View should contain indicator (> or similar) for selected item
	if !strings.Contains(view, ">") && !strings.Contains(view, "►") && !strings.Contains(view, "▶") {
		t.Error("view should indicate cursor position with a marker")
	}
}

// ---------------------------------------------------------------------------
// MessageListModel Update tests
// ---------------------------------------------------------------------------

func TestMessageListUpdate_JMovesDown(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	for i := 0; i < 3; i++ {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   "test-agent",
			Subject:   "Message " + string(rune('A'+i)),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "test-agent")
	m.height = 40
	m.cursor = 0

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after j", got.cursor)
	}
}

func TestMessageListUpdate_KMovesUp(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	for i := 0; i < 3; i++ {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   "test-agent",
			Subject:   "Message " + string(rune('A'+i)),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "test-agent")
	m.height = 40
	m.cursor = 1

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})

	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after k", got.cursor)
	}
}

func TestMessageListUpdate_JClampedAtEnd(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	for i := 0; i < 2; i++ {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   "test-agent",
			Subject:   "Message " + string(rune('A'+i)),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "test-agent")
	m.height = 40
	m.cursor = 1

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (clamped)", got.cursor)
	}
}

func TestMessageListUpdate_KClampedAtStart(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Message A",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageListModel(store, "test-agent")
	m.height = 40
	m.cursor = 0

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})

	if got.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped)", got.cursor)
	}
}

func TestMessageListUpdate_EnterSelectsMessage(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msgID := uuid.New()
	msg := &csuite.CsuiteInboxMessage{
		ID:        msgID,
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test Message",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageListModel(store, "test-agent")
	m.height = 40
	m.cursor = 0

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got.selectedMessageID == nil {
		t.Fatal("selectedMessageID should be set after Enter")
	}
	if *got.selectedMessageID != msgID {
		t.Errorf("selectedMessageID = %v, want %v", *got.selectedMessageID, msgID)
	}
}

func TestMessageListUpdate_CTriggersCompose(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	m := NewMessageListModel(store, "test-agent")
	m.height = 40

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})

	if !got.composeTriggered {
		t.Error("composeTriggered should be true after c")
	}
	if cmd == nil {
		t.Error("should return a command for compose trigger")
	}
}

func TestMessageListUpdate_AToggleArchiveVisibility(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	m := NewMessageListModel(store, "test-agent")
	m.height = 40
	m.showArchived = false

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if !got.showArchived {
		t.Error("showArchived should be true after a")
	}

	// Toggle again
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if got.showArchived {
		t.Error("showArchived should be false after second a")
	}
}

func TestMessageListUpdate_QuitKey(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	m := NewMessageListModel(store, "test-agent")
	m.height = 40

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if cmd == nil {
		t.Error("should return a command for quit key")
	}
}

// ---------------------------------------------------------------------------
// MessageDetailModel View rendering tests
// ---------------------------------------------------------------------------

func TestMessageDetailView_RendersFrontmatter(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	createdAt := time.Now()
	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test Subject",
		Body:      "Test body",
		Priority:  csuite.PriorityHigh,
		Type:      csuite.MessageTypeAlert,
		CreatedAt: createdAt,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	// Should show all frontmatter fields
	if !strings.Contains(view, "kyle") {
		t.Error("view should contain from field")
	}
	if !strings.Contains(view, "Test Subject") {
		t.Error("view should contain subject")
	}
	if !strings.Contains(view, "high") {
		t.Error("view should contain priority")
	}
	if !strings.Contains(view, "alert") {
		t.Error("view should contain message type")
	}
}

func TestMessageDetailView_RenderesBody(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test Subject",
		Body:      "This is the detailed message body with important information",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	if !strings.Contains(view, "This is the detailed message body") {
		t.Error("view should contain message body")
	}
}

func TestMessageDetailView_ShowsHelpBar(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.width = 100
	m.height = 40

	view := m.View()

	// Help bar should indicate navigation keys
	if !strings.Contains(view, "esc") && !strings.Contains(view, "[") {
		t.Error("view should show help with key bindings")
	}
}

// ---------------------------------------------------------------------------
// MessageDetailModel Update tests
// ---------------------------------------------------------------------------

func TestMessageDetailUpdate_EscGoesBack(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.height = 40

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if !got.backTriggered {
		t.Error("backTriggered should be true after Esc")
	}
	if cmd == nil {
		t.Error("should return a command for back trigger")
	}
}

func TestMessageDetailUpdate_RTriggersQuickReply(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	senderName := "kyle"
	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: senderName,
		ToAgent:   "test-agent",
		Subject:   "Original Subject",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.height = 40

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	// Should set reply fields
	if !got.quickReplyTriggered {
		t.Error("quickReplyTriggered should be true after r")
	}
	if got.replyTo != senderName {
		t.Errorf("replyTo = %q, want %q", got.replyTo, senderName)
	}
	if !strings.HasPrefix(got.replySubject, "Re:") {
		t.Errorf("replySubject should start with 'Re:', got %q", got.replySubject)
	}
	if cmd == nil {
		t.Error("should return a command for quick reply trigger")
	}
}

func TestMessageDetailUpdate_QuickReplyPrefillsCorrectly(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test Subject Here",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.height = 40

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if got.replyTo != "kyle" {
		t.Errorf("replyTo = %q, want 'kyle'", got.replyTo)
	}
	if got.replySubject != "Re: Test Subject Here" {
		t.Errorf("replySubject = %q, want 'Re: Test Subject Here'", got.replySubject)
	}
}

func TestMessageDetailUpdate_QuitKey(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msg := &csuite.CsuiteInboxMessage{
		ID:        uuid.New(),
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeStatus,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msg.ID, "test-agent")
	m.height = 40

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if cmd == nil {
		t.Error("should return a command for quit key")
	}
}

// ---------------------------------------------------------------------------
// MessageDetailModel LoadMessage tests
// ---------------------------------------------------------------------------

func TestMessageDetailLoad_FetchesMessageCorrectly(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	msgID := uuid.New()
	msg := &csuite.CsuiteInboxMessage{
		ID:        msgID,
		FromAgent: "kyle",
		ToAgent:   "test-agent",
		Subject:   "Test Subject",
		Body:      "Test body",
		Priority:  csuite.PriorityHigh,
		Type:      csuite.MessageTypeAlert,
		CreatedAt: time.Now(),
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	m := NewMessageDetailModel(store, msgID, "test-agent")

	// The model should have loaded the message
	// We can't directly check internal state, but View should render it
	view := m.View()

	if !strings.Contains(view, "kyle") {
		t.Error("model should have loaded the message with sender kyle")
	}
}

// ---------------------------------------------------------------------------
// MessageListModel Scrolling tests
// ---------------------------------------------------------------------------

func TestMessageListScroll_HandlesLongList(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	// Create 20 messages
	for i := 0; i < 20; i++ {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   "test-agent",
			Subject:   "Message " + string(rune('A'+rune(i%26))),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "test-agent")
	m.width = 100
	m.height = 10 // Small height to force scrolling

	view := m.View()

	// View should exist and contain some messages
	if view == "" {
		t.Error("view should not be empty")
	}
	if !strings.Contains(view, "Message") {
		t.Error("view should contain messages")
	}
}

// ---------------------------------------------------------------------------
// MessageListModel Filter tests
// ---------------------------------------------------------------------------

func TestMessageListFilter_FiltersByAgentCorrectly(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteInboxMessage{})
	store := csuite.NewStore(db)

	// Create messages for different agents
	for _, agentName := range []string{"agent-a", "agent-b"} {
		msg := &csuite.CsuiteInboxMessage{
			ID:        uuid.New(),
			FromAgent: "kyle",
			ToAgent:   agentName,
			Subject:   "Message for " + agentName,
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
			CreatedAt: time.Now(),
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	m := NewMessageListModel(store, "agent-a")
	m.width = 100
	m.height = 40

	view := m.View()

	// Should only show messages for agent-a
	if !strings.Contains(view, "Message for agent-a") {
		t.Error("view should contain message for the current agent")
	}
	if strings.Contains(view, "Message for agent-b") {
		t.Error("view should not show messages for other agents")
	}
}
