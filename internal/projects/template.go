package projects

import (
	"bytes"
	"embed"
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
	// CsuiteImages maps C-Suite persona ("mike", "alex", "ross", "seth")
	// to its container image tag.
	CsuiteImages map[string]string
	// BareRepoPath is the absolute path to the project's bare repository
	// on the host, mounted into the orchestrator container.
	BareRepoPath string
	// SharedToken authenticates agentmon POST /internal/logs requests for
	// this project. Generated once per registration.
	SharedToken string
}

// Render executes the compose template with data and returns the rendered
// bytes.
func Render(data TemplateData) ([]byte, error) {
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
