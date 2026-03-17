package agentmon

import (
	"encoding/json"
	"time"
)

// transcriptLine is the minimal structure needed from a JSONL transcript line.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

// contentBlock represents a single block within the assistant message content.
type contentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// parseTranscriptLine parses a single JSONL line and extracts tool call
// information. Returns nil if the line is not an assistant message with a
// tool_use block.
func parseTranscriptLine(line []byte) *ToolCall {
	var entry transcriptLine
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil
	}
	if entry.Type != "assistant" {
		return nil
	}

	for _, block := range entry.Message.Content {
		if block.Type != "tool_use" {
			continue
		}

		tc := &ToolCall{
			Timestamp: time.Now(),
			Tool:      block.Name,
		}
		tc.Target = extractTarget(block.Name, block.Input)
		return tc
	}
	return nil
}

// extractTarget extracts the target string from a tool_use input based on
// the tool name.
func extractTarget(tool string, raw json.RawMessage) string {
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
