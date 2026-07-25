package benchv2

import (
	"encoding/json"
	"fmt"
	"time"
)

func normalizePi(raw []byte, started time.Time) (normalizedExternal, error) {
	lines, err := decodeJSONLines(raw)
	if err != nil {
		return normalizedExternal{}, err
	}
	var result normalizedExternal
	seenSession, seenStart, seenEnd := false, false, false
	pending := map[string]ATIFToolCall{}
	for index, line := range lines {
		var event struct {
			Type       string          `json:"type"`
			Version    int             `json:"version"`
			ID         string          `json:"id"`
			Timestamp  string          `json:"timestamp"`
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Args       any             `json:"args"`
			Result     any             `json:"result"`
			IsError    bool            `json:"isError"`
			Message    json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return normalizedExternal{}, fmt.Errorf("invalid Pi event")
		}
		timestamp := atifTimestamp(started, index)
		if event.Timestamp != "" {
			parsed, err := time.Parse(time.RFC3339Nano, event.Timestamp)
			if err != nil {
				return normalizedExternal{}, fmt.Errorf("invalid Pi timestamp")
			}
			timestamp = parsed.UTC().Format(time.RFC3339Nano)
		}
		switch event.Type {
		case "session":
			if index != 0 || event.Version != 3 || event.ID == "" {
				return normalizedExternal{}, fmt.Errorf("invalid Pi session header")
			}
			seenSession = true
			result.SessionID = event.ID
		case "agent_start":
			seenStart = true
		case "agent_end":
			seenEnd = true
			result.StopReason = "agent_end"
		case "message_end":
			var message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(event.Message, &message) != nil {
				return normalizedExternal{}, fmt.Errorf("invalid Pi message_end")
			}
			if message.Role != "assistant" {
				continue
			}
			content := textContent(message.Content)
			if content == "" {
				return normalizedExternal{}, fmt.Errorf("Pi assistant message is empty")
			}
			result.Steps = append(result.Steps, ATIFStep{StepID: fmt.Sprintf("message-%d", index), Timestamp: timestamp, Source: "assistant", Message: content})
			result.Output = content
		case "tool_execution_start":
			if event.ToolCallID == "" || event.ToolName == "" {
				return normalizedExternal{}, fmt.Errorf("invalid Pi tool start")
			}
			if _, exists := pending[event.ToolCallID]; exists {
				return normalizedExternal{}, fmt.Errorf("duplicate Pi tool call")
			}
			pending[event.ToolCallID] = ATIFToolCall{ID: event.ToolCallID, Name: event.ToolName, Arguments: marshalValue(event.Args)}
		case "tool_execution_end":
			call, exists := pending[event.ToolCallID]
			if !exists || event.ToolName != call.Name {
				return normalizedExternal{}, fmt.Errorf("Pi tool end has no matching start")
			}
			call.Result = marshalValue(event.Result)
			if event.IsError {
				call.Error = call.Result
			}
			delete(pending, event.ToolCallID)
			result.Steps = append(result.Steps, ATIFStep{StepID: event.ToolCallID, Timestamp: timestamp, Source: "tool", ToolCalls: []ATIFToolCall{call}})
		case "turn_start", "turn_end", "message_start", "message_update", "tool_execution_update", "queue_update", "compaction_start", "compaction_end", "auto_retry_start", "auto_retry_end", "summarization_retry_scheduled", "summarization_retry_attempt_start", "summarization_retry_finished", "agent_settled":
			// Lifecycle and partial-update events carry no additional final text.
		default:
			return normalizedExternal{}, fmt.Errorf("unsupported Pi event type %q", event.Type)
		}
	}
	if !seenSession || !seenStart || !seenEnd || len(pending) != 0 || len(result.Steps) == 0 || result.Output == "" {
		return normalizedExternal{}, fmt.Errorf("incomplete Pi trajectory")
	}
	return result, nil
}
