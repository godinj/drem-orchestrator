package benchv2

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func normalizeOpenCode(raw []byte, started time.Time) (normalizedExternal, error) {
	lines, err := decodeJSONLines(raw)
	if err != nil {
		return normalizedExternal{}, err
	}
	var result normalizedExternal
	seenStart, seenFinish := false, false
	for index, line := range lines {
		var event struct {
			Type      string          `json:"type"`
			Timestamp int64           `json:"timestamp"`
			SessionID string          `json:"sessionID"`
			Part      json.RawMessage `json:"part"`
			Error     json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.SessionID == "" {
			return normalizedExternal{}, fmt.Errorf("invalid OpenCode envelope")
		}
		if result.SessionID == "" {
			result.SessionID = event.SessionID
		} else if result.SessionID != event.SessionID {
			return normalizedExternal{}, fmt.Errorf("OpenCode session id changed")
		}
		timestamp := atifTimestamp(started, index)
		if event.Timestamp > 0 {
			timestamp = time.UnixMilli(event.Timestamp).UTC().Format(time.RFC3339Nano)
		}
		switch event.Type {
		case "step_start":
			seenStart = true
		case "text":
			var part struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Part, &part) != nil || part.Type != "text" || part.Text == "" {
				return normalizedExternal{}, fmt.Errorf("invalid OpenCode text event")
			}
			result.Steps = append(result.Steps, ATIFStep{StepID: nonemptyID(part.ID, index), Timestamp: timestamp, Source: "assistant", Message: part.Text})
			result.Output = part.Text
		case "tool_use":
			var part struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Tool  string `json:"tool"`
				State struct {
					Status string `json:"status"`
					Input  any    `json:"input"`
					Output string `json:"output"`
					Error  string `json:"error"`
				} `json:"state"`
			}
			if json.Unmarshal(event.Part, &part) != nil || part.Type != "tool" || part.ID == "" || part.Tool == "" ||
				(part.State.Status != "completed" && part.State.Status != "error") {
				return normalizedExternal{}, fmt.Errorf("invalid OpenCode tool event")
			}
			result.Steps = append(result.Steps, ATIFStep{
				StepID: part.ID, Timestamp: timestamp, Source: "tool",
				ToolCalls: []ATIFToolCall{{ID: part.ID, Name: part.Tool, Arguments: marshalValue(part.State.Input), Result: part.State.Output, Error: part.State.Error}},
			})
		case "step_finish":
			var part struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			}
			if json.Unmarshal(event.Part, &part) != nil || part.Type != "step-finish" || part.Reason == "" {
				return normalizedExternal{}, fmt.Errorf("invalid OpenCode terminal event")
			}
			seenFinish = true
			result.StopReason = part.Reason
		case "error":
			return normalizedExternal{}, fmt.Errorf("OpenCode error event: %s", strings.TrimSpace(string(event.Error)))
		case "reasoning":
			// Reasoning is intentionally not copied into assistant text.
		default:
			return normalizedExternal{}, fmt.Errorf("unsupported OpenCode event type %q", event.Type)
		}
	}
	if !seenStart || !seenFinish || len(result.Steps) == 0 || result.Output == "" {
		return normalizedExternal{}, fmt.Errorf("incomplete OpenCode trajectory")
	}
	return result, nil
}

func nonemptyID(id string, index int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("event-%d", index)
}
