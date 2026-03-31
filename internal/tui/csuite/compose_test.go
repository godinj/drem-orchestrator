package csuite

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// ComposeModel initialization tests
// ---------------------------------------------------------------------------

func TestComposeModel_NewComposeModel_InitializesEmptyForm(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	if model.To() != "" {
		t.Errorf("expected To to be empty, got %q", model.To())
	}
	if model.Subject() != "" {
		t.Errorf("expected Subject to be empty, got %q", model.Subject())
	}
	if model.Body() != "" {
		t.Errorf("expected Body to be empty, got %q", model.Body())
	}
	if model.Priority() != csuite.PriorityNormal {
		t.Errorf("expected Priority to default to %q, got %q", csuite.PriorityNormal, model.Priority())
	}
}

func TestComposeModel_NewComposeModelWithRecipient_PreFillsTo(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store, WithTo("alice"))

	if model.To() != "alice" {
		t.Errorf("expected To to be %q, got %q", "alice", model.To())
	}
}

func TestComposeModel_NewComposeModelWithSubjectPrefix_PreFillsSubject(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store, WithSubjectPrefix("Re: "))

	if model.Subject() != "Re: " {
		t.Errorf("expected Subject to start with %q, got %q", "Re: ", model.Subject())
	}
}

// ---------------------------------------------------------------------------
// ComposeModel field input tests
// ---------------------------------------------------------------------------

func TestComposeModel_SetTo_UpdatesField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetTo("bob")

	if model.To() != "bob" {
		t.Errorf("expected To to be %q, got %q", "bob", model.To())
	}
}

func TestComposeModel_SetSubject_UpdatesField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetSubject("Hello World")

	if model.Subject() != "Hello World" {
		t.Errorf("expected Subject to be %q, got %q", "Hello World", model.Subject())
	}
}

func TestComposeModel_SetBody_UpdatesField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetBody("This is a test message\nwith multiple lines")

	if model.Body() != "This is a test message\nwith multiple lines" {
		t.Errorf("expected Body to match, got %q", model.Body())
	}
}

func TestComposeModel_SetPriority_UpdatesField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetPriority(csuite.PriorityHigh)

	if model.Priority() != csuite.PriorityHigh {
		t.Errorf("expected Priority to be %q, got %q", csuite.PriorityHigh, model.Priority())
	}
}

func TestComposeModel_SetMessageType_UpdatesField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetMessageType(csuite.MessageTypeRequest)

	if model.MessageType() != csuite.MessageTypeRequest {
		t.Errorf("expected MessageType to be %q, got %q", csuite.MessageTypeRequest, model.MessageType())
	}
}

// ---------------------------------------------------------------------------
// ComposeModel field validation tests
// ---------------------------------------------------------------------------

func TestComposeModel_Validate_FailsWhenToEmpty(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetSubject("Test")
	model.SetBody("Test")

	if model.Validate() {
		t.Error("expected Validate to fail when To is empty")
	}
}

func TestComposeModel_Validate_FailsWhenSubjectEmpty(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetTo("alice")
	model.SetBody("Test")

	if model.Validate() {
		t.Error("expected Validate to fail when Subject is empty")
	}
}

func TestComposeModel_Validate_FailsWhenBodyEmpty(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetTo("alice")
	model.SetSubject("Test")

	if model.Validate() {
		t.Error("expected Validate to fail when Body is empty")
	}
}

func TestComposeModel_Validate_SucceedsWhenAllFieldsFilled(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetTo("alice")
	model.SetSubject("Test")
	model.SetBody("Test body")

	if !model.Validate() {
		t.Error("expected Validate to succeed when all required fields are filled")
	}
}

// ---------------------------------------------------------------------------
// ComposeModel navigation tests
// ---------------------------------------------------------------------------

func TestComposeModel_CurrentFocusedField_StartsAtTo(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	focused := model.FocusedField()
	if focused != "to" {
		t.Errorf("expected initial focused field to be %q, got %q", "to", focused)
	}
}

func TestComposeModel_TabNavigation_MovesForwardThroughFields(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	fields := []string{"to", "subject", "priority", "type", "body"}
	for i, expectedField := range fields {
		if model.FocusedField() != expectedField {
			t.Errorf("step %d: expected focused field %q, got %q", i, expectedField, model.FocusedField())
		}
		model.FocusNext()
	}

	// After tabbing past the last field, should wrap to first
	if model.FocusedField() != "to" {
		t.Errorf("expected focus to wrap to %q, got %q", "to", model.FocusedField())
	}
}

