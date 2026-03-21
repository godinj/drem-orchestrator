package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// FeedbackModel.Update tests
// ---------------------------------------------------------------------------

func TestFeedbackUpdate_NotVisibleIsNoop(t *testing.T) {
	f := NewFeedbackModel("Test")
	// visible is false by default

	got, cmd := f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("Update when not visible should return nil cmd")
	}
	if got.visible {
		t.Error("should remain not visible")
	}
}

func TestFeedbackUpdate_VisibleDelegatesToInput(t *testing.T) {
	f := NewFeedbackModel("Test")
	f.Show()

	// Send a key while visible - should not crash
	_, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
}

// ---------------------------------------------------------------------------
// FeedbackModel.Show/Hide/Visible tests
// ---------------------------------------------------------------------------

func TestFeedbackShowHide(t *testing.T) {
	f := NewFeedbackModel("Test")

	if f.Visible() {
		t.Error("should not be visible initially")
	}

	f.Show()
	if !f.Visible() {
		t.Error("should be visible after Show")
	}

	f.Hide()
	if f.Visible() {
		t.Error("should not be visible after Hide")
	}
}

func TestFeedbackShowResetsInput(t *testing.T) {
	f := NewFeedbackModel("Test")
	f.Show()
	f.input.SetValue("old value")

	f.Show() // second show should reset
	got := f.Value()
	if got != "" {
		t.Errorf("Value = %q, want empty after Show (reset)", got)
	}
}

// ---------------------------------------------------------------------------
// FeedbackModel.SetWidth edge cases
// ---------------------------------------------------------------------------

func TestFeedbackSetWidth_MinimumWidth(t *testing.T) {
	f := NewFeedbackModel("Test")
	f.SetWidth(5) // Very small
	// Should not crash; input width should be at least 10
	if f.input.Width() < 10 {
		t.Errorf("input.Width = %d, want >= 10", f.input.Width())
	}
}
