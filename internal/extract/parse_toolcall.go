package extract

import (
	"encoding/json"
	"time"
)

// transcriptLine is the minimal structure needed from a Claude JSONL
// transcript line.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content []transcriptBlock `json:"content"`
	} `json:"message"`
}

// transcriptBlock represents a single block within an assistant message's
// content array.
type transcriptBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// matchToolCall recognises Claude Code assistant-message transcript lines
// that contain a tool_use block and returns a *ToolCall. Non-JSON lines
// and non-assistant entries return (nil, false) cheaply.
func matchToolCall(src string, line []byte, now time.Time) (Event, bool) {
	if len(line) == 0 || line[0] != '{' {
		return nil, false
	}

	var entry transcriptLine
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, false
	}
	if entry.Type != "assistant" {
		return nil, false
	}

	for _, block := range entry.Message.Content {
		if block.Type != "tool_use" {
			continue
		}
		return &ToolCall{
			Timestamp: now,
			Source:    src,
			Tool:      block.Name,
			Target:    extractToolTarget(block.Name, block.Input),
		}, true
	}
	return nil, false
}

// extractToolTarget extracts a concise target string from a tool_use block
// based on the tool name. The rules mirror the original agentmon parser so
// that behaviour is preserved after the extraction.
func extractToolTarget(tool string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch tool {
	case "Read", "Edit", "Write":
		var input struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(raw, &input); err == nil && input.FilePath != "" {
			return input.FilePath
		}
	case "Grep", "Glob":
		var input struct {
			Path    string `json:"path"`
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(raw, &input); err == nil {
			target := input.Path
			if input.Pattern != "" {
				if target != "" {
					target += " "
				}
				target += input.Pattern
			}
			return target
		}
	case "Bash":
		var input struct {
			Description string `json:"description"`
			Command     string `json:"command"`
		}
		if err := json.Unmarshal(raw, &input); err == nil {
			if input.Description != "" {
				return input.Description
			}
			if len(input.Command) > 80 {
				return input.Command[:80]
			}
			return input.Command
		}
	}
	return ""
}
