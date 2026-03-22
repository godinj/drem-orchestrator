package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// CreateModel.Update tests
// ---------------------------------------------------------------------------

func TestCreateUpdate_TabSwitchesFocus(t *testing.T) {
	c := NewCreateModel()
	if c.focused != 0 {
		t.Fatalf("focused = %d, want 0 initially", c.focused)
	}

	// Tab to description
	got, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got.focused != 1 {
		t.Errorf("focused = %d, want 1 after tab", got.focused)
	}

	// Tab to quickfix
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got.focused != 2 {
		t.Errorf("focused = %d, want 2 after second tab", got.focused)
	}

	// Tab back to title
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got.focused != 0 {
		t.Errorf("focused = %d, want 0 after third tab", got.focused)
	}
}

func TestCreateUpdate_ShiftTabSwitchesFocus(t *testing.T) {
	c := NewCreateModel()

	// Shift+tab from title goes to quickfix (wraps around)
	got, _ := c.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got.focused != 2 {
		t.Errorf("focused = %d, want 2 after shift+tab from title", got.focused)
	}

	// Shift+tab from quickfix goes to description
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got.focused != 1 {
		t.Errorf("focused = %d, want 1 after shift+tab from quickfix", got.focused)
	}

	// Shift+tab from description goes back to title
	got, _ = got.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got.focused != 0 {
		t.Errorf("focused = %d, want 0 after shift+tab from description", got.focused)
	}
}

func TestCreateUpdate_DelegatesToFocusedInput(t *testing.T) {
	c := NewCreateModel()

	// Typing a character while title is focused should not crash
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	// cmd may or may not be nil depending on the textinput implementation
	_ = cmd
}

// ---------------------------------------------------------------------------
// CreateModel.Reset tests
// ---------------------------------------------------------------------------

func TestCreateReset(t *testing.T) {
	c := NewCreateModel()
	c.titleInput.SetValue("Some title")
	c.descInput.SetValue("Some description")
	c.focused = 1
	c.err = errForTest("some error")

	c.Reset()

	if c.focused != 0 {
		t.Errorf("focused = %d, want 0 after reset", c.focused)
	}
	if c.err != nil {
		t.Error("err should be nil after reset")
	}
	title, desc, _ := c.Value()
	if title != "" {
		t.Errorf("title = %q, want empty after reset", title)
	}
	if desc != "" {
		t.Errorf("desc = %q, want empty after reset", desc)
	}
}

// ---------------------------------------------------------------------------
// CreateModel.SetWidth edge cases
// ---------------------------------------------------------------------------

func TestCreateSetWidth_MinimumWidth(t *testing.T) {
	c := NewCreateModel()
	c.SetWidth(5) // Very small
	// Should not crash, input widths should be at least 10
	if c.titleInput.Width < 10 {
		t.Errorf("titleInput.Width = %d, want >= 10", c.titleInput.Width)
	}
}

// ---------------------------------------------------------------------------
// lipglossRender helper test
// ---------------------------------------------------------------------------

func TestLipglossRender(t *testing.T) {
	result := lipglossRender(colorDanger, "error text")
	if result == "" {
		t.Error("lipglossRender should return non-empty string")
	}
}
