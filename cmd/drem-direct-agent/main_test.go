package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenTraceUsesRuntimeDirectoryOutsideCheckout(t *testing.T) {
	workDir := t.TempDir()
	traceDir := t.TempDir()
	t.Setenv("DREM_WORKDIR", workDir)
	t.Setenv("DREM_TRACE_DIR", traceDir)
	t.Setenv("DREM_AGENT_ID", "12345678-aaaa")

	trace, err := openTrace()
	require.NoError(t, err)
	require.NoError(t, trace.Close())

	_, err = os.Stat(filepath.Join(traceDir, "agent-trace-12345678.jsonl"))
	require.NoError(t, err)
	entries, err := os.ReadDir(workDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestClassifyPushFailureDuplicateWhenRefsMatch(t *testing.T) {
	require.Equal(t, "duplicate", classifyPushFailure(
		"abc123",
		"abc123",
		"error: failed to push some refs to '/bare'",
	))
}

func TestClassifyPushFailureStaleRefOrRace(t *testing.T) {
	require.Equal(t, "stale_ref_or_race", classifyPushFailure(
		"local",
		"remote",
		"! [rejected] HEAD -> feature/x (fetch first)\nerror: failed to push some refs to '/bare'",
	))
}

func TestClassifyPushFailureUnknown(t *testing.T) {
	require.Equal(t, "unknown", classifyPushFailure("local", "", "fatal: 'origin' does not appear to be a git repository"))
}

func TestTailStringKeepsStderrTail(t *testing.T) {
	tail := tailString("prefix "+strings.Repeat("x", 20)+" suffix", 10)
	require.Equal(t, "xxx suffix", tail)
}