func TestComposeModel_ShiftTabNavigation_MovesBackwardThroughFields(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Move to the second-to-last field first (field 4 = body)
	for i := 0; i < 4; i++ {
		model.FocusNext()
	}

	// Now move backwards through all fields
	fields := []string{"body", "type", "priority", "subject", "to"}
	for i, expectedField := range fields {
		if model.FocusedField() != expectedField {
			t.Errorf("step %d: expected focused field %q, got %q", i, expectedField, model.FocusedField())
		}
		model.FocusPrev()
	}

	// After shift-tab past the first field, should wrap to last (body)
	if model.FocusedField() != "body" {
		t.Errorf("expected focus to wrap to %q, got %q", "body", model.FocusedField())
	}
}

// ---------------------------------------------------------------------------
// ComposeModel input handling tests
// ---------------------------------------------------------------------------

func TestComposeModel_HandleInput_InToField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	model.HandleInput("alice")
	if model.To() != "alice" {
		t.Errorf("expected To to be %q, got %q", "alice", model.To())
	}
}

func TestComposeModel_HandleInput_InSubjectField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.FocusNext() // Move to subject field

	model.HandleInput("Test Subject")
	if model.Subject() != "Test Subject" {
		t.Errorf("expected Subject to be %q, got %q", "Test Subject", model.Subject())
	}
}

func TestComposeModel_HandleInput_InBodyField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	// Focus body field
	for i := 0; i < 4; i++ {
		model.FocusNext()
	}

	model.HandleInput("Test body text")
	if model.Body() != "Test body text" {
		t.Errorf("expected Body to contain text, got %q", model.Body())
	}
}

func TestComposeModel_HandleInput_InPriorityField_CyclesPriorities(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.FocusNext() // to subject
	model.FocusNext() // to priority

	// Initial priority is normal
	if model.Priority() != csuite.PriorityNormal {
		t.Errorf("expected initial priority to be %q, got %q", csuite.PriorityNormal, model.Priority())
	}

	// Cycle to next priority
	model.CycleNextPriority()
	if model.Priority() != csuite.PriorityHigh {
		t.Errorf("expected priority after cycle to be %q, got %q", csuite.PriorityHigh, model.Priority())
	}

	// Cycle again
	model.CycleNextPriority()
	if model.Priority() != csuite.PriorityLow {
		t.Errorf("expected priority after second cycle to be %q, got %q", csuite.PriorityLow, model.Priority())
	}

	// Cycle again should wrap
	model.CycleNextPriority()
	if model.Priority() != csuite.PriorityNormal {
		t.Errorf("expected priority after wrap to be %q, got %q", csuite.PriorityNormal, model.Priority())
	}
}

func TestComposeModel_HandleInput_InTypeField_CyclesTypes(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	// Focus type field
	for i := 0; i < 3; i++ {
		model.FocusNext()
	}

	// Initial type is status
	if model.MessageType() != csuite.MessageTypeStatus {
		t.Errorf("expected initial type to be %q, got %q", csuite.MessageTypeStatus, model.MessageType())
	}

	// Cycle to next type
	model.CycleNextType()
	if model.MessageType() != csuite.MessageTypeRequest {
		t.Errorf("expected type after cycle to be %q, got %q", csuite.MessageTypeRequest, model.MessageType())
	}

	// Cycle again
	model.CycleNextType()
	if model.MessageType() != csuite.MessageTypeAlert {
		t.Errorf("expected type after second cycle to be %q, got %q", csuite.MessageTypeAlert, model.MessageType())
	}

	// Cycle again should wrap
	model.CycleNextType()
	if model.MessageType() != csuite.MessageTypeStatus {
		t.Errorf("expected type after wrap to be %q, got %q", csuite.MessageTypeStatus, model.MessageType())
	}
}

// ---------------------------------------------------------------------------
// ComposeModel submission tests
// ---------------------------------------------------------------------------

