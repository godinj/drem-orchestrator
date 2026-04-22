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

// TestRender_OrchDoesNotForwardAnthropicAPIKey asserts the compose
// template does NOT declare an ANTHROPIC_API_KEY env passthrough on orch.
// Per plans/warm-planner-pivot.md §§1, 8, subscription auth is handled
// inside the drem-planner container via a bind-mounted credentials file;
// orch never needs the API key and forwarding it would bypass the
// subscription-only policy.
func TestRender_OrchDoesNotForwardAnthropicAPIKey(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	require.NotContains(t, s, "ANTHROPIC_API_KEY",
		"compose must NOT forward ANTHROPIC_API_KEY (subscription-only auth per plan §1)")
}

// TestRender_WorkerCredsPathIsWired asserts the orch env declares
// DREM_WORKER_CREDS_PATH with a non-empty value. Orch forwards this
// into every claude-backed worker spawn as SpawnWorkerParams.CredsMount.
// See plans/worker-subscription-auth.md §6 commit 6.
func TestRender_WorkerCredsPathIsWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	require.Contains(t, s, "DREM_WORKER_CREDS_PATH",
		"orch env must declare DREM_WORKER_CREDS_PATH so it can CredsMount claude workers")
	require.Contains(t, s, "/home/operator/.claude/.credentials.json",
		"the rendered path must derive from HostHome")

	// Parse the YAML and assert the value shape explicitly, independent
	// of the legacy-regression ANTHROPIC_API_KEY check.
	var parsed struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	got := parsed.Services["orch"].Environment["DREM_WORKER_CREDS_PATH"]
	require.Equal(t, "/home/operator/.claude/.credentials.json", got)
	// The negative check from TestRender_OrchDoesNotForwardAnthropicAPIKey
	// still holds under the new key.
	require.NotContains(t, s, "ANTHROPIC_API_KEY")
}

// TestRender_WorkerCredsPathDefaultsFromHostHome asserts applyDefaults
// fills in WorkerCredsPath from HostHome when both are zero-value.
// HostHome itself is populated from os.UserHomeDir — we set it
// explicitly here to avoid a dependency on $HOME in test.
func TestRender_WorkerCredsPathDefaultsFromHostHome(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/root"
	// Caller leaves WorkerCredsPath zero; applyDefaults fills it.
	out, err := projects.Render(data)
	require.NoError(t, err)
	require.Contains(t, string(out), "/root/.claude/.credentials.json")
}

