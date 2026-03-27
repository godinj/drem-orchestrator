package tui

import (
	"github.com/godinj/drem-orchestrator/internal/model"
)

// DisplayStatus maps a TaskStatus to its display string representation.
// This is used in the TUI to render status values. The special case is
// needs_clarification which is truncated to needs_clar to fit TUI column widths.
//
// Examples:
//   - StatusNeedsClarification → "needs_clar"
//   - StatusInProgress → "in_progress"
//   - StatusDone → "done"
func DisplayStatus(status model.TaskStatus) string {
	if status == model.StatusNeedsClarification {
		return "needs_clar"
	}
	return string(status)
}
