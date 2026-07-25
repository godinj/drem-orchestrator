package benchv2

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDirectTraceProducesATIF17SingleSourceMetrics(t *testing.T) {
	trace := []agent.TraceEvent{{Iteration: 0, ElapsedMs: 12, Assistant: "inspect", ToolCalls: []agent.TraceCall{{Name: "read", Args: `{"path":"a"}`, Result: "ok"}}}}
	telemetry := Telemetry{TokensIn: 20, TokensOut: 4, DurationMs: 15}
	trajectory := NormalizeDirectTrace("run", validHarness(), validRuntime(), trace, telemetry)
	require.NoError(t, ValidateATIF(trajectory))
	require.Equal(t, ATIFVersion, trajectory.SchemaVersion)
	require.Equal(t, 20, trajectory.FinalMetrics.PromptTokens)
	require.Len(t, trajectory.Steps, 1)
	require.Len(t, trajectory.Steps[0].ToolCalls, 1)
}

func TestATIFValidationFailsClosed(t *testing.T) {
	require.Error(t, ValidateATIF(ATIFTrajectory{}))
}
