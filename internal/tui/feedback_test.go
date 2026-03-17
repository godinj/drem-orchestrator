package tui

import (
	"strings"
	"testing"
)

func TestFeedbackModel_SetWidth(t *testing.T) {
	tests := []struct {
		name              string
		overlayInnerWidth int
	}{
		{"width 40", 40},
		{"width 80", 80},
		{"width 120", 120},
		{"width 140", 140},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFeedbackModel("Test Feedback")
			f.SetWidth(tt.overlayInnerWidth)

			halfWidth := tt.overlayInnerWidth / 2
			if f.input.Width() < halfWidth {
				t.Errorf("feedback input width = %d, want >= %d (half of overlay inner width %d)",
					f.input.Width(), halfWidth, tt.overlayInnerWidth)
			}
		})
	}
}

func TestFeedbackModel_Value(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"basic feedback", "Looks good to me", "Looks good to me"},
		{"trimmed whitespace", "  padded feedback  ", "padded feedback"},
		{"empty feedback", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFeedbackModel("Test")
			f.Show()
			f.input.SetValue(tt.text)

			got := f.Value()
			if got != tt.want {
				t.Errorf("Value() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFeedbackModel_TextareaWraps(t *testing.T) {
	f := NewFeedbackModel("Test")
	f.Show()
	multiLine := "First line\nSecond line\nThird line"
	f.input.SetValue(multiLine)

	got := f.Value()
	if !strings.Contains(got, "\n") {
		t.Errorf("feedback should preserve newlines for multi-line content, got %q", got)
	}
}