// TestRender_CsuiteWatcherTokenPathIsWired asserts every persona
// container bind-mounts the operator's csuite-watcher token file at
// the same /run/secrets/csuite-watcher-token path, and that each
// persona's environment sets CSUITE_WATCHER_TOKEN_FILE pointing at
// that mount. Scoreboard item 33: the host-shell-env inherit path
// (CSUITE_WATCHER_TOKEN: with no value) was silently dropping the
// token on compose-up when the operator forgot to export it; the
// file-mount path is the canonical source of truth.
func TestRender_CsuiteWatcherTokenPathIsWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)

	// Default derives from HostHome.
	const wantHostPath = "/home/operator/.drem/csuite-watcher.token"
	require.Contains(t, s, wantHostPath,
		"compose must bind-mount the csuite-watcher token file under HostHome")
	require.Contains(t, s, "/run/secrets/csuite-watcher-token",
		"compose must mount the token file at the persona binary's expected path")
	require.Contains(t, s, "CSUITE_WATCHER_TOKEN_FILE",
		"persona env must declare CSUITE_WATCHER_TOKEN_FILE so the binary can fall back to file read")

	// Parse and cross-check each persona.
	var parsed struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Volumes     []string          `yaml:"volumes"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	for _, p := range []string{"csuite-mike", "csuite-alex", "csuite-ross", "csuite-seth"} {
		svc, ok := parsed.Services[p]
		require.True(t, ok, "service %s missing from rendered compose", p)
		require.Equal(t, "/run/secrets/csuite-watcher-token",
			svc.Environment["CSUITE_WATCHER_TOKEN_FILE"],
			"%s must set CSUITE_WATCHER_TOKEN_FILE to the mount path", p)
		// Find the token mount line among volumes.
		var found bool
		for _, v := range svc.Volumes {
			if strings.Contains(v, "/run/secrets/csuite-watcher-token") {
				require.Contains(t, v, wantHostPath,
					"%s token mount must bind-mount %s", p, wantHostPath)
				found = true
				break
			}
		}
		require.True(t, found, "%s missing csuite-watcher token mount", p)
	}
}

// TestRender_CsuiteWatcherTokenPathExplicitOverride asserts a
// caller-supplied value overrides the HostHome-derived default.
func TestRender_CsuiteWatcherTokenPathExplicitOverride(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	data.CsuiteWatcherTokenPath = "/etc/drem/csuite-watcher.token"
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "/etc/drem/csuite-watcher.token")
}

// TestRender_WorkerCredsPathExplicitOverride asserts a caller-supplied
// WorkerCredsPath wins over the HostHome-derived default.
func TestRender_WorkerCredsPathExplicitOverride(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	data.WorkerCredsPath = "/etc/drem/shared-creds/.credentials.json"
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "/etc/drem/shared-creds/.credentials.json")
	require.NotContains(t, s, "/home/operator/.claude/.credentials.json",
		"explicit WorkerCredsPath must override the HostHome default")
}

// TestRender_WorkerPromptRootIsWired asserts the orch env declares
// DREM_PROMPT_ROOT_HOST AND bind-mounts the same host-identical path
// read-write on orch so os.WriteFile in orch and os.Stat in spawner
// see the same bytes. See plans/worker-prompt-delivery.md §§2, 4.
func TestRender_WorkerPromptRootIsWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	require.Contains(t, s, "DREM_PROMPT_ROOT_HOST",
		"orch env must declare DREM_PROMPT_ROOT_HOST so buildSpawnContext resolves it")
	expectedRoot := "/home/operator/.drem/projects/drem-orchestrator/prompts"
	require.Contains(t, s, expectedRoot,
		"rendered root must derive from HostHome + ProjectName")

	// Parse and assert the env value explicitly.
	var parsed struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Volumes     []string          `yaml:"volumes"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	got := parsed.Services["orch"].Environment["DREM_PROMPT_ROOT_HOST"]
	require.Equal(t, expectedRoot, got)

	// The orch service must also bind-mount the same host path
	// read-write at the host-identical target so orch's writes are
	// visible to the spawner's pre-stat via the shared host filesystem.
	foundMount := false
	expectedMount := expectedRoot + ":" + expectedRoot + ":rw"
	for _, v := range parsed.Services["orch"].Volumes {
		if v == expectedMount {
			foundMount = true
			break
		}
	}
	require.True(t, foundMount,
		"orch must bind-mount WorkerPromptRoot at its host-identical path, rw; volumes=%v",
		parsed.Services["orch"].Volumes)
}

// TestRender_WorkerPromptRootDefaultsFromHostHome asserts applyDefaults
// fills in WorkerPromptRoot from HostHome + ProjectName when both are
// zero-value at the caller. See plans/worker-prompt-delivery.md §4.
func TestRender_WorkerPromptRootDefaultsFromHostHome(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/root"
	data.WorkerPromptRoot = "" // explicit
	out, err := projects.Render(data)
	require.NoError(t, err)
	require.Contains(t, string(out),
		"/root/.drem/projects/drem-orchestrator/prompts",
		"default WorkerPromptRoot must derive from HostHome + ProjectName")
}

// TestRender_WorkerPromptRootExplicitOverride asserts a caller-supplied
// WorkerPromptRoot wins over the HostHome-derived default — necessary
// for operators on unusual host layouts (shared-tmp, custom drem home).
func TestRender_WorkerPromptRootExplicitOverride(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	data.WorkerPromptRoot = "/var/drem/prompts/drem-orchestrator"
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "/var/drem/prompts/drem-orchestrator")
	require.NotContains(t, s, "/home/operator/.drem/projects/drem-orchestrator/prompts",
		"explicit WorkerPromptRoot must override the HostHome default")
}

