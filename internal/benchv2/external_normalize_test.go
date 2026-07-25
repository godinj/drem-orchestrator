package benchv2

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExternalGoldenTrajectoriesNormalizeToATIF17(t *testing.T) {
	tests := []struct {
		kind       string
		normalizer string
		fixture    string
		output     string
		toolCalls  int
		artifact   bool
	}{
		{AdapterOpenCode, NormalizerOpenCode, "opencode.jsonl", "OpenCode finished exactly.", 1, false},
		{AdapterQwenCode, NormalizerQwenCode, "qwen-code.jsonl", "Qwen finished exactly.", 1, false},
		{AdapterMiniSWE, NormalizerMiniSWE, "mini-swe-agent.json", "Mini-SWE finished exactly.", 1, true},
		{AdapterPi, NormalizerPi, "pi.jsonl", "Pi finished exactly.", 1, false},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "external", test.fixture))
			require.NoError(t, err)
			execution := OuterExecutionResult{Stdout: raw, StartedAt: time.Unix(1784937600, 0), Duration: 2 * time.Second, Artifacts: map[string][]byte{}}
			if test.artifact {
				execution.Stdout = nil
				execution.Artifacts[".canvasbench/mini-swe-agent-trajectory.json"] = raw
			}
			request := TrialRequest{Task: TaskSpec{ID: "golden"}, Harness: HarnessConfig{Name: test.kind, Version: "pinned", ConfigSHA256: "cfg"}, Runtime: RuntimeAttestation{ModelID: "model"}}
			run, err := NormalizeExternal(test.kind, test.normalizer, request, execution)
			require.NoError(t, err)
			require.Equal(t, test.output, run.Output)
			require.Equal(t, test.toolCalls, run.Telemetry.ToolCalls)
			if test.kind == AdapterQwenCode {
				require.Contains(t, run.Trajectory.Steps[0].Message, "Inspect the declared file before editing.")
			}
			require.Equal(t, 0, run.Trajectory.FinalMetrics.PromptTokens, "harness logs must not supply server usage")
			require.NoError(t, ValidateATIF(run.Trajectory))
		})
	}
}

func TestExternalNormalizersFailClosedOnMalformedOrIncompleteData(t *testing.T) {
	request := TrialRequest{Task: TaskSpec{ID: "bad"}, Harness: HarnessConfig{Name: "bad", Version: "1"}}
	for _, test := range []struct {
		kind       string
		normalizer string
		stdout     string
		artifacts  map[string][]byte
	}{
		{AdapterOpenCode, NormalizerOpenCode, `{"type":"text"}`, nil},
		{AdapterQwenCode, NormalizerQwenCode, `{"type":"system","subtype":"session_start","session_id":"s"}` + "\n", nil},
		{AdapterMiniSWE, NormalizerMiniSWE, "", map[string][]byte{".canvasbench/mini-swe-agent-trajectory.json": []byte(`{"trajectory_format":"mini-swe-agent-1.0"}`)}},
		{AdapterPi, NormalizerPi, `{"type":"session","version":3,"id":"s"}` + "\n", nil},
	} {
		t.Run(test.kind, func(t *testing.T) {
			_, err := NormalizeExternal(test.kind, test.normalizer, request, OuterExecutionResult{Stdout: []byte(test.stdout), Artifacts: test.artifacts})
			require.Error(t, err)
		})
	}
}

func TestQwenHarnessUsageCannotPopulateServerTruth(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "external", "qwen-code.jsonl"))
	require.NoError(t, err)
	run, err := NormalizeExternal(AdapterQwenCode, NormalizerQwenCode,
		TrialRequest{Harness: HarnessConfig{Name: AdapterQwenCode, Version: "1"}},
		OuterExecutionResult{Stdout: raw, StartedAt: time.Unix(0, 0), Duration: time.Second})
	require.NoError(t, err)
	require.Equal(t, 0, run.Telemetry.TokensIn)
	require.Equal(t, ServerUsage{}, run.ServerUsage)
}

func TestQwenIncompleteTrajectoryPreservesMeasuredSteps(t *testing.T) {
	raw := []byte("{\"type\":\"system\",\"subtype\":\"session_start\",\"session_id\":\"s\"}\n" +
		"{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"Inspect first.\"},{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"read\",\"input\":{\"path\":\"x\"}}]}}\n")
	run, err := NormalizeExternal(AdapterQwenCode, NormalizerQwenCode,
		TrialRequest{Harness: HarnessConfig{Name: AdapterQwenCode, Version: "1", ConfigSHA256: "cfg"}, Runtime: RuntimeAttestation{ModelID: "model"}},
		OuterExecutionResult{Stdout: raw, StartedAt: time.Unix(0, 0), Duration: time.Second})
	require.ErrorContains(t, err, "incomplete Qwen trajectory")
	require.Equal(t, "invalid_trajectory", run.StopReason)
	require.Len(t, run.Trajectory.Steps, 1)
	require.Equal(t, 1, run.Telemetry.ToolCalls)
	require.Contains(t, run.Trajectory.Steps[0].Message, "Inspect first.")
}
