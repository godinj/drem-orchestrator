package tui

// DisplayStatus maps a status string to its display representation.
// The special case is needs_clarification which is truncated to needs_clar
// to fit TUI column widths. All other status strings pass through unchanged.
func DisplayStatus(status string) string {
	if status == "needs_clarification" {
		return "needs_clar"
	}
	return status
}
