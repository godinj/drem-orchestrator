package projects_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/godinj/drem-orchestrator/internal/projects"
)

// TestRender_ProducesNonEmptyYAML asserts that Render returns a non-empty
// payload with the project name interpolated.
func TestRender_ProducesNonEmptyYAML(t *testing.T) {
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		OrchURL:      "http://localhost:8080",
		Language:     projects.LanguageGo,
		WorkerImage:  "drem-worker-go:latest",
		MergerImage:  "drem-merger:latest",
		CsuiteImages: map[string]string{"mike": "drem-csuite-mike:latest"},
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
		SharedToken:  "token-123",
	}
	out, err := projects.Render(data)
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Contains(t, string(out), "drem-orchestrator")
}

// TestWriteProjectComposeAt_CreatesValidYAML verifies that the helper
// creates the expected directory tree and writes a file whose contents
// parse as valid YAML.
func TestWriteProjectComposeAt_CreatesValidYAML(t *testing.T) {
	homeDir := t.TempDir()
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		OrchURL:      "http://localhost:8080",
		Language:     projects.LanguageGo,
		WorkerImage:  "drem-worker-go:latest",
		MergerImage:  "drem-merger:latest",
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
		SharedToken:  "token-123",
	}

	path, err := projects.WriteProjectComposeAt(homeDir, "drem-orchestrator", data)
	require.NoError(t, err)

	expectedPath := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "compose.yml")
	require.Equal(t, expectedPath, path)

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(contents), "drem-orchestrator"))

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(contents, &parsed), "compose output must be valid YAML")
	require.Contains(t, parsed, "services")
}

// TestWriteProjectComposeAt_RejectsEmptyName covers the error path for an
// empty project name.
func TestWriteProjectComposeAt_RejectsEmptyName(t *testing.T) {
	_, err := projects.WriteProjectComposeAt(t.TempDir(), "", projects.TemplateData{})
	require.Error(t, err)
}
