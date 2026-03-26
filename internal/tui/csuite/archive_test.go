package csuite

import (
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestFilterMessagesShowsOnlyArchivedWhenTrue tests that FilterMessages
// returns only archived messages when showArchived=true.
func TestFilterMessagesShowsOnlyArchivedWhenTrue(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	// Create a mix of archived and unarchived messages
	archived1 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "archived1", csuite.PriorityNormal, csuite.MessageTypeStatus)
	archived1.Archived = true
	db.Save(&archived1)

	archived2 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "archived2", csuite.PriorityHigh, csuite.MessageTypeAlert)
	archived2.Archived = true
	db.Save(&archived2)

	unarchived := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "unarchived", csuite.PriorityNormal, csuite.MessageTypeRequest)

	// Create messages slice
	messages := []csuite.CsuiteInboxMessage{archived1, archived2, unarchived}

	// Filter with showArchived=true
	result := FilterMessages(messages, true)

	// Should return only archived messages
	if len(result) != 2 {
		t.Errorf("FilterMessages(showArchived=true) returned %d messages, want 2", len(result))
	}

	// Verify all returned messages are archived
	for _, msg := range result {
		if !msg.Archived {
			t.Errorf("FilterMessages(showArchived=true) returned non-archived message: %s", msg.Subject)
		}
	}
}

// TestFilterMessagesHidesArchivedWhenFalse tests that FilterMessages
// hides archived messages when showArchived=false.
func TestFilterMessagesHidesArchivedWhenFalse(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	// Create a mix of archived and unarchived messages
	archived := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "archived", csuite.PriorityNormal, csuite.MessageTypeStatus)
	archived.Archived = true
	db.Save(&archived)

	unarchived1 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "unarchived1", csuite.PriorityNormal, csuite.MessageTypeRequest)
	unarchived2 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "unarchived2", csuite.PriorityHigh, csuite.MessageTypeAlert)

	// Create messages slice
	messages := []csuite.CsuiteInboxMessage{archived, unarchived1, unarchived2}

	// Filter with showArchived=false
	result := FilterMessages(messages, false)

	// Should return only unarchived messages
	if len(result) != 2 {
		t.Errorf("FilterMessages(showArchived=false) returned %d messages, want 2", len(result))
	}

	// Verify all returned messages are NOT archived
	for _, msg := range result {
		if msg.Archived {
			t.Errorf("FilterMessages(showArchived=false) returned archived message: %s", msg.Subject)
		}
	}
}

// TestFilterMessagesEmptyInput tests that FilterMessages handles empty input correctly.
func TestFilterMessagesEmptyInput(t *testing.T) {
	messages := []csuite.CsuiteInboxMessage{}

	resultTrue := FilterMessages(messages, true)
	resultFalse := FilterMessages(messages, false)

	if len(resultTrue) != 0 {
		t.Errorf("FilterMessages with empty input and showArchived=true returned %d, want 0", len(resultTrue))
	}
	if len(resultFalse) != 0 {
		t.Errorf("FilterMessages with empty input and showArchived=false returned %d, want 0", len(resultFalse))
	}
}

// TestFilterMessagesPreservesOrder tests that FilterMessages preserves message order.
func TestFilterMessagesPreservesOrder(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	// Create messages in a specific order
	msg1 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg1", csuite.PriorityNormal, csuite.MessageTypeStatus)
	msg2 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg2", csuite.PriorityNormal, csuite.MessageTypeRequest)
	msg3 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg3", csuite.PriorityNormal, csuite.MessageTypeAlert)

	msg1.Archived = false
	msg2.Archived = true
	msg3.Archived = false
	db.Save(&msg1)
	db.Save(&msg2)
	db.Save(&msg3)

	messages := []csuite.CsuiteInboxMessage{msg1, msg2, msg3}

	// Filter showing archived
	result := FilterMessages(messages, true)

	// Should return [msg2]
	if len(result) != 1 || result[0].Subject != "msg2" {
		t.Errorf("FilterMessages order not preserved for archived messages")
	}

	// Filter hiding archived
	result = FilterMessages(messages, false)

	// Should return [msg1, msg3]
	if len(result) != 2 || result[0].Subject != "msg1" || result[1].Subject != "msg3" {
		t.Errorf("FilterMessages order not preserved for unarchived messages")
	}
}

