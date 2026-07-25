package benchv2

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScopedAgentWorkspaceHidesFixtureAndAppliesOnlyDeclaredWrites(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, ".git", "config"), []byte("secret git"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "read.cpp"), []byte("visible read"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "write.cpp"), []byte("before"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "secret.cpp"), []byte("hidden"), 0o644))

	workspace, err := PrepareScopedAgentWorkspace(fixture, []string{"read.cpp", "write.cpp"}, []string{"write.cpp"}, nil)
	require.NoError(t, err)
	defer workspace.Cleanup()
	_, err = os.Stat(filepath.Join(workspace.WorkDir, "secret.cpp"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(workspace.WorkDir, ".git"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.WriteFile(filepath.Join(workspace.WorkDir, "write.cpp"), []byte("after"), 0o644))
	mutation, err := workspace.MutationObserved()
	require.NoError(t, err)
	require.True(t, mutation)
	require.NoError(t, workspace.ValidateAndApply())
	updated, err := os.ReadFile(filepath.Join(fixture, "write.cpp"))
	require.NoError(t, err)
	require.Equal(t, "after", string(updated))
	hidden, err := os.ReadFile(filepath.Join(fixture, "secret.cpp"))
	require.NoError(t, err)
	require.Equal(t, "hidden", string(hidden))
}

func TestScopedAgentWorkspaceObservesCreatedAndDeletedWritableFiles(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "existing.cpp"), []byte("before"), 0o644))
	workspace, err := PrepareScopedAgentWorkspace(fixture, nil, []string{"existing.cpp", "new.cpp"}, nil)
	require.NoError(t, err)
	defer workspace.Cleanup()
	mutation, err := workspace.MutationObserved()
	require.NoError(t, err)
	require.False(t, mutation)
	require.NoError(t, os.Remove(filepath.Join(workspace.WorkDir, "existing.cpp")))
	mutation, err = workspace.MutationObserved()
	require.NoError(t, err)
	require.True(t, mutation)
}

func TestScopedAgentWorkspaceRejectsUndeclaredAndReadOnlyWrites(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "read.cpp"), []byte("read"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "write.cpp"), []byte("before"), 0o644))

	t.Run("undeclared", func(t *testing.T) {
		workspace, err := PrepareScopedAgentWorkspace(fixture, []string{"read.cpp"}, []string{"write.cpp"}, nil)
		require.NoError(t, err)
		defer workspace.Cleanup()
		require.NoError(t, os.WriteFile(filepath.Join(workspace.WorkDir, "escape.cpp"), []byte("escape"), 0o644))
		require.ErrorContains(t, workspace.Validate(), "undeclared output")
		_, err = os.Stat(filepath.Join(fixture, "escape.cpp"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("read-only", func(t *testing.T) {
		workspace, err := PrepareScopedAgentWorkspace(fixture, []string{"read.cpp"}, []string{"write.cpp"}, nil)
		require.NoError(t, err)
		defer workspace.Cleanup()
		require.NoError(t, os.WriteFile(filepath.Join(workspace.WorkDir, "read.cpp"), []byte("changed"), 0o644))
		require.ErrorContains(t, workspace.Validate(), "read-only path changed")
		original, err := os.ReadFile(filepath.Join(fixture, "read.cpp"))
		require.NoError(t, err)
		require.Equal(t, "read", string(original))
	})
}

func TestScopedAgentWorkspaceRejectsGitAndOracleExposure(t *testing.T) {
	for _, path := range []string{".git/config", "bench/oracles/answer.json", "../escape"} {
		_, err := PrepareScopedAgentWorkspace(t.TempDir(), []string{path}, nil, nil)
		require.Error(t, err)
	}
}

func TestScopedAgentWorkspacePermissionsSupportUnprivilegedOuterUser(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "src", "existing.cpp"), []byte("before"), 0o644))
	workspace, err := PrepareScopedAgentWorkspace(fixture, nil, []string{"src/existing.cpp", "src/new.cpp"}, []string{".canvasbench/trajectory.json"})
	require.NoError(t, err)
	defer workspace.Cleanup()

	rootInfo, err := os.Stat(workspace.WorkDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o777), rootInfo.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Join(workspace.WorkDir, "src"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o777), dirInfo.Mode().Perm())
	fileInfo, err := os.Stat(filepath.Join(workspace.WorkDir, "src", "existing.cpp"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o666), fileInfo.Mode().Perm())
	internalInfo, err := os.Stat(filepath.Join(workspace.WorkDir, ".canvasbench"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o777), internalInfo.Mode().Perm())
}
