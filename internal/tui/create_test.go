package tui

import (
	"strings"
	"testing"
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

			gotTitle, gotDesc := m.Value()
			if gotTitle != tt.wantTitle {
				t.Errorf("title = %q, want %q", gotTitle, tt.wantTitle)
			}
			if gotDesc != tt.wantDesc {
				t.Errorf("description = %q, want %q", gotDesc, tt.wantDesc)
			}
		})
	}
}

func TestCreateModel_DescriptionWraps(t *testing.T) {
	m := NewCreateModel()
	multiLine := "First line\nSecond line\nThird line"
	m.descInput.SetValue(multiLine)

	_, desc := m.Value()
	if !strings.Contains(desc, "\n") {
		t.Errorf("description should preserve newlines for multi-line content, got %q", desc)
	}
}