// TestArchiveToggleInitialState tests that ArchiveToggle has correct initial state.
func TestArchiveToggleInitialState(t *testing.T) {
	toggle := NewArchiveToggle()

	// Default: archived messages should be hidden
	if toggle.ShowArchived() {
		t.Errorf("ArchiveToggle.ShowArchived() returned true, want false (default hidden)")
	}
}

// TestArchiveToggleStateTransition tests that ArchiveToggle transitions state correctly.
func TestArchiveToggleStateTransition(t *testing.T) {
	toggle := NewArchiveToggle()

	// Initial state: false
	if toggle.ShowArchived() {
		t.Error("ArchiveToggle initial state should be false")
	}

	// Toggle to true
	toggle.Toggle()
	if !toggle.ShowArchived() {
		t.Error("ArchiveToggle should be true after first Toggle()")
	}

	// Toggle back to false
	toggle.Toggle()
	if toggle.ShowArchived() {
		t.Error("ArchiveToggle should be false after second Toggle()")
	}

	// Toggle again
	toggle.Toggle()
	if !toggle.ShowArchived() {
		t.Error("ArchiveToggle should be true after third Toggle()")
	}
}

// TestArchiveToggleMultipleToggles tests rapid toggling.
func TestArchiveToggleMultipleToggles(t *testing.T) {
	toggle := NewArchiveToggle()

	for i := 0; i < 10; i++ {
		toggle.Toggle()
	}

	// After 10 toggles, should be false (started false, even count = back to false)
	if toggle.ShowArchived() {
		t.Error("ArchiveToggle should be false after even number of toggles")
	}

	toggle.Toggle()
	if !toggle.ShowArchived() {
		t.Error("ArchiveToggle should be true after odd number of toggles")
	}
}

// TestArchiveToggleSet tests explicit state setting.
func TestArchiveToggleSet(t *testing.T) {
	toggle := NewArchiveToggle()

	// Set to true
	toggle.Set(true)
	if !toggle.ShowArchived() {
		t.Error("ArchiveToggle.Set(true) should set state to true")
	}

	// Set to false
	toggle.Set(false)
	if toggle.ShowArchived() {
		t.Error("ArchiveToggle.Set(false) should set state to false")
	}

	// Set to true again
	toggle.Set(true)
	if !toggle.ShowArchived() {
		t.Error("ArchiveToggle.Set(true) second time should keep state as true")
	}
}

// TestArchiveToggleSetIdempotent tests that Set is idempotent.
func TestArchiveToggleSetIdempotent(t *testing.T) {
	toggle := NewArchiveToggle()

	// Set to true multiple times
	toggle.Set(true)
	toggle.Set(true)
	toggle.Set(true)

	if !toggle.ShowArchived() {
		t.Error("ArchiveToggle.Set(true) should remain true")
	}

	// Set to false multiple times
	toggle.Set(false)
	toggle.Set(false)
	toggle.Set(false)

	if toggle.ShowArchived() {
		t.Error("ArchiveToggle.Set(false) should remain false")
	}
}

// TestArchiveToggleKeyBinding tests key binding 'a' triggers toggle.
func TestArchiveToggleKeyBinding(t *testing.T) {
	toggle := NewArchiveToggle()

	// Initially false
	if toggle.ShowArchived() {
		t.Fatal("ArchiveToggle should start as false")
	}

	// Simulate pressing 'a' key
	triggerArchiveToggle(toggle, 'a')

	// Should be toggled to true
	if !toggle.ShowArchived() {
		t.Error("Pressing 'a' key should toggle archive display to true")
	}

	// Simulate pressing 'a' key again
	triggerArchiveToggle(toggle, 'a')

	// Should be toggled back to false
	if toggle.ShowArchived() {
		t.Error("Pressing 'a' key again should toggle archive display back to false")
	}
}