// TestRender_PlannerURLIsWired asserts the orch env declares
// DREM_PLANNER_URL pointing at the warm drem-planner container. This is
// the replacement for the deleted planner-template stub: orch now POSTs
// to a long-lived planner container on drem-net.
func TestRender_PlannerURLIsWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	s := string(out)
	require.Contains(t, s, "DREM_PLANNER_URL",
		"orch env must declare DREM_PLANNER_URL so it routes plan jobs to the warm planner")
	require.Contains(t, s, "http://drem-planner:8090/plan",
		"DREM_PLANNER_URL must point at the warm drem-planner container")
}

// TestRender_NoPlannerTemplateStub: the planner-template profiles:[never]
// stub is removed because drem-planner is a long-lived service in
// deploy/compose/global.yml, not a per-task spawn. A lingering stub
// would regress the pivot by re-introducing an image-prime entry.
func TestRender_NoPlannerTemplateStub(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]any `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	require.NotContains(t, parsed.Services, "planner-template",
		"planner-template stub must be removed; see plans/warm-planner-pivot.md §4")
}

// TestRender_CsuiteHomeMountsAreWired asserts that each csuite-*
// service bind-mounts two host paths: the operator's Claude
// subscription credentials (read-only) and the per-persona
// inbox/outbox/state tree under <CsuiteHomeRoot>/<persona>
// (read-write). Without either mount the containerized persona
// cannot authenticate OR receive inbox messages. See CLAUDE.md
// (subscription-only) and the csuite-docker end-to-end plan.
func TestRender_CsuiteHomeMountsAreWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))

	for _, persona := range []string{"mike", "alex", "ross", "seth"} {
		svc := "csuite-" + persona
		creds := "/home/operator/.claude/.credentials.json:" +
			"/home/drem/.claude/.credentials.json:ro"
		home := "/home/operator/.drem-csuite/" + persona +
			":/home/drem/.drem-csuite/" + persona + ":rw"

		require.Contains(t, parsed.Services[svc].Volumes, creds,
			"%s must bind-mount operator's Claude credentials read-only "+
				"(subscription-only auth — no auth tokens, no API keys); "+
				"volumes=%v", svc, parsed.Services[svc].Volumes)
		require.Contains(t, parsed.Services[svc].Volumes, home,
			"%s must bind-mount <CsuiteHomeRoot>/%s at /home/drem/.drem-csuite/%s "+
				"read-write so inbox messages reach the container and "+
				"state.md persists across restarts; volumes=%v",
			svc, persona, persona, parsed.Services[svc].Volumes)
	}
}

// TestRender_CsuiteHomeRootDefaultsFromHostHome asserts applyDefaults
// fills in CsuiteHomeRoot as <HostHome>/.drem-csuite when the caller
// leaves it zero-value. The csuite comms tree is host-global (one per
// operator, not per project), so the default only depends on HostHome.
func TestRender_CsuiteHomeRootDefaultsFromHostHome(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/root"
	data.CsuiteHomeRoot = "" // explicit

	out, err := projects.Render(data)
	require.NoError(t, err)
	require.Contains(t, string(out),
		"/root/.drem-csuite/seth:/home/drem/.drem-csuite/seth:rw",
		"default CsuiteHomeRoot must derive from HostHome")
}

// TestRender_CsuiteHomeRootExplicitOverride asserts a caller-supplied
// CsuiteHomeRoot wins over the HostHome-derived default — necessary
// for operators who run csuite off a shared/NFS path distinct from
// their home.
func TestRender_CsuiteHomeRootExplicitOverride(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	data.CsuiteHomeRoot = "/srv/drem-csuite"

	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s,
		"/srv/drem-csuite/seth:/home/drem/.drem-csuite/seth:rw")
	require.NotContains(t, s,
		"/home/operator/.drem-csuite/",
		"explicit CsuiteHomeRoot must override the HostHome default")
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

// TestRender_CsuiteWatcherRoutingMountsAreWired asserts that
// csuite-watcher gets the shared /csuite/ mount and the private
// /var/lib/watcher/ named-volume mount required by the outbox-
// routing MVP (plans/csuite-watcher-outbox-routing.md §4). Without
// the first, the watcher cannot see outbox files or write to
// destination inboxes; without the second, the delivery ledger
// can't persist across container recreation.
func TestRender_CsuiteWatcherRoutingMountsAreWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))

	watcher := parsed.Services["csuite-watcher"]
	require.Contains(t, watcher.Volumes,
		"/home/operator/.drem-csuite:/csuite:rw",
		"watcher must bind-mount the csuite home root at /csuite so it "+
			"can route outbox -> inbox files; volumes=%v", watcher.Volumes)
	require.Contains(t, watcher.Volumes,
		"drem-drem-orchestrator-csuite-watcher-data:/var/lib/watcher",
		"watcher must mount its private named volume at /var/lib/watcher "+
			"for the delivery ledger; volumes=%v", watcher.Volumes)
	require.Contains(t, parsed.Volumes,
		"drem-drem-orchestrator-csuite-watcher-data",
		"top-level volumes block must declare the watcher-data named volume")
}

// TestRender_CsuiteWatcherTokenEnvIsDeclared asserts the compose
// template declares CSUITE_WATCHER_TOKEN as an env key on the
// watcher service (value unset, inherits from host shell at compose
// up). Without the key, docker compose does not propagate the host
// env var and the watcher rejects every /deliver request.
func TestRender_CsuiteWatcherTokenEnvIsDeclared(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))

	env := parsed.Services["csuite-watcher"].Environment
	require.Contains(t, env, "CSUITE_WATCHER_TOKEN",
		"watcher env must declare CSUITE_WATCHER_TOKEN for host-shell "+
			"passthrough; env=%v", env)
	require.Contains(t, env, "CSUITE_WATCHER_DB_PATH",
		"watcher env must declare CSUITE_WATCHER_DB_PATH pointing at "+
			"the named-volume mount; env=%v", env)
	require.Equal(t, "/var/lib/watcher/deliveries.db",
		env["CSUITE_WATCHER_DB_PATH"])
}

// TestRender_HostDataDirBindMountIsWired asserts the orch service
// bind-mounts HostDataDir onto /var/lib/drem read-write, replacing
// the pre-pivot `drem-<project>-db` named volume. Host bind-mount
// lets the operator inspect drem.db (plus its WAL/shm sidecars)
// directly with sqlite3/du/cp — no `docker run --rm -v <vol>:/src`
// detour. See plans/orch-db-host-access-impl.md (Option A).
func TestRender_HostDataDirBindMountIsWired(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))

	expectedMount := "/home/operator/.drem/projects/drem-orchestrator/data:/var/lib/drem:rw"
	require.Contains(t, parsed.Services["orch"].Volumes, expectedMount,
		"orch must bind-mount HostDataDir at /var/lib/drem read-write; "+
			"volumes=%v", parsed.Services["orch"].Volumes)

	// The pre-pivot named-volume reference on the orch service MUST be
	// gone — if it stayed, docker would mount the (empty, on a fresh
	// project) named volume over our bind-mount and mask the operator's
	// host tree.
	for _, v := range parsed.Services["orch"].Volumes {
		require.NotEqual(t, "drem-drem-orchestrator-db:/var/lib/drem", v,
			"orch must no longer reference the pre-pivot named db volume; "+
				"volumes=%v", parsed.Services["orch"].Volumes)
	}

	// The prompts named volume inside /var/lib/drem/prompts must
	// still stack on top of the host bind-mount — it's a separate
	// storage tier the compose template has carried since inception.
	require.Contains(t, parsed.Services["orch"].Volumes,
		"drem-drem-orchestrator-prompts:/var/lib/drem/prompts",
		"prompts named volume must stay wired; volumes=%v",
		parsed.Services["orch"].Volumes)
}

// TestRender_HostDataDirDefaultsFromHostHome asserts applyDefaults
// fills in HostDataDir as <HostHome>/.drem/projects/<ProjectName>/data
// when the caller leaves it zero-value. Mirrors the WorkerPromptRoot
// defaulting pattern so every per-project host-side tree lives under
// one predictable root.
func TestRender_HostDataDirDefaultsFromHostHome(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/root"
	data.HostDataDir = "" // explicit

	out, err := projects.Render(data)
	require.NoError(t, err)
	require.Contains(t, string(out),
		"/root/.drem/projects/drem-orchestrator/data:/var/lib/drem:rw",
		"default HostDataDir must derive from HostHome + ProjectName")
}

// TestRender_HostDataDirExplicitOverride asserts a caller-supplied
// HostDataDir wins over the HostHome-derived default — necessary for
// operators on NFS / dedicated SSD / encrypted-volume host layouts.
func TestRender_HostDataDirExplicitOverride(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	data.HostHome = "/home/operator"
	data.HostDataDir = "/srv/drem-state/drem-orchestrator"
	out, err := projects.Render(data)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "/srv/drem-state/drem-orchestrator:/var/lib/drem:rw")
	require.NotContains(t, s,
		"/home/operator/.drem/projects/drem-orchestrator/data:/var/lib/drem:rw",
		"explicit HostDataDir must override the HostHome default")
}

// TestWriteProjectComposeAt_PrecreatesHostDataDir asserts the helper
// creates the host-side data dir on disk so docker's auto-create as
// root doesn't race the first `docker compose up`. Best-effort Chown
// failures are not fatal; the MkdirAll itself must land.
func TestWriteProjectComposeAt_PrecreatesHostDataDir(t *testing.T) {
	homeDir := t.TempDir()
	data := projects.TemplateData{
		ProjectName:  "drem-orchestrator",
		OrchURL:      "http://localhost:8080",
		Language:     projects.LanguageGo,
		WorkerImage:  "drem-worker-go:latest",
		MergerImage:  "drem-merger:latest",
		BareRepoPath: "/home/dev/git/drem-orchestrator.git",
		SharedToken:  "token-123",
		HostHome:     homeDir,
	}
	_, err := projects.WriteProjectComposeAt(homeDir, "drem-orchestrator", data)
	require.NoError(t, err)

	dataDir := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "data")
	info, err := os.Stat(dataDir)
	require.NoError(t, err, "WriteProjectComposeAt must pre-create HostDataDir")
	require.True(t, info.IsDir(), "HostDataDir must be a directory")
}

// TestRender_PersonaSignalEnvIsDeclared asserts each persona service
// declares both CSUITE_WATCHER_TOKEN (host-shell passthrough) and
// CSUITE_SIGNAL_ENDPOINT (pointing at the watcher's in-network name).
// Without these, post-write signaling is disabled and every reply
// waits for a rescan pass before reaching its addressee — the
// regression Kyle and the operator explicitly called out in the
// outbox-routing plan §7b.
func TestRender_PersonaSignalEnvIsDeclared(t *testing.T) {
	data := fullTemplateData("drem-orchestrator", projects.LanguageGo)
	out, err := projects.Render(data)
	require.NoError(t, err)

	var parsed struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))

	for _, persona := range []string{"mike", "alex", "ross", "seth"} {
		svc := "csuite-" + persona
		env := parsed.Services[svc].Environment
		require.Contains(t, env, "CSUITE_WATCHER_TOKEN",
			"%s env must declare CSUITE_WATCHER_TOKEN; env=%v", svc, env)
		require.Contains(t, env, "CSUITE_SIGNAL_ENDPOINT",
			"%s env must declare CSUITE_SIGNAL_ENDPOINT; env=%v", svc, env)
		require.Equal(t,
			"http://csuite-watcher:8090/deliver",
			env["CSUITE_SIGNAL_ENDPOINT"],
			"%s CSUITE_SIGNAL_ENDPOINT must point at the watcher's in-network "+
				"drem-net DNS name + /deliver", svc)
	}
}
