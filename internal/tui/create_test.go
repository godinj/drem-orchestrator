package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCreateModel_SetWidth(t *testing.T) {
	tests := []struct {
		name              string
		overlayInnerWidth int
	}{
		{"width 40", 40},
		{"width 80", 80},
		{"width 120", 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewCreateModel()
			m.SetWidth(tt.overlayInnerWidth)

			halfWidth := tt.overlayInnerWidth / 2
			if m.descInput.Width() < halfWidth {
				t.Errorf("description input width = %d, want >= %d (half of overlay inner width %d)",
					m.descInput.Width(), halfWidth, tt.overlayInnerWidth)
			}
		})
	}
}

func TestCreateModel_Value(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		desc      string
		wantTitle string
		wantDesc  string
	}{
		{"basic input", "My Task", "Do the thing", "My Task", "Do the thing"},
		{"trimmed whitespace", "  padded  ", "  spaced  ", "padded", "spaced"},
		{"empty values", "", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewCreateModel()
			m.titleInput.SetValue(tt.title)
			m.descInput.SetValue(tt.desc)

			gotTitle, gotDesc, _ := m.Value()
			if gotTitle != tt.wantTitle {
				t.Errorf("title = %q, want %q", gotTitle, tt.wantTitle)
			}
			if gotDesc != tt.wantDesc {
				t.Errorf("description = %q, want %q", gotDesc, tt.wantDesc)
			}
		})
	}
}

// TestCreateModel_NoQuickFixInView verifies that the task creation form does
// NOT render a "Quick fix" checkbox. After the classifier agent replaces the
// one-shot classifier, the quick fix concept is removed from task creation.
func TestCreateModel_NoQuickFixInView(t *testing.T) {
	m := NewCreateModel()
	view := m.View()

	if strings.Contains(view, "Quick fix") {
		t.Errorf("View() should not contain 'Quick fix' checkbox, got:\n%s", view)
	}
	if strings.Contains(view, "[ ]") || strings.Contains(view, "[x]") {
		t.Errorf("View() should not render any checkbox, got:\n%s", view)
	}
}

// TestCreateModel_TwoFocusableFields verifies that tab cycles through exactly
// 2 fields (title, description) — not 3. The quick fix checkbox field is
// removed from the tab cycle.
func TestCreateModel_TwoFocusableFields(t *testing.T) {
	m := NewCreateModel()

	// Initially focused on field 0 (title).
	if m.focused != 0 {
		t.Fatalf("initial focus = %d, want 0", m.focused)
	}

	// Tab once: should move to field 1 (description).
	tabMsg := tea.KeyMsg{Type: tea.KeyTab}
	m, _ = m.Update(tabMsg)
	if m.focused != 1 {
		t.Errorf("after 1 tab, focus = %d, want 1", m.focused)
	}

	// Tab again: should wrap back to field 0 (title), not go to field 2.
	m, _ = m.Update(tabMsg)
	if m.focused != 0 {
		t.Errorf("after 2 tabs, focus = %d, want 0 (wrap around 2 fields)", m.focused)
	}
}

func TestCreateModel_DescriptionWraps(t *testing.T) {
	m := NewCreateModel()
	multiLine := "First line\nSecond line\nThird line"
	m.descInput.SetValue(multiLine)

	_, desc, _ := m.Value()
	if !strings.Contains(desc, "\n") {
		t.Errorf("description should preserve newlines for multi-line content, got %q", desc)
	}
}
