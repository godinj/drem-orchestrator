package benchv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type normalizedExternal struct {
	SessionID  string
	Steps      []ATIFStep
	Output     string
	StopReason string
}

func NormalizeExternal(kind, normalizer string, request TrialRequest, execution OuterExecutionResult) (HarnessRun, error) {
	if normalizer != normalizerForAdapter(kind) {
		return HarnessRun{}, fmt.Errorf("normalizer %q does not match adapter %q", normalizer, kind)
	}
	var (
		normalized normalizedExternal
		err        error
	)
	switch kind {
	case AdapterOpenCode:
		normalized, err = normalizeOpenCode(execution.Stdout, execution.StartedAt)
	case AdapterQwenCode:
		normalized, err = normalizeQwenCode(execution.Stdout, execution.StartedAt)
	case AdapterMiniSWE:
		raw := execution.Artifacts[".canvasbench/mini-swe-agent-trajectory.json"]
		normalized, err = normalizeMiniSWE(raw, execution.StartedAt)
	case AdapterPi:
		normalized, err = normalizePi(execution.Stdout, execution.StartedAt)
	case AdapterAider, AdapterOpenHands, AdapterGoose:
		normalized, err = normalizeWrappedCLI(execution.Stdout, execution.StartedAt, kind)
	default:
		err = fmt.Errorf("unsupported external normalizer %q", kind)
	}
	if err != nil {
		if normalized.SessionID == "" || len(normalized.Steps) == 0 {
			return HarnessRun{Output: string(execution.Stdout), StopReason: "invalid_trajectory"}, err
		}
		run := externalHarnessRun(normalized, normalizer, request, execution)
		run.StopReason = "invalid_trajectory"
		return run, err
	}
	run := externalHarnessRun(normalized, normalizer, request, execution)
	if execution.ExitCode != 0 {
		return run, fmt.Errorf("outer harness exited %d", execution.ExitCode)
	}
	return run, nil
}

func externalHarnessRun(normalized normalizedExternal, normalizer string, request TrialRequest, execution OuterExecutionResult) HarnessRun {
	trajectory := ATIFTrajectory{
		SchemaVersion: ATIFVersion, SessionID: normalized.SessionID,
		Agent: ATIFAgent{Name: request.Harness.Name, Version: request.Harness.Version, Model: request.Runtime.ModelID},
		Steps: normalized.Steps, FinalMetrics: ATIFMetrics{DurationMs: execution.Duration.Milliseconds()},
		Extra: map[string]any{"normalizer": normalizer, "harness_config_sha256": request.Harness.ConfigSHA256},
	}
	telemetry := Telemetry{DurationMs: execution.Duration.Milliseconds(), ToolCalls: countATIFToolCalls(normalized.Steps)}
	return HarnessRun{Output: normalized.Output, StopReason: normalized.StopReason, Telemetry: telemetry, Trajectory: trajectory}
}

func decodeJSONLines(raw []byte) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var lines []json.RawMessage
	for {
		var item json.RawMessage
		if err := decoder.Decode(&item); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("malformed JSON event: %w", err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &envelope); err != nil || envelope.Type == "" {
			return nil, fmt.Errorf("JSON event lacks a type")
		}
		lines = append(lines, append(json.RawMessage(nil), item...))
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("trajectory is empty")
	}
	return lines, nil
}

func marshalValue(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func atifTimestamp(start time.Time, index int) string {
	if start.IsZero() {
		start = time.Unix(0, 0).UTC()
	}
	return start.UTC().Add(time.Duration(index) * time.Millisecond).Format(time.RFC3339Nano)
}

func textContent(raw json.RawMessage) string {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var texts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "")
}

func countATIFToolCalls(steps []ATIFStep) int {
	count := 0
	for _, step := range steps {
		count += len(step.ToolCalls)
	}
	return count
}
