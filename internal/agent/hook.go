package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
)

// exitLogScriptTimeout is the hook timeout (in seconds) for the Stop hook command.
const exitLogScriptTimeout = 30

// generateExitLogScript returns a bash script that runs on agent Stop.
// It reads the latest JSONL transcript, extracts the last tool_use entry,
// reads agent-metadata.json for IDs, and appends an entry to exit-log.jsonl.
func generateExitLogScript(agentID uuid.UUID, taskID uuid.UUID, projectDir string) string {
	return fmt.Sprintf(`#!/bin/bash
# Exit log hook — captures structured exit info for the orchestrator.
set -euo pipefail

AGENT_ID="%s"
TASK_ID="%s"
PROJECT_DIR="%s"
EXIT_LOG="${PROJECT_DIR}/exit-log.jsonl"

# Read agent-metadata.json (written by orchestrator at spawn time).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
META_FILE="${SCRIPT_DIR}/agent-metadata.json"

# Find the latest .jsonl transcript in the project dir.
TRANSCRIPT=""
for f in "${PROJECT_DIR}"/*.jsonl; do
    if [ -f "$f" ] && [ "$f" != "$EXIT_LOG" ]; then
        TRANSCRIPT="$f"
    fi
done

# Extract last tool_use entry from transcript.
LAST_TOOL=""
LAST_TARGET=""
if [ -n "$TRANSCRIPT" ] && command -v jq >/dev/null 2>&1; then
    LAST_TOOL_LINE=$(grep -o '"type":"tool_use"[^}]*' "$TRANSCRIPT" 2>/dev/null | tail -1 || true)
    if [ -n "$LAST_TOOL_LINE" ]; then
        LAST_TOOL=$(echo "$LAST_TOOL_LINE" | grep -o '"name":"[^"]*"' | head -1 | sed 's/"name":"//;s/"//' || true)
    fi
fi

# Determine exit reason from environment or default.
EXIT_REASON="${CLAUDE_EXIT_REASON:-unknown}"

# Count git commits and modified files.
COMMITS_MADE=0
FILES_MODIFIED="[]"

# Build JSON entry and append to exit-log.jsonl.
TIMESTAMP=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)
printf '{"agent_id":"%%s","task_id":"%%s","timestamp":"%%s","exit_reason":"%%s","last_tool":"%%s","last_target":"%%s","files_modified":%%s,"commits_made":%%d,"summary":""}\n' \
    "$AGENT_ID" "$TASK_ID" "$TIMESTAMP" "$EXIT_REASON" "$LAST_TOOL" "$LAST_TARGET" "$FILES_MODIFIED" "$COMMITS_MADE" \
    >> "$EXIT_LOG"
`, agentID.String(), taskID.String(), projectDir)
}

// writeAgentMetadata writes agent-metadata.json to claudeDir containing
// the agent_id and task_id as a JSON object.
func writeAgentMetadata(claudeDir string, agentID uuid.UUID, taskID uuid.UUID) error {
	meta := map[string]string{
		"agent_id": agentID.String(),
		"task_id":  taskID.String(),
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal agent metadata: %w", err)
	}
	metaPath := filepath.Join(claudeDir, "agent-metadata.json")
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("write agent metadata: %w", err)
	}
	return nil
}

// buildSettingsJSON constructs the settings map for an agent's settings.json,
// including Notification, PreCompact, and Stop hooks. The existingHooks map
// provides pre-built Notification and PreCompact hooks from the caller.
func buildSettingsJSON(claudeDir, worktreePath, exitLogScriptPath string, existingHooks map[string]any) map[string]any {
	// Add Stop hook for exit logging.
	existingHooks["Stop"] = []any{
		map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": exitLogScriptPath,
					"timeout": exitLogScriptTimeout,
				},
			},
		},
	}

	statusScriptPath := filepath.Join(claudeDir, "context-status.sh")
	return map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": statusScriptPath,
		},
		"hooks": existingHooks,
	}
}

// enrichCompletion reads exit-log.jsonl from projectDir and populates
// comp.ExitInfo with the latest entry matching the agent ID.
func enrichCompletion(comp *Completion, projectDir string) {
	exitLogPath := filepath.Join(projectDir, "exit-log.jsonl")
	entries, err := agentmon.ReadExitLog(exitLogPath)
	if err != nil || len(entries) == 0 {
		return
	}

	agentIDStr := comp.AgentID.String()
	var latest *agentmon.ExitLogEntry
	for i := range entries {
		if entries[i].AgentID == agentIDStr {
			latest = &entries[i]
		}
	}
	if latest == nil {
		return
	}

	comp.ExitInfo = &ExitInfo{
		ExitReason:  latest.ExitReason,
		LastTool:    latest.LastTool,
		ExitSummary: latest.Summary,
	}
}

// openCodeEvent represents a single JSON event from the OpenCode JSONL output
// stream. Only the fields needed for exit info enrichment are included.
type openCodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Reason string `json:"reason"`
		Tokens struct {
			Total     int `json:"total"`
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
		} `json:"tokens"`
	} `json:"part"`
}

// enrichOpenCodeCompletion reads the OpenCode JSONL output log and populates
// comp.ExitInfo from the last step_finish event. If the file doesn't exist or
// no step_finish event is found, it returns without setting ExitInfo.
func enrichOpenCodeCompletion(comp *Completion, logPath string) {
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()

	var lastFinish *openCodeEvent
	scanner := bufio.NewScanner(f)
	// Allow large lines (OpenCode events can be sizeable).
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt openCodeEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Type == "step_finish" {
			copied := evt
			lastFinish = &copied
		}
	}

	if lastFinish == nil {
		return
	}

	comp.ExitInfo = &ExitInfo{
		ExitReason: lastFinish.Part.Reason,
	}
}