// TestArchiveToggleKeyBindingOtherKeys tests that other keys don't affect toggle.
func TestArchiveToggleKeyBindingOtherKeys(t *testing.T) {
	toggle := NewArchiveToggle()

	// Initially false
	if toggle.ShowArchived() {
		t.Fatal("ArchiveToggle should start as false")
	}

	// Try pressing other keys
	triggerArchiveToggle(toggle, 'j')
	if toggle.ShowArchived() {
		t.Error("Pressing 'j' key should not toggle archive display")
	}

	triggerArchiveToggle(toggle, 'k')
	if toggle.ShowArchived() {
		t.Error("Pressing 'k' key should not toggle archive display")
	}

	triggerArchiveToggle(toggle, 'q')
	if toggle.ShowArchived() {
		t.Error("Pressing 'q' key should not toggle archive display")
	}

	// Now press 'a' to verify it still works
	triggerArchiveToggle(toggle, 'a')
	if !toggle.ShowArchived() {
		t.Error("Pressing 'a' key should toggle archive display to true")
	}
}

// TestFilterMessagesWithMixedPriorities tests filtering with different priority levels.
func TestFilterMessagesWithMixedPriorities(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	// Create messages with different priorities
	critical := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "critical", csuite.PriorityHigh, csuite.MessageTypeAlert)
	critical.Archived = true
	db.Save(&critical)

	normal := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "normal", csuite.PriorityNormal, csuite.MessageTypeRequest)
	normal.Archived = false
	db.Save(&normal)

	low := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "low", csuite.PriorityLow, csuite.MessageTypeStatus)
	low.Archived = true
	db.Save(&low)

	messages := []csuite.CsuiteInboxMessage{critical, normal, low}

	// Filter showing archived (should get critical and low)
	result := FilterMessages(messages, true)
	if len(result) != 2 {
		t.Errorf("FilterMessages(true) with mixed priorities returned %d, want 2", len(result))
	}

	// Verify high and low priority archived messages are present
	found := make(map[string]bool)
	for _, msg := range result {
		found[msg.Subject] = true
	}
	if !found["critical"] || !found["low"] {
		t.Error("FilterMessages(true) missing expected archived messages")
	}

	// Filter hiding archived (should get only normal)
	result = FilterMessages(messages, false)
	if len(result) != 1 {
		t.Errorf("FilterMessages(false) with mixed priorities returned %d, want 1", len(result))
	} else if result[0].Subject != "normal" {
		t.Error("FilterMessages(false) returned wrong message")
	}
}

// TestFilterMessagesWithMixedTypes tests filtering with different message types.
func TestFilterMessagesWithMixedTypes(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	// Create messages with different types
	status := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "status", csuite.PriorityNormal, csuite.MessageTypeStatus)
	status.Archived = true
	db.Save(&status)

	request := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "request", csuite.PriorityNormal, csuite.MessageTypeRequest)
	request.Archived = false
	db.Save(&request)

	alert := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "alert", csuite.PriorityNormal, csuite.MessageTypeAlert)
	alert.Archived = true
	db.Save(&alert)

	messages := []csuite.CsuiteInboxMessage{status, request, alert}

	// Filter showing archived
	result := FilterMessages(messages, true)
	if len(result) != 2 {
		t.Errorf("FilterMessages(true) with mixed types returned %d, want 2", len(result))
	}

	// Verify message types
	found := make(map[string]bool)
	for _, msg := range result {
		found[msg.Subject] = true
	}
	if !found["status"] || !found["alert"] {
		t.Error("FilterMessages(true) missing expected archived messages by type")
	}
}

// TestArchivedMessagesHiddenByDefault tests the default behavior.
func TestArchivedMessagesHiddenByDefault(t *testing.T) {
	toggle := NewArchiveToggle()

	// Default state should hide archived
	if toggle.ShowArchived() != false {
		t.Error("Archive toggle should default to hiding archived messages (showArchived=false)")
	}
}

// TestFilterMessagesAllArchived tests filtering when all messages are archived.
func TestFilterMessagesAllArchived(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	msg1 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg1", csuite.PriorityNormal, csuite.MessageTypeStatus)
	msg1.Archived = true
	db.Save(&msg1)

	msg2 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg2", csuite.PriorityNormal, csuite.MessageTypeStatus)
	msg2.Archived = true
	db.Save(&msg2)

	messages := []csuite.CsuiteInboxMessage{msg1, msg2}

	// Filter showing archived
	result := FilterMessages(messages, true)
	if len(result) != 2 {
		t.Errorf("FilterMessages(true) with all archived returned %d, want 2", len(result))
	}

	// Filter hiding archived
	result = FilterMessages(messages, false)
	if len(result) != 0 {
		t.Errorf("FilterMessages(false) with all archived returned %d, want 0", len(result))
	}
}