func TestComposeModel_Submit_CreatesMessageInStore(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Fill form
	model.SetTo("alice")
	model.SetSubject("Test Message")
	model.SetBody("This is a test")
	model.SetPriority(csuite.PriorityHigh)

	// Submit
	err := model.Submit("bob") // bob is the "from" agent
	if err != nil {
		t.Fatalf("expected Submit to succeed, got error: %v", err)
	}

	// Verify message was created in store
	messages, err := store.GetMessagesByAgent("alice")
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}
	if len(messages) == 0 {
		t.Error("expected message to be created in store")
	}

	msg := messages[0]
	if msg.FromAgent != "bob" {
		t.Errorf("expected FromAgent to be %q, got %q", "bob", msg.FromAgent)
	}
	if msg.ToAgent != "alice" {
		t.Errorf("expected ToAgent to be %q, got %q", "alice", msg.ToAgent)
	}
	if msg.Subject != "Test Message" {
		t.Errorf("expected Subject to be %q, got %q", "Test Message", msg.Subject)
	}
	if msg.Body != "This is a test" {
		t.Errorf("expected Body to be %q, got %q", "This is a test", msg.Body)
	}
}

func TestComposeModel_Submit_FailsWhenValidationFails(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Leave To empty (invalid)
	model.SetSubject("Test")
	model.SetBody("Test")

	err := model.Submit("bob")
	if err == nil {
		t.Error("expected Submit to fail when validation fails")
	}
}

func TestComposeModel_Submit_FailsWhenStoreReturnsError(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Fill form with invalid data that would cause store to fail
	// (empty ToAgent will cause store.CreateMessage to fail)
	model.SetTo("")
	model.SetSubject("Test")
	model.SetBody("Test")

	err := model.Submit("bob")
	if err == nil {
		t.Error("expected Submit to fail when store returns error")
	}
}

func TestComposeModel_Submit_ResetsFormAfterSuccess(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Fill and submit
	model.SetTo("alice")
	model.SetSubject("Test")
	model.SetBody("Test")
	err := model.Submit("bob")
	if err != nil {
		t.Fatalf("expected Submit to succeed, got error: %v", err)
	}

	// Verify form is reset
	if model.To() != "" {
		t.Errorf("expected To to be reset to empty, got %q", model.To())
	}
	if model.Subject() != "" {
		t.Errorf("expected Subject to be reset to empty, got %q", model.Subject())
	}
	if model.Body() != "" {
		t.Errorf("expected Body to be reset to empty, got %q", model.Body())
	}
	if model.Priority() != csuite.PriorityNormal {
		t.Errorf("expected Priority to be reset to %q, got %q", csuite.PriorityNormal, model.Priority())
	}
}

// ---------------------------------------------------------------------------
// ComposeModel Bubble Tea interface tests
// ---------------------------------------------------------------------------

func TestComposeModel_Init_ReturnsCommand(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	cmd := model.Init()
	// Should return either nil or a valid Cmd (or no error if it's a function)
	// Just verify it doesn't panic
	_ = cmd
}

func TestComposeModel_Update_HandlesTabKey(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	initialFocus := model.FocusedField()
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	updatedFocus := newModel.(*ComposeModel).FocusedField()

	if initialFocus == updatedFocus {
		t.Error("expected Tab key to change focus")
	}
}

func TestComposeModel_Update_HandlesShiftTabKey(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	// Move forward first
	model.FocusNext()

	oldFocus := model.FocusedField()
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	newFocus := newModel.(*ComposeModel).FocusedField()

	if oldFocus == newFocus {
		t.Error("expected Shift+Tab key to change focus")
	}
}

func TestComposeModel_Update_HandlesEscapeKey_NoQuit(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("Escape key must not return a command (no tea.Quit)")
	}
}

func TestComposeModel_Update_HandlesRegularCharacterInput(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updatedModel := newModel.(*ComposeModel)

	if updatedModel.To() != "a" {
		t.Errorf("expected To to contain 'a', got %q", updatedModel.To())
	}
}

func TestComposeModel_Update_HandlesCtrlSSubmission(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Fill form first
	model.SetTo("alice")
	model.SetSubject("Test")
	model.SetBody("Test")

	// Ctrl+S should be handled by parent model, so just verify it doesn't panic
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if newModel == nil {
		t.Error("expected Update to return a model")
	}
}

func TestComposeModel_View_RendersForms(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)
	model.SetTo("alice")

	view := model.View()
	if view == "" {
		t.Error("expected View to return non-empty string")
	}
}

