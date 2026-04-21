package projects

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates/project-compose.yml.tmpl
var composeTemplateFS embed.FS

// composeTemplateName is the filename of the embedded template.
const composeTemplateName = "templates/project-compose.yml.tmpl"

// TemplateData holds the values interpolated into the per-project compose
// template. It is populated by the caller from the project registry entry
// plus per-language defaults; the template itself does not know how to
// resolve overrides.
type TemplateData struct {
	// ProjectName matches Project.Name in the registry.
	ProjectName string
	// OrchURL is the orchestrator HTTP endpoint for this project.
	OrchURL string
	// Language is the project's declared language ("go", "cpp").
	Language string
	// WorkerImage is the fully-qualified worker image tag (for example
	// "drem-worker-go:latest").
	WorkerImage string
	// MergerImage is the merger container image tag.
	MergerImage string
	// OrchImage is the orchestrator container image tag. Defaults to
	// DefaultOrchImage (production distroless build); when DevMode is
	// true the caller should swap in DefaultOrchDevImage.
	OrchImage string
	// CsuiteImages maps C-Suite persona ("mike", "alex", "ross", "seth")
	// to its container image tag.
	CsuiteImages map[string]string
	// BareRepoPath is the absolute path to the project's bare repository
	// on the host, mounted into the orchestrator container.
	BareRepoPath string
	// SharedToken authenticates agentmon POST /internal/logs requests for
	// this project. Generated once per registration via NewSharedToken.
	SharedToken string
	// OrchHostPort is the host-side loopback port bound to the
	// orchestrator container's :8080. Kept unique across projects by
	// registry.AllocateOrchHostPort.
	OrchHostPort int
	// DevMode toggles the development-mode orchestrator image and a
	// read-only bind-mount of the repo source at /src, driven by
	// `drem project register --dev`.
	DevMode bool
	// RepoSourcePath is the absolute path to the orchestrator's own
	// source tree (go.mod root). Only used when DevMode is true.
	RepoSourcePath string
	// ConfigFilePath is the absolute host-side path to the per-project
	// drem.toml written by WriteProjectConfig. The compose template
	// bind-mounts it read-only into the orch container at
	// /var/lib/drem/drem.toml. Required for the direct-classifier path —
	// defaults to <homeDir>/.drem/projects/<name>/drem.toml when the
	// caller goes through projectPaths.
	ConfigFilePath string
	// HostHome is the host operator's home directory ($HOME on host).
	// Used to derive the default WorkerCredsPath when the caller does
	// not set it explicitly. Populated by applyDefaults from
	// os.UserHomeDir() when empty so Render callers don't have to know
	// about it. See plans/worker-subscription-auth.md §6 commit 6.
	HostHome string
	// WorkerCredsPath is the host path of the operator's Claude
	// subscription credentials file, passed to orch via the
	// DREM_WORKER_CREDS_PATH env var. Orch forwards it into every
	// claude-backed worker spawn as SpawnWorkerParams.CredsMount. The
	// spawner pre-checks the path exists before creating the container
	// so a missing file fails closed with a clear error. Defaults to
	// HostHome/.claude/.credentials.json when empty.
	WorkerCredsPath string
	// WorkerPromptRoot is the host directory under which orch writes
	// per-task prompt files. Passed to orch via DREM_PROMPT_ROOT_HOST
	// AND bind-mounted read-write into the orch container at the same
	// host-identical path so orch's os.WriteFile lands on a path the
	// spawner (which is a separate container) can bind-mount read-only
	// back into a worker at /home/drem/.drem/prompt.md. Defaults to
	// HostHome/.drem/projects/<ProjectName>/prompts when empty. See
	// plans/worker-prompt-delivery.md §§2, 4.
	WorkerPromptRoot string
	// CsuiteHomeRoot is the host directory that holds per-persona
	// inbox/outbox/state trees for the four csuite agents (mike, alex,
	// ross, seth). The compose template bind-mounts
	// <CsuiteHomeRoot>/<persona>/ read-write into each csuite-*
	// container at /home/drem/.drem-csuite/<persona>/ so inbox
	// messages dropped by the host reach the containerized persona and
	// state.md writes persist across restarts. Defaults to
	// HostHome/.drem-csuite when empty. Shared across projects (one
	// csuite comms tree per operator, not per project). See the csuite-
	// docker end-to-end design and plans/ for the rationale.
	CsuiteHomeRoot string
}

// Default image tags for the orchestrator.
const (
	// DefaultOrchImage is the production distroless orchestrator image.
	DefaultOrchImage = "localhost:5000/drem-orch:latest"
	// DefaultOrchDevImage is the development orchestrator image that
	// compiles /src on start.
	DefaultOrchDevImage = "localhost:5000/drem-orch-dev:latest"
	// DefaultOrchHostPort is the first host port allocated to a project's
	// orchestrator container. Subsequent projects use DefaultOrchHostPort+N.
	DefaultOrchHostPort = 8080
)

