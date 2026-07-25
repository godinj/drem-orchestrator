package benchv2

import (
	"encoding/json"
	"fmt"
	"time"
)

type qwenContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     any             `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func normalizeQwenCode(raw []byte, started time.Time) (normalizedExternal, error) {
	lines, err := decodeJSONLines(raw)
	if err != nil {
		return normalizedExternal{}, err
	}
	var result normalizedExternal
	seenStart, seenResult, seenAssistant := false, false, false
	toolLocations := map[string][2]int{}
	for index, line := range lines {
		var event struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
			IsError   bool   `json:"is_error"`
			Duration  int64  `json:"duration_ms"`
			Result    string `json:"result"`
			Message   struct {
				Role    string             `json:"role"`
				Content []qwenContentBlock `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return normalizedExternal{}, fmt.Errorf("invalid Qwen event")
		}
		if event.SessionID != "" {
			if result.SessionID == "" {
				result.SessionID = event.SessionID
			} else if result.SessionID != event.SessionID {
				return normalizedExternal{}, fmt.Errorf("Qwen session id changed")
			}
		}
		switch event.Type {
		case "system":
			if (event.Subtype != "session_start" && event.Subtype != "init") || event.SessionID == "" {
				return normalizedExternal{}, fmt.Errorf("invalid Qwen session start")
			}
			seenStart = true
		case "assistant":
			if event.Message.Role != "assistant" || len(event.Message.Content) == 0 {
				return normalizedExternal{}, fmt.Errorf("invalid Qwen assistant event")
			}
			step := ATIFStep{StepID: nonemptyID(event.SessionID, index), Timestamp: atifTimestamp(started, index), Source: "assistant"}
			for _, block := range event.Message.Content {
				switch block.Type {
				case "thinking":
					if block.Thinking == "" {
						return normalizedExternal{}, fmt.Errorf("invalid Qwen thinking content")
					}
					step.Message += block.Thinking
				case "text":
					step.Message += block.Text
				case "tool_use":
					if block.ID == "" || block.Name == "" {
						return normalizedExternal{}, fmt.Errorf("invalid Qwen tool use")
					}
					step.ToolCalls = append(step.ToolCalls, ATIFToolCall{ID: block.ID, Name: block.Name, Arguments: marshalValue(block.Input)})
					toolLocations[block.ID] = [2]int{len(result.Steps), len(step.ToolCalls) - 1}
				default:
					return normalizedExternal{}, fmt.Errorf("unsupported Qwen assistant content %q", block.Type)
				}
			}
			result.Steps = append(result.Steps, step)
			seenAssistant = true
		case "user":
			if event.Message.Role != "user" {
				return normalizedExternal{}, fmt.Errorf("invalid Qwen user event")
			}
			for _, block := range event.Message.Content {
				if block.Type != "tool_result" || block.ToolUseID == "" {
					return normalizedExternal{}, fmt.Errorf("unsupported Qwen user content")
				}
				location, ok := toolLocations[block.ToolUseID]
				if !ok {
					return normalizedExternal{}, fmt.Errorf("Qwen tool result has no matching call")
				}
				result.Steps[location[0]].ToolCalls[location[1]].Result = rawOrString(block.Content)
				if block.IsError {
					result.Steps[location[0]].ToolCalls[location[1]].Error = result.Steps[location[0]].ToolCalls[location[1]].Result
				}
			}
		case "result":
			if event.Subtype != "success" || event.IsError || event.SessionID == "" {
				return normalizedExternal{}, fmt.Errorf("Qwen result reports failure")
			}
			seenResult = true
			result.Output = event.Result
			result.StopReason = event.Subtype
		case "stream_event":
			return normalizedExternal{}, fmt.Errorf("partial Qwen stream events are not accepted")
		default:
			return normalizedExternal{}, fmt.Errorf("unsupported Qwen event type %q", event.Type)
		}
	}
	if !seenStart || !seenResult || !seenAssistant || result.SessionID == "" || result.Output == "" {
		return normalizedExternal{}, fmt.Errorf("incomplete Qwen trajectory")
	}
	return result, nil
}

func rawOrString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}
