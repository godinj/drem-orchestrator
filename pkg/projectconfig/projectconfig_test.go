package projectconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRegisteredProjectAndToken(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".drem", "projects", "canvas"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".drem", "projects.toml"), []byte(`
[[projects]]
name = "canvas"
bare_repo_path = "/tmp/canvas.git"
orch_url = "http://127.0.0.1:8080"
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".drem", "projects", "canvas", "compose.yml"), []byte(`
services:
  orch:
    environment:
      DREM_AGENTMON_TOKEN: secret-value
`), 0o600))

	project, token, err := Load(home, "canvas")
	require.NoError(t, err)
	require.Equal(t, "/tmp/canvas.git", project.BareRepoPath)
	require.Equal(t, "http://127.0.0.1:8080", project.OrchURL)
	require.Equal(t, "secret-value", token)
}
