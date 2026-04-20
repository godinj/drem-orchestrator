package projects_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/projects"
)

// TestRenderConfig_EnablesDirectClassifier asserts that the generated
// drem.toml sets [agents.classifier].direct=true and overrides the
// direct-tool endpoint to gq on drem-net. These are the two flags the
// T1 canary proved necessary inside the containerized orch (see
// plans/install-log.md iteration notes).
func TestRenderConfig_EnablesDirectClassifier(t *testing.T) {
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}
	out, err := projects.RenderConfig(data)
	require.NoError(t, err)
	s := string(out)

	require.Contains(t, s, "bare_repo_path = \"/home/dev/git/drem-orchestrator.git\"")
	require.Contains(t, s, "[agents.classifier]")
	require.Regexp(t, `direct\s*=\s*true`, s)
	require.Contains(t, s, "[direct_tool_agent]")
	require.Contains(t, s, "endpoint = \"http://gq:8090/v1/chat/completions\"")

	// Round-trip through the TOML parser so a broken template is caught
	// at test time, not at container start.
	var parsed struct {
		BareRepoPath string `toml:"bare_repo_path"`
		Project      struct {
			Language string `toml:"language"`
		} `toml:"project"`
		Agents struct {
			Classifier struct {
				Direct   bool   `toml:"direct"`
				Endpoint string `toml:"endpoint"`
				Model    string `toml:"model"`
			} `toml:"classifier"`
		} `toml:"agents"`
		DirectToolAgent struct {
			Endpoint string `toml:"endpoint"`
			Model    string `toml:"model"`
		} `toml:"direct_tool_agent"`
	}
	require.NoError(t, toml.Unmarshal(out, &parsed))
	require.Equal(t, "/home/dev/git/drem-orchestrator.git", parsed.BareRepoPath)
	require.Equal(t, "go", parsed.Project.Language)
	require.True(t, parsed.Agents.Classifier.Direct)
	require.Equal(t, "http://gq:8090/v1/chat/completions", parsed.DirectToolAgent.Endpoint)
	// The warm-classifier endpoint must round-trip so orch picks it up on
	// startup without needing DREM_CLASSIFIER_URL also set. See
	// plans/warm-direct-classifier.md §3 (Modified files).
	require.Equal(t, "http://drem-classifier:8090/classify", parsed.Agents.Classifier.Endpoint)
}

// TestRenderConfig_ContainsWarmClassifierEndpoint is a tighter substring
// check that protects the exact key/value pair against typo regressions.
// The round-trip above catches TOML-level breakage; this catches someone
// renaming the key in-flight.
func TestRenderConfig_ContainsWarmClassifierEndpoint(t *testing.T) {
	out, err := projects.RenderConfig(projects.TemplateData{
		BareRepoPath: "/tmp/x",
		Language:     projects.LanguageGo,
	})
	require.NoError(t, err)
	require.Contains(t, string(out), `endpoint = "http://drem-classifier:8090/classify"`)
}

// TestRenderConfig_RequiresBareRepoPath asserts the nil-guard on the
// one field the template cannot safely default.
func TestRenderConfig_RequiresBareRepoPath(t *testing.T) {
	_, err := projects.RenderConfig(projects.TemplateData{Language: "go"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "BareRepoPath")
}

// TestRenderConfig_RequiresLanguage asserts that the template refuses
// an empty language (it interpolates into [project].language).
func TestRenderConfig_RequiresLanguage(t *testing.T) {
	_, err := projects.RenderConfig(projects.TemplateData{BareRepoPath: "/x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Language")
}

// TestWriteProjectConfigAt_WritesAndResolvesPath verifies that the helper
// drops drem.toml in the same directory as compose.yml, at the filename
// the compose template's bind-mount expects.
func TestWriteProjectConfigAt_WritesAndResolvesPath(t *testing.T) {
	homeDir := t.TempDir()
	data := projects.TemplateData{
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}

	path, err := projects.WriteProjectConfigAt(homeDir, "drem-orchestrator", data)
	require.NoError(t, err)

	expected := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "drem.toml")
	require.Equal(t, expected, path)

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Regexp(t, `direct\s*=\s*true`, string(contents),
		"written drem.toml must enable the direct classifier")
}

// TestWriteProjectConfigAt_RejectsEmptyName covers the argument-validation
// error path.
func TestWriteProjectConfigAt_RejectsEmptyName(t *testing.T) {
	_, err := projects.WriteProjectConfigAt(t.TempDir(), "", projects.TemplateData{
		Language:     "go",
		BareRepoPath: "/x",
	})
	require.Error(t, err)
}

// TestRender_MountsDremTomlAtConfigFilePath verifies that the compose
// template bind-mounts the absolute ConfigFilePath at the orch CWD.
// Docker compose does not expand ~, so the path must be absolute.
func TestRender_MountsDremTomlAtConfigFilePath(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.ConfigFilePath = "/home/dev/.drem/projects/drem-orchestrator/drem.toml"

	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	require.Contains(t, s, "/home/dev/.drem/projects/drem-orchestrator/drem.toml:/var/lib/drem/drem.toml:ro",
		"compose must bind-mount the absolute drem.toml path at the orch CWD")
}

// TestWriteProjectComposeAt_DefaultsConfigFilePath verifies that
// WriteProjectComposeAt fills in ConfigFilePath when the caller leaves
// it blank, so the bind-mount is always present in the rendered compose.
func TestWriteProjectComposeAt_DefaultsConfigFilePath(t *testing.T) {
	homeDir := t.TempDir()
	data := projects.TemplateData{
		Language:     projects.LanguageGo,
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
	}

	composePath, err := projects.WriteProjectComposeAt(homeDir, "drem-orchestrator", data)
	require.NoError(t, err)

	contents, err := os.ReadFile(composePath)
	require.NoError(t, err)
	expected := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "drem.toml")
	require.Contains(t, string(contents), expected+":/var/lib/drem/drem.toml:ro")
}
