package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

func TestBoundedStopWithWork(t *testing.T) {
	dir := t.TempDir()
	runGitForTest(t, dir, "init")
	runGitForTest(t, dir, "config", "user.email", "test@example.com")
	runGitForTest(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o644))
	runGitForTest(t, dir, "add", "tracked.txt")
	runGitForTest(t, dir, "commit", "-m", "base")

	startSHA := gitForTest(t, dir, "rev-parse", "HEAD")
	require.False(t, boundedStopWithWork(dir, startSHA, &agent.DirectToolAgentResult{StopReason: agent.DirectToolStopReasonNoProgress}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("changed\n"), 0o644))
	for _, reason := range []string{agent.DirectToolStopReasonMaxIterations, agent.DirectToolStopReasonContextLimit, agent.DirectToolStopReasonTokenBudget} {
		require.True(t, boundedStopWithWork(dir, startSHA, &agent.DirectToolAgentResult{StopReason: reason}), reason)
	}
	for _, reason := range []string{agent.DirectToolStopReasonNoProgress, agent.DirectToolStopReasonMaxTokens, agent.DirectToolStopReasonTimeout, ""} {
		require.False(t, boundedStopWithWork(dir, startSHA, &agent.DirectToolAgentResult{StopReason: reason}), reason)
	}
	require.False(t, boundedStopWithWork(dir, startSHA, &agent.DirectToolAgentResult{
		StopReason: agent.DirectToolStopReasonTokenBudget, PendingMutationRepairs: []string{"second.cpp"},
	}), "a partial checkpoint with an unresolved failed edit must exit non-zero")
	require.False(t, boundedStopWithWork(dir, startSHA, &agent.DirectToolAgentResult{
		StopReason: agent.DirectToolStopReasonTokenBudget, MissingRequiredMutations: []string{"second.cpp"},
	}), "a partial checkpoint missing a required output file must exit non-zero")

	runGitForTest(t, dir, "add", "tracked.txt")
	runGitForTest(t, dir, "commit", "-m", "completed work")
	require.Empty(t, gitForTest(t, dir, "status", "--porcelain"))
	require.False(t, boundedStopWithWork(dir, startSHA, &agent.DirectToolAgentResult{StopReason: agent.DirectToolStopReasonMaxTokens}))
}

func TestEnvJSONStrings(t *testing.T) {
	t.Setenv("DREM_SCOPED_FILES_JSON", `[" src/Main.cpp ","","tests/unit/test_Main.cpp"]`)
	require.Equal(t,
		[]string{"src/Main.cpp", "tests/unit/test_Main.cpp"},
		envJSONStrings("DREM_SCOPED_FILES_JSON"))
}

func TestEnvJSONStringsInvalidFallsBackToUnscoped(t *testing.T) {
	t.Setenv("DREM_SCOPED_FILES_JSON", `{not-json}`)
	require.Nil(t, envJSONStrings("DREM_SCOPED_FILES_JSON"))
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func gitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(bytes.TrimSpace(out))
}
