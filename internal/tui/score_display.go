package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// renderScoreBadge renders a styled score badge from the scores map stored in
// a task's Context["scores"]. Returns empty string if scores is nil.
func renderScoreBadge(scores map[string]any) string {
	if scores == nil {
		return ""
	}
	formatted, ok := scores["formatted"].(string)
	if !ok {
		return ""
	}
	tdd, _ := scores["tdd"].(float64)
	con, _ := scores["constitution"].(float64)
	doc, _ := scores["documentation"].(float64)
	color := colorSuccess
	if tdd < 0.5 || con < 0.5 {
		color = colorDanger
	} else if tdd < 0.8 || doc < 0.5 {
		color = colorWarning
	}
	return lipgloss.NewStyle().Foreground(color).Render(formatted)
}

// taskScoreBadge returns a compact score indicator for display on the board
// view (e.g., "T:85 C:100 D:0"). Returns empty string if the context map has
// no scores.
func taskScoreBadge(ctx map[string]any) string {
	if ctx == nil {
		return ""
	}
	scores, ok := ctx["scores"].(map[string]any)
	if !ok {
		return ""
	}
	tdd, _ := scores["tdd"].(float64)
	con, _ := scores["constitution"].(float64)
	doc, _ := scores["documentation"].(float64)
	text := fmt.Sprintf("T:%d C:%d D:%d", int(tdd*100), int(con*100), int(doc*100))
	color := colorSuccess
	if tdd < 0.5 || con < 0.5 {
		color = colorDanger
	} else if tdd < 0.8 || doc < 0.5 {
		color = colorWarning
	}
	return lipgloss.NewStyle().Foreground(color).Render(text)
}
