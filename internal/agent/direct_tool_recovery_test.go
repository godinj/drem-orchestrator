package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticLoopDetectorCatchesSameObservationAcrossDifferentReads(t *testing.T) {
	var detector semanticLoopDetector
	decision, _ := detector.Observe("read:a", "same")
	require.Equal(t, semanticRecoveryNone, decision)
	decision, _ = detector.Observe("read:b", "same")
	require.Equal(t, semanticRecoveryNone, decision)
	decision, reason := detector.Observe("read:c", "same")
	require.Equal(t, semanticRecoveryRehydrate, decision)
	require.Contains(t, reason, "same observation")
	decision, _ = detector.Observe("read:d", "same")
	require.Equal(t, semanticRecoveryStop, decision)
}

func TestSemanticLoopDetectorCatchesAlternatingCycle(t *testing.T) {
	var detector semanticLoopDetector
	detector.Observe("read:a", "a")
	detector.Observe("read:b", "b")
	detector.Observe("read:a", "a")
	decision, reason := detector.Observe("read:b", "b")
	require.Equal(t, semanticRecoveryRehydrate, decision)
	require.Contains(t, reason, "ABAB")
}

func TestFailedMutationLoopDetectorCountsAcrossInterleavedReads(t *testing.T) {
	var detector failedMutationLoopDetector
	args := `{"path":"src/file.cpp","old":"","new":"replacement"}`
	failure := "old string appears 14336 times"

	require.Equal(t, 1, detector.Observe("edit", args, failure))
	require.Zero(t, detector.Observe("read", `{"path":"src/file.cpp"}`, ""))
	require.Equal(t, 2, detector.Observe("edit", args, failure))
	require.Equal(t, 3, detector.Observe("edit", args, failure))

	detector.Reset()
	require.Equal(t, 1, detector.Observe("edit", args, failure))
}

func TestFailedMutationLoopDetectorRestoresDurableFailuresAfterLatestSuccess(t *testing.T) {
	messages := []toolChatMsg{
		{Role: "tool", Name: "edit", Content: "ERROR: old string not found"},
		{Role: "tool", Name: "write", Content: "wrote 12 bytes to src/new.cpp"},
		{Role: "tool", Name: "edit", Content: "ERROR: old string appears 14336 times"},
		{Role: "tool", Name: "read", Content: "file contents"},
		{Role: "tool", Name: "edit", Content: "ERROR: old string appears 14336 times\n\n[HARNESS] change it"},
	}
	var detector failedMutationLoopDetector
	detector.Restore(messages)
	require.Equal(t, 3, detector.Observe("edit", `{"path":"src/file.cpp","old":""}`, "old string appears 14336 times"))
	require.Equal(t, 1, detector.Observe("edit", `{"path":"src/file.cpp","old":"missing"}`, "old string not found"))
}