// NewSharedToken returns a freshly generated 32-byte hex-encoded token
// suitable for Project.SharedToken. It reads from crypto/rand and returns
// an error only if the underlying entropy source is unavailable.
func NewSharedToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Render executes the compose template with data and returns the rendered
// bytes. Zero-valued fields on data are filled in with sensible defaults
// (OrchImage, OrchHostPort) so that callers from older code paths do not
// produce broken compose files.
func Render(data TemplateData) ([]byte, error) {
	applyDefaults(&data)
	tmpl, err := template.ParseFS(composeTemplateFS, composeTemplateName)
	if err != nil {
		return nil, fmt.Errorf("parse compose template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute compose template: %w", err)
	}
	return buf.Bytes(), nil
}

// applyDefaults fills in unset fields on data. It is called by Render so
// every rendered compose file has a usable image and port even when the
// caller only populated the minimum set.
func applyDefaults(data *TemplateData) {
	if data.OrchImage == "" {
		if data.DevMode {
			data.OrchImage = DefaultOrchDevImage
		} else {
			data.OrchImage = DefaultOrchImage
		}
	}
	if data.OrchHostPort == 0 {
		data.OrchHostPort = DefaultOrchHostPort
	}
	// HostHome + WorkerCredsPath: derive from os.UserHomeDir on the
	// operator's host at render time. `drem project register` runs on
	// host, so $HOME here is the operator's home — not the orch
	// container's /root. This is the whole reason HostHome is a
	// template field rather than something orch introspects at
	// runtime. See plans/worker-subscription-auth.md §6 commit 6.
	if data.HostHome == "" {
		if h, err := os.UserHomeDir(); err == nil {
			data.HostHome = h
		}
	}
	if data.WorkerCredsPath == "" && data.HostHome != "" {
		data.WorkerCredsPath = filepath.Join(data.HostHome, ".claude", ".credentials.json")
	}
	// WorkerPromptRoot mirrors WorkerCredsPath's derivation pattern but
	// is per-project (one prompt dir per project, not a shared host
	// file). The path HAS to be under a host dir docker can bind-mount
	// both into orch (rw, for writing) and into each worker (ro, for
	// reading), which is why it lives under HostHome rather than inside
	// the orch container's own filesystem. See
	// plans/worker-prompt-delivery.md §2.
	if data.WorkerPromptRoot == "" && data.HostHome != "" && data.ProjectName != "" {
		data.WorkerPromptRoot = filepath.Join(
			data.HostHome, ".drem", "projects", data.ProjectName, "prompts")
	}
	// CsuiteHomeRoot is host-global (one per operator, not per project)
	// so it only depends on HostHome. Defaulted here so render callers
	// and compose.override.yml operators don't have to know the layout.
	if data.CsuiteHomeRoot == "" && data.HostHome != "" {
		data.CsuiteHomeRoot = filepath.Join(data.HostHome, ".drem-csuite")
	}
}

// WriteProjectCompose renders the template and writes it to
// $HOME/.drem/projects/<projectName>/compose.yml. Returns the absolute
// path of the written file. projectName must match data.ProjectName.
func WriteProjectCompose(projectName string, data TemplateData) (string, error) {
	return WriteProjectComposeAt("", projectName, data)
}

// WriteProjectComposeAt is WriteProjectCompose with an explicit home
// directory override for tests and for the --home-dir CLI flag. An empty
// homeDir falls back to os.UserHomeDir.
func WriteProjectComposeAt(homeDir, projectName string, data TemplateData) (string, error) {
	if projectName == "" {
		return "", errors.New("projectName is required")
	}
	if data.ProjectName == "" {
		data.ProjectName = projectName
	}
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		homeDir = h
	}
	dir := filepath.Join(homeDir, ".drem", "projects", projectName)
	// Fill in the drem.toml mount path before rendering so the compose
	// template can bind-mount it. The toml file itself is produced by
	// WriteProjectConfigAt; the paths must agree.
	if data.ConfigFilePath == "" {
		data.ConfigFilePath = filepath.Join(dir, configFilename)
	}
	rendered, err := Render(data)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create project dir %q: %w", dir, err)
	}
	// Pre-create the host-side prompts dir so the first worker spawn
	// doesn't race to `MkdirAll` inside a bind-mount source docker
	// would otherwise auto-create as root. Matches the subscription-
	// auth `~/.claude` ownership rationale in worker-base.Dockerfile.
	// Best-effort: if the path is outside this homeDir (explicit
	// override), MkdirAll on its parent may fail and that's fine —
	// the orch container still boots and the first spawn will surface
	// the real error.
	if data.WorkerPromptRoot != "" {
		_ = os.MkdirAll(data.WorkerPromptRoot, 0o755)
	}
	path := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return "", fmt.Errorf("write compose file %q: %w", path, err)
	}
	return path, nil
}
