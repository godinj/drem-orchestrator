package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// FeedbackModel is a text input dialog for plan rejection or test failure feedback.
type FeedbackModel struct {
	input   textarea.Model
	title   string // e.g. "Reject Plan" or "Fail Test"
	visible bool
}

// NewFeedbackModel creates a FeedbackModel with the given dialog title.
func NewFeedbackModel(title string) FeedbackModel {
	ta := textarea.New()
	ta.Placeholder = "Enter feedback..."
	ta.CharLimit = 500
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(3)
	ta.SetWidth(60)

	return FeedbackModel{
		input: ta,
		title: title,
	}
}

// Update handles messages for the feedback dialog.
func (f FeedbackModel) Update(msg tea.Msg) (FeedbackModel, tea.Cmd) {
	if !f.visible {
		return f, nil
	}

	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return f, cmd
}

// View renders the feedback dialog.
func (f FeedbackModel) View() string {
	if !f.visible {
		return ""
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(f.title))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  %s\n", f.input.View()))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  [enter] submit  [esc] cancel"))
	return b.String()
}

// Value returns the current feedback text.
func (f FeedbackModel) Value() string {
	return strings.TrimSpace(f.input.Value())
}

// Show makes the dialog visible and focuses the input.
func (f *FeedbackModel) Show() {
	f.visible = true
	f.input.Reset()
	f.input.Focus()
}

// Hide hides the dialog and blurs the input.
func (f *FeedbackModel) Hide() {
	f.visible = false
	f.input.Blur()
}

// SetWidth updates the input width to fit the given overlay inner width.
func (f *FeedbackModel) SetWidth(w int) {
	inputWidth := w - 2 // account for "  " indent prefix
	if inputWidth < 10 {
		inputWidth = 10
	}
	f.input.SetWidth(inputWidth)
}

// Visible returns whether the dialog is currently shown.
func (f FeedbackModel) Visible() bool {
	return f.visible
}
