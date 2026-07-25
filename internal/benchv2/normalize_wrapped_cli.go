package benchv2

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func normalizeWrappedCLI(raw []byte, started time.Time, harness string) (normalizedExternal, error) {
	var event struct {
		Schema    string `json:"schema"`
		Harness   string `json:"harness"`
		SessionID string `json:"session_id"`
		Output    string `json:"output"`
		ExitCode  int    `json:"exit_code"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return normalizedExternal{}, fmt.Errorf("invalid %s wrapper event: %w", harness, err)
	}
	if event.Schema != "canvasbench.cli-wrapper.v1" || event.Harness != harness || event.SessionID == "" {
		return normalizedExternal{}, fmt.Errorf("invalid %s wrapper identity", harness)
	}
	output := strings.TrimSpace(event.Output)
	if output == "" {
		return normalizedExternal{}, fmt.Errorf("%s wrapper output is empty", harness)
	}
	stop := "exit"
	if event.ExitCode != 0 {
		stop = "error"
	}
	return normalizedExternal{
		SessionID:  event.SessionID,
		Output:     output,
		StopReason: stop,
		Steps: []ATIFStep{{
			StepID:    "terminal-output",
			Timestamp: atifTimestamp(started, 0),
			Source:    "assistant",
			Message:   output,
		}},
	}, nil
}
