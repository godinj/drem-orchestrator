package ctxmon

import "fmt"

// StatusScript returns the content of a shell script that reads Claude Code's
// status line JSON from stdin and writes the context-usage.json file atomically.
// outputPath is the absolute path to the output JSON file.
func StatusScript(outputPath string) string {
	return fmt.Sprintf(`#!/bin/sh
# Context window status line script for drem-orchestrator.
# Reads Claude Code status JSON from stdin and writes usage data atomically.

INPUT=$(cat)
OUTFILE=%q
TMPFILE="${OUTFILE}.tmp"

if command -v jq >/dev/null 2>&1; then
    echo "$INPUT" | jq -c '{
        total_input_tokens: .context_window.total_input_tokens,
        total_output_tokens: .context_window.total_output_tokens,
        context_window_size: .context_window.context_window_size,
        used_percentage: .context_window.used_percentage,
        remaining_percentage: .context_window.remaining_percentage,
        total_cost_usd: .cost.total_cost_usd
    }' > "$TMPFILE" 2>/dev/null && mv "$TMPFILE" "$OUTFILE"
else
    echo "$INPUT" > "$TMPFILE" && mv "$TMPFILE" "$OUTFILE"
fi
`, outputPath)
}

// HooksJSON returns the PreCompact hook configuration as a map suitable for
// merging into the agent's settings.json hooks object. The hook writes a
// signal file when auto-compaction triggers.
func HooksJSON(signalPath string) map[string]any {
	return map[string]any{
		"PreCompact": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": fmt.Sprintf("touch %s", signalPath),
						"timeout": 5,
					},
				},
			},
		},
	}
}