// ---------------------------------------------------------------------------
// ComposeModel error handling tests
// ---------------------------------------------------------------------------

func TestComposeModel_Submit_HandlesStoreCreationFailure(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Set invalid data that would make Store.CreateMessage fail
	model.SetTo("")
	model.SetSubject("Test")
	model.SetBody("Test")

	err := model.Submit("bob")
	if err == nil {
		t.Error("expected Submit to return error when store creation fails")
	}
}

// ---------------------------------------------------------------------------
// ComposeModel cancellation tests
// ---------------------------------------------------------------------------

func TestComposeModel_Cancel_ClearsForm(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Fill form
	model.SetTo("alice")
	model.SetSubject("Test")
	model.SetBody("Test")

	// Cancel
	model.Cancel()

	// Verify form is cleared
	if model.To() != "" {
		t.Errorf("expected To to be empty after cancel, got %q", model.To())
	}
	if model.Subject() != "" {
		t.Errorf("expected Subject to be empty after cancel, got %q", model.Subject())
	}
	if model.Body() != "" {
		t.Errorf("expected Body to be empty after cancel, got %q", model.Body())
	}
}

func TestComposeModel_Cancel_ResetsFocusToFirstField(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store)

	// Move focus
	model.FocusNext()
	model.FocusNext()

	// Cancel
	model.Cancel()

	// Verify focus is reset
	if model.FocusedField() != "to" {
		t.Errorf("expected focus to be reset to %q, got %q", "to", model.FocusedField())
	}
}

// ---------------------------------------------------------------------------
// ComposeModel quick reply tests
// ---------------------------------------------------------------------------

func TestComposeModel_NewComposeModelQuickReply_PreFillsFromAndRePrefixSubject(t *testing.T) {
	store := testutil.NewTestStore(t)
	model := NewComposeModel(store, WithQuickReply("alice", "Original Subject"))

	if model.To() != "alice" {
		t.Errorf("expected To to be %q, got %q", "alice", model.To())
	}

	subject := model.Subject()
	if subject != "Re: Original Subject" {
		t.Errorf("expected Subject to be %q, got %q", "Re: Original Subject", subject)
	}
}

// ---------------------------------------------------------------------------
// ComposeModel integration tests
// ---------------------------------------------------------------------------

func TestComposeModel_FullFormWorkflow(t *testing.T) {
	store := testutil.NewTestStore(t)
	fromAgent := "bob"

	// Create and fill compose model
	model := NewComposeModel(store)
	model.SetTo("alice")
	model.SetSubject("Integration Test")
	model.SetBody("This is a full workflow test")
	model.SetPriority(csuite.PriorityHigh)
	model.SetMessageType(csuite.MessageTypeRequest)

	// Submit the form
	err := model.Submit(fromAgent)
	if err != nil {
		t.Fatalf("expected Submit to succeed, got error: %v", err)
	}

	// Verify the message exists in store
	messages, err := store.GetMessagesByAgent("alice")
	if err != nil {
		t.Fatalf("failed to retrieve messages: %v", err)
	}

	if len(messages) == 0 {
		t.Fatal("expected at least one message in store")
	}

	msg := messages[0]
	if msg.ToAgent != "alice" || msg.FromAgent != fromAgent || msg.Subject != "Integration Test" {
		t.Error("message contents do not match submitted form data")
	}
}

func TestComposeModel_MultipleMessagesCanBeComposed(t *testing.T) {
	store := testutil.NewTestStore(t)

	// Create first message
	model := NewComposeModel(store)
	model.SetTo("alice")
	model.SetSubject("First Message")
	model.SetBody("First body")
	err := model.Submit("bob")
	if err != nil {
		t.Fatalf("expected first submit to succeed, got error: %v", err)
	}

	// Create second message
	model.SetTo("charlie")
	model.SetSubject("Second Message")
	model.SetBody("Second body")
	err = model.Submit("bob")
	if err != nil {
		t.Fatalf("expected second submit to succeed, got error: %v", err)
	}

	// Verify both messages exist
	alice, err := store.GetMessagesByAgent("alice")
	if err != nil || len(alice) != 1 {
		t.Error("expected one message for alice")
	}

	charlie, err := store.GetMessagesByAgent("charlie")
	if err != nil || len(charlie) != 1 {
		t.Error("expected one message for charlie")
	}
}
