package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
