package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CreateModel is the new-task creation form with title and description fields.
type CreateModel struct {
	titleInput textinput.Model
	descInput  textinput.Model
	focused    int // 0=title, 1=desc
	err        error
}

// NewCreateModel creates a CreateModel with initialized text inputs.
func NewCreateModel() CreateModel {
	ti := textinput.New()
	ti.Placeholder = "Task title"
	ti.CharLimit = 120
	ti.Width = 50
	ti.Focus()

	di := textinput.New()
	di.Placeholder = "Description (what needs to be done)"
	di.CharLimit = 500
	di.Width = 50

	return CreateModel{
		titleInput: ti,
		descInput:  di,
		focused:    0,
	}
}

// Update handles messages for the create form.
func (c CreateModel) Update(msg tea.Msg) (CreateModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "shift+tab":
			// Toggle between title and description fields.
			if c.focused == 0 {
				c.focused = 1
				c.titleInput.Blur()
				c.descInput.Focus()
			} else {
				c.focused = 0
				c.descInput.Blur()
				c.titleInput.Focus()
			}
			return c, nil
		}
	}

	// Delegate to the focused input.
	var cmd tea.Cmd
	if c.focused == 0 {
		c.titleInput, cmd = c.titleInput.Update(msg)
	} else {
		c.descInput, cmd = c.descInput.Update(msg)
	}
	return c, cmd
}

// View renders the task creation form.
func (c CreateModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("New Task"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  Title:       %s\n", c.titleInput.View()))
	b.WriteString(fmt.Sprintf("  Description: %s\n", c.descInput.View()))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  [tab] switch field  [enter] create  [esc] cancel"))
	if c.err != nil {
		b.WriteString("\n")
		b.WriteString(lipglossRender(colorDanger, fmt.Sprintf("  Error: %v", c.err)))
	}
	return b.String()
}

// Value returns the entered title and description.
func (c CreateModel) Value() (title, description string) {
	return strings.TrimSpace(c.titleInput.Value()), strings.TrimSpace(c.descInput.Value())
}

// Reset clears the form inputs for reuse.
func (c *CreateModel) Reset() {
	c.titleInput.Reset()
	c.descInput.Reset()
	c.focused = 0
	c.titleInput.Focus()
	c.descInput.Blur()
	c.err = nil
}

// lipglossRender is a small helper to render text in a color.
func lipglossRender(color lipgloss.Color, text string) string {
	return lipgloss.NewStyle().Foreground(color).Render(text)
}
