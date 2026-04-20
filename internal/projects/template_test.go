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

// fullTemplateData returns a TemplateData whose fields cover every
// template branch (C-Suite personas, language, ports, etc.). The caller
// can override fields as needed.
func fullTemplateData(name, lang string) projects.TemplateData {
	workerImage := "localhost:5000/drem-worker-go:latest"
	if lang == projects.LanguageCpp {
		workerImage = "localhost:5000/drem-worker-cpp:latest"
	}
	return projects.TemplateData{
		ProjectName:  name,
		OrchURL:      "http://localhost:8080",
		Language:     lang,
		WorkerImage:  workerImage,
		MergerImage:  "localhost:5000/drem-merger:latest",
		OrchImage:    projects.DefaultOrchImage,
		OrchHostPort: projects.DefaultOrchHostPort,
		CsuiteImages: map[string]string{
			"mike": "localhost:5000/drem-csuite-mike:latest",
			"alex": "localhost:5000/drem-csuite-alex:latest",
			"ross": "localhost:5000/drem-csuite-ross:latest",
			"seth": "localhost:5000/drem-csuite-seth:latest",
		},
		BareRepoPath: "/home/dev/git/" + name + ".git",
		SharedToken:  "abc123def456",
	}
}

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

// TestRender_GoTemplateFull verifies that a Go-language project renders
// every expected service (orch, agentmon, csuite-watcher, four C-Suite
// personas) with the shared token plumbed to orch + agentmon and the
// worker image tagged for Go. The merger image is referenced by an
// image-prime stub (merger-template, profiles: ["never"]) rather than a
// running pool — drem-merger is a per-task one-shot binary; long-lived
// replicas crash-loop on missing flags. See plans/merger-spawn-on-demand.md.
func TestRender_GoTemplateFull(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	for _, service := range []string{
		"orch:", "agentmon:", "csuite-watcher:",
		"csuite-mike:", "csuite-alex:", "csuite-ross:", "csuite-seth:",
	} {
		require.Contains(t, s, service, "missing service %q", service)
	}
	require.Contains(t, s, "drem-worker-go")
	require.NotContains(t, s, "drem-worker-cpp")
	// The merger pool must NOT be present as a running service.
	require.NotContains(t, s, "merger-pool:",
		"merger-pool service must not be present; drem-merger is one-shot, see plans/merger-spawn-on-demand.md")

	// SharedToken must reach orch and agentmon, but never a C-Suite container.
	var parsed struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	require.Equal(t, data.SharedToken, parsed.Services["orch"].Environment["DREM_AGENTMON_TOKEN"])
	// DREM_ORCH_URL must be present on orch so dispatchMerge has a
	// self-URL to pass to spawned merger containers. See
	// plans/merger-spawn-on-demand-impl.md.
	require.Equal(t, "http://orch:8080", parsed.Services["orch"].Environment["DREM_ORCH_URL"])
	// DREM_CLASSIFIER_URL must route orch to the warm drem-classifier
	// container on drem-net; see plans/warm-direct-classifier.md §3.
	require.Equal(t, "http://drem-classifier:8090/classify", parsed.Services["orch"].Environment["DREM_CLASSIFIER_URL"])
	require.Equal(t, data.SharedToken, parsed.Services["agentmon"].Environment["DREM_AGENTMON_TOKEN"])
	require.Empty(t, parsed.Services["csuite-mike"].Environment["DREM_AGENTMON_TOKEN"])
	require.Empty(t, parsed.Services["csuite-watcher"].Environment["DREM_AGENTMON_TOKEN"])
	// merger-pool must not exist; the merger-template stub exists only
	// so `docker compose pull` primes the image and is gated behind
	// profiles: ["never"].
	require.NotContains(t, parsed.Services, "merger-pool")
	require.Contains(t, parsed.Services, "merger-template")
	require.Equal(t, data.MergerImage, parsed.Services["merger-template"].Image)
}

// TestRender_CppTemplateSwapsWorkerImage verifies that a C++ project
// pins the drem-worker-cpp image tag.
func TestRender_CppTemplateSwapsWorkerImage(t *testing.T) {
	data := fullTemplateData("drem-canvas", projects.LanguageCpp)
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "drem-worker-cpp")
	require.NotContains(t, s, "drem-worker-go")
}

// TestRender_BareRepoMountIsPathIdentical asserts that the bare repo is
// bind-mounted at its HOST-identical path (host:/home/op/foo.git →
// container:/home/op/foo.git) rather than at a fixed target like /bare.
// The orchestrator passes DREM_BARE_REPO as a host path; mounting it at a
// different target inside the container used to break `git worktree list`
// because the env var and the mount target disagreed.
func TestRender_BareRepoMountIsPathIdentical(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.BareRepoPath = "/home/dev/git/drem-orchestrator.git"

	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)

	require.Contains(t, s, "/home/dev/git/drem-orchestrator.git:/home/dev/git/drem-orchestrator.git:rw",
		"bare repo must bind-mount at the host-identical path")
	require.NotContains(t, s, ":/bare:rw",
		"the legacy /bare target must not regress — it breaks git inside orch")
}

// TestRender_DevModeBindsSource asserts that DevMode=true emits a
// /src bind-mount under the orchestrator service.
func TestRender_DevModeBindsSource(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.DevMode = true
	data.RepoSourcePath = "/home/dev/git/drem-orchestrator.git/master"
	data.OrchImage = projects.DefaultOrchDevImage

	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, projects.DefaultOrchDevImage)
	require.Contains(t, s, "/home/dev/git/drem-orchestrator.git/master:/src:ro")

	// Production mode should NOT emit /src.
	data.DevMode = false
	data.OrchImage = projects.DefaultOrchImage
	out, err = projects.Render(data)
	require.NoError(t, err)
	require.NotContains(t, string(out), ":/src:ro")
}

// TestRender_Defaults confirms that zero-valued OrchImage and OrchHostPort
// get defaulted during Render so callers from older code paths do not
// produce broken compose files.
func TestRender_Defaults(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.OrchImage = ""
	data.OrchHostPort = 0
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, projects.DefaultOrchImage)
	require.Contains(t, s, "127.0.0.1:8080:8080")
}

// TestRender_ParsesAsYAML exercises yaml.Unmarshal on the full rendering
// to catch quoting/indent bugs the narrower tests would miss.
func TestRender_ParsesAsYAML(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	services, ok := parsed["services"].(map[string]any)
	require.True(t, ok, "services must be a map")
	for _, name := range []string{"orch", "agentmon", "csuite-watcher",
		"csuite-mike", "csuite-alex", "csuite-ross", "csuite-seth"} {
		require.Contains(t, services, name)
	}
	// merger-pool was removed because drem-merger is a per-task one-shot
	// binary; long-lived replicas crash-loop on missing required flags.
	// See plans/merger-spawn-on-demand.md.
	require.NotContains(t, services, "merger-pool")
}

// TestNewSharedToken asserts that NewSharedToken produces a 64-character
// hex string (32 raw bytes) and that back-to-back calls do not repeat.
func TestNewSharedToken(t *testing.T) {
	a, err := projects.NewSharedToken()
	require.NoError(t, err)
	require.Len(t, a, 64)
	b, err := projects.NewSharedToken()
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}
