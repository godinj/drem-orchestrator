package benchv2

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func passingResult() TrialResult {
	return TrialResult{Status: "passed", Score: 100, Telemetry: Telemetry{TokensIn: 10, TokensOut: 5, PeakRequestInput: 10}, Gates: Gates{VerifierPassed: true, Compiled: true, ScopePassed: true, ReadScopePassed: true, OracleIsolated: true, Attested: true, RequiredMutationPassed: true, ArtifactAttested: true}}
}

func TestScoreHardGatesCapFailures(t *testing.T) {
	task := TaskSpec{Budget: Budget{MaxInputTokens: 100, MaxOutputTokens: 100}}
	result := passingResult()
	require.Equal(t, 100.0, Score(task, &result))
	result.Gates.Compiled = false
	require.LessOrEqual(t, Score(task, &result), 40.0)
	result = passingResult()
	result.Gates.Attested = false
	require.LessOrEqual(t, Score(task, &result), 40.0)
	result = passingResult()
	result.Gates.RequiredMutationPassed = false
	require.LessOrEqual(t, Score(task, &result), 40.0)
	result = passingResult()
	result.Gates.ArtifactAttested = false
	require.LessOrEqual(t, Score(task, &result), 40.0)
}

func TestAggregateMakesPlaceholdersAndCase9FailureIneligible(t *testing.T) {
	tasks := []TaskSpec{{ID: "case-08", Status: "runnable", Weight: 10}, {ID: "case-09", Status: "placeholder", Weight: 16}}
	result := passingResult()
	result.TaskID = "case-08"
	aggregate := AggregateResults("matrix", tasks, []TrialResult{result})
	require.False(t, aggregate.Eligible)
	require.Equal(t, "non_runnable", aggregate.Cases[1].Status)
	require.NotEmpty(t, aggregate.IneligibleReasons)
}

func TestWilsonConfidenceIntervalIsRecorded(t *testing.T) {
	low, high := wilson95(9, 10)
	require.Greater(t, low, 0.5)
	require.Less(t, high, 1.0)
}