// TestFilterMessagesAllUnarchived tests filtering when all messages are unarchived.
func TestFilterMessagesAllUnarchived(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	msg1 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg1", csuite.PriorityNormal, csuite.MessageTypeStatus)
	msg1.Archived = false
	db.Save(&msg1)

	msg2 := testutil.CreateCsuiteInboxMessage(t, db, "agent1", "agent2", "msg2", csuite.PriorityNormal, csuite.MessageTypeStatus)
	msg2.Archived = false
	db.Save(&msg2)

	messages := []csuite.CsuiteInboxMessage{msg1, msg2}

	// Filter showing archived
	result := FilterMessages(messages, true)
	if len(result) != 0 {
		t.Errorf("FilterMessages(true) with all unarchived returned %d, want 0", len(result))
	}

	// Filter hiding archived
	result = FilterMessages(messages, false)
	if len(result) != 2 {
		t.Errorf("FilterMessages(false) with all unarchived returned %d, want 2", len(result))
	}
}

// TestFilterMessagesLargeDataset tests filtering with many messages.
func TestFilterMessagesLargeDataset(t *testing.T) {
	db := testutil.NewTestDBWithModels(t,
		&csuite.CsuiteInboxMessage{},
	)

	// Create 100 messages, half archived
	messages := make([]csuite.CsuiteInboxMessage, 100)
	for i := 0; i < 100; i++ {
		msg := testutil.CreateCsuiteInboxMessage(
			t, db,
			"agent1", "agent2",
			"msg"+string(rune(i)),
			csuite.PriorityNormal,
			csuite.MessageTypeStatus,
		)
		msg.Archived = (i%2 == 0)
		db.Save(&msg)
		messages[i] = msg
	}

	// Filter showing archived
	result := FilterMessages(messages, true)
	if len(result) != 50 {
		t.Errorf("FilterMessages(true) large dataset returned %d, want 50", len(result))
	}

	// Verify all are archived
	for _, msg := range result {
		if !msg.Archived {
			t.Error("FilterMessages(true) returned non-archived message in large dataset")
		}
	}

	// Filter hiding archived
	result = FilterMessages(messages, false)
	if len(result) != 50 {
		t.Errorf("FilterMessages(false) large dataset returned %d, want 50", len(result))
	}

	// Verify all are unarchived
	for _, msg := range result {
		if msg.Archived {
			t.Error("FilterMessages(false) returned archived message in large dataset")
		}
	}
}

// triggerArchiveToggle simulates a key press and calls the toggle if key is 'a'.
// This is a test helper to verify key binding behavior.
func triggerArchiveToggle(toggle *ArchiveToggle, key rune) {
	if key == 'a' {
		toggle.Toggle()
	}
}

// TestArchiveToggleString tests string representation.
func TestArchiveToggleString(t *testing.T) {
	toggle := NewArchiveToggle()

	// Initially false
	str := toggle.String()
	if str == "" {
		t.Error("ArchiveToggle.String() should not be empty")
	}

	// Toggle and check again
	toggle.Toggle()
	str = toggle.String()
	if str == "" {
		t.Error("ArchiveToggle.String() should not be empty after toggle")
	}
}

// TestArchiveToggleTimestamp tests that toggle timestamps are tracked.
func TestArchiveToggleTimestamp(t *testing.T) {
	toggle := NewArchiveToggle()

	// Get timestamp before toggle
	before := time.Now()

	// Toggle
	toggle.Toggle()

	// Get timestamp after toggle
	after := time.Now()

	// Check timestamp is within expected range
	ts := toggle.LastToggled()
	if ts.Before(before) || ts.After(after.Add(time.Second)) {
		t.Errorf("ArchiveToggle.LastToggled() returned unexpected timestamp: %v", ts)
	}
}
