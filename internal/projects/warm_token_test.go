package projects_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/projects"
)

func TestEnsureWarmAgentTokenPreservesSecureToken(t *testing.T) {
	home := t.TempDir()
	path, err := projects.EnsureWarmAgentToken(home)
	require.NoError(t, err)
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	again, err := projects.EnsureWarmAgentToken(home)
	require.NoError(t, err)
	second, err := os.ReadFile(again)
	require.NoError(t, err)
	require.Equal(t, first, second)
}
