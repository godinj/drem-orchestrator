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
}

// WriteProjectCompose renders the template and writes it to
// $HOME/.drem/projects/<projectName>/compose.yml. Returns the absolute
// path of the written file. projectName must match data.ProjectName.
func WriteProjectCompose(projectName string, data TemplateData) (string, error) {
	if projectName == "" {
		return "", errors.New("projectName is required")
	}
	if data.ProjectName == "" {
		data.ProjectName = projectName
	}

	rendered, err := Render(data)
	if err != nil {
		return "", err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".drem", "projects", projectName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create project dir %q: %w", dir, err)
	}
	path := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return "", fmt.Errorf("write compose file %q: %w", path, err)
	}
	return path, nil
}

// WriteProjectComposeAt is WriteProjectCompose but with an explicit home
// directory override for tests and for the --home-dir CLI flag.
func WriteProjectComposeAt(homeDir, projectName string, data TemplateData) (string, error) {
	if projectName == "" {
		return "", errors.New("projectName is required")
	}
	if homeDir == "" {
		return WriteProjectCompose(projectName, data)
	}
	if data.ProjectName == "" {
		data.ProjectName = projectName
	}

	rendered, err := Render(data)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(homeDir, ".drem", "projects", projectName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create project dir %q: %w", dir, err)
	}
	path := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return "", fmt.Errorf("write compose file %q: %w", path, err)
	}
	return path, nil
}
