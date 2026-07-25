package benchv2

import (
	"fmt"
	"time"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

const ATIFVersion = "ATIF-v1.7"

type ATIFAgent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Model   string `json:"model,omitempty"`
}

type ATIFToolCall struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ATIFStep struct {
	StepID    string         `json:"step_id"`
	Timestamp string         `json:"timestamp"`
	Source    string         `json:"source"`
	Message   string         `json:"message,omitempty"`
	ToolCalls []ATIFToolCall `json:"tool_calls,omitempty"`
}

type ATIFMetrics struct {
	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	DurationMs       int64 `json:"duration_ms"`
}

type ATIFTrajectory struct {
	SchemaVersion string         `json:"schema_version"`
	SessionID     string         `json:"session_id"`
	Agent         ATIFAgent      `json:"agent"`
	Steps         []ATIFStep     `json:"steps"`
	FinalMetrics  ATIFMetrics    `json:"final_metrics"`
	Extra         map[string]any `json:"extra,omitempty"`
}

func NormalizeDirectTrace(runID string, harness HarnessConfig, runtime RuntimeAttestation, trace []agent.TraceEvent, metrics Telemetry) ATIFTrajectory {
	trajectory := ATIFTrajectory{
		SchemaVersion: ATIFVersion, SessionID: runID,
		Agent:        ATIFAgent{Name: harness.Name, Version: harness.Version, Model: runtime.ModelID},
		FinalMetrics: ATIFMetrics{PromptTokens: metrics.TokensIn, CompletionTokens: metrics.TokensOut, DurationMs: metrics.DurationMs},
		Extra:        map[string]any{"normalizer": "drem-direct-v2", "harness_config_sha256": harness.ConfigSHA256},
	}
	start := time.Unix(0, 0).UTC()
	for _, event := range trace {
		step := ATIFStep{
			StepID:    fmt.Sprintf("iteration-%d", event.Iteration),
			Timestamp: start.Add(time.Duration(event.ElapsedMs) * time.Millisecond).Format(time.RFC3339Nano),
			Source:    "agent", Message: event.Assistant,
		}
		for index, call := range event.ToolCalls {
			step.ToolCalls = append(step.ToolCalls, ATIFToolCall{
				ID: fmt.Sprintf("iteration-%d-call-%d", event.Iteration, index), Name: call.Name,
				Arguments: call.Args, Result: call.Result, Error: call.Error,
			})
		}
		trajectory.Steps = append(trajectory.Steps, step)
	}
	return trajectory
}

func ValidateATIF(trajectory ATIFTrajectory) error {
	if trajectory.SchemaVersion != ATIFVersion || trajectory.SessionID == "" || trajectory.Agent.Name == "" || trajectory.Agent.Version == "" {
		return fmt.Errorf("invalid ATIF identity")
	}
	if trajectory.FinalMetrics.PromptTokens < 0 || trajectory.FinalMetrics.CompletionTokens < 0 || trajectory.FinalMetrics.DurationMs < 0 {
		return fmt.Errorf("invalid ATIF final metrics")
	}
	if len(trajectory.Steps) == 0 {
		return fmt.Errorf("ATIF trajectory has no steps")
	}
	for _, step := range trajectory.Steps {
		if step.StepID == "" || step.Timestamp == "" || step.Source == "" {
			return fmt.Errorf("invalid ATIF step")
		}
		if _, err := time.Parse(time.RFC3339Nano, step.Timestamp); err != nil {
			return fmt.Errorf("invalid ATIF timestamp")
		}
		if step.Message == "" && len(step.ToolCalls) == 0 {
			return fmt.Errorf("empty ATIF step")
		}
		for _, call := range step.ToolCalls {
			if call.Name == "" || call.Arguments == "" {
				return fmt.Errorf("invalid ATIF tool call")
			}
		}
	}
	return nil
}
