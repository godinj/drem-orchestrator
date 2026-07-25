package benchv2

import (
	"encoding/json"
	"fmt"
	"time"
)

func normalizeMiniSWE(raw []byte, started time.Time) (normalizedExternal, error) {
	if len(raw) == 0 {
		return normalizedExternal{}, fmt.Errorf("mini-SWE trajectory artifact is missing")
	}
	var trajectory struct {
		TrajectoryFormat string `json:"trajectory_format"`
		Info             struct {
			ExitStatus string `json:"exit_status"`
			Submission string `json:"submission"`
		} `json:"info"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Extra   struct {
				Timestamp  float64 `json:"timestamp"`
				ReturnCode *int    `json:"returncode"`
				Actions    []struct {
					Command string `json:"command"`
				} `json:"actions"`
				ExitStatus string `json:"exit_status"`
				Submission string `json:"submission"`
			} `json:"extra"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &trajectory); err != nil {
		return normalizedExternal{}, fmt.Errorf("malformed mini-SWE trajectory: %w", err)
	}
	if trajectory.TrajectoryFormat != NormalizerMiniSWE || trajectory.Info.ExitStatus != "Submitted" || len(trajectory.Messages) == 0 {
		return normalizedExternal{}, fmt.Errorf("incomplete mini-SWE trajectory")
	}
	result := normalizedExternal{SessionID: "mini-swe-agent", Output: trajectory.Info.Submission, StopReason: trajectory.Info.ExitStatus}
	var lastAssistant int = -1
	for index, message := range trajectory.Messages {
		timestamp := atifTimestamp(started, index)
		if message.Extra.Timestamp > 0 {
			seconds := int64(message.Extra.Timestamp)
			nanos := int64((message.Extra.Timestamp - float64(seconds)) * float64(time.Second))
			timestamp = time.Unix(seconds, nanos).UTC().Format(time.RFC3339Nano)
		}
		switch message.Role {
		case "system":
			continue
		case "assistant":
			content := textContent(message.Content)
			if content == "" {
				return normalizedExternal{}, fmt.Errorf("mini-SWE assistant message is empty")
			}
			step := ATIFStep{StepID: fmt.Sprintf("message-%d", index), Timestamp: timestamp, Source: "assistant", Message: content}
			for actionIndex, action := range message.Extra.Actions {
				if action.Command == "" {
					return normalizedExternal{}, fmt.Errorf("mini-SWE action is empty")
				}
				step.ToolCalls = append(step.ToolCalls, ATIFToolCall{
					ID: fmt.Sprintf("message-%d-action-%d", index, actionIndex), Name: "bash",
					Arguments: marshalValue(map[string]string{"command": action.Command}),
				})
			}
			result.Steps = append(result.Steps, step)
			lastAssistant = len(result.Steps) - 1
		case "user", "tool":
			if message.Extra.ReturnCode == nil {
				if message.Extra.ExitStatus != "" {
					continue
				}
				continue
			}
			if lastAssistant < 0 || len(result.Steps[lastAssistant].ToolCalls) == 0 {
				return normalizedExternal{}, fmt.Errorf("mini-SWE observation has no action")
			}
			call := &result.Steps[lastAssistant].ToolCalls[len(result.Steps[lastAssistant].ToolCalls)-1]
			call.Result = textContent(message.Content)
			if *message.Extra.ReturnCode != 0 {
				call.Error = fmt.Sprintf("returncode=%d", *message.Extra.ReturnCode)
			}
		default:
			return normalizedExternal{}, fmt.Errorf("unsupported mini-SWE role %q", message.Role)
		}
	}
	if len(result.Steps) == 0 || result.Output == "" {
		return normalizedExternal{}, fmt.Errorf("incomplete mini-SWE trajectory")
	}
	return result, nil
}
