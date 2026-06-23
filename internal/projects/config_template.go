package projects

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"text/template"
)

//go:embed templates/project-drem.toml.tmpl
var configTemplateFS embed.FS

// configTemplateName is the filename of the embedded drem.toml template.
const configTemplateName = "templates/project-drem.toml.tmpl"

// configFilename is the name of the per-project orchestrator config file
// written into ~/.drem/projects/<name>/. It mirrors the in-container
// filename (orch reads drem.toml from its CWD).
const configFilename = "drem.toml"

// RenderConfig executes the drem.toml template with data and returns the
// rendered bytes. BareRepoPath and Language are required; language-derived
// defaults are filled before rendering. The rendered file enables
// direct-classifier and points the direct-tool endpoint at gq on drem-net,
// which are the correct defaults for a containerized deployment.
func RenderConfig(data TemplateData) ([]byte, error) {
	if data.BareRepoPath == "" {
		return nil, errors.New("RenderConfig: BareRepoPath is required")
	}
	if data.Language == "" {
		return nil, errors.New("RenderConfig: Language is required")
	}
	data = compileTemplateDefaults(data, "", "")
	tmpl, err := template.ParseFS(configTemplateFS, configTemplateName)
	if err != nil {
		return nil, fmt.Errorf("parse drem.toml template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute drem.toml template: %w", err)
	}
	return buf.Bytes(), nil
}

// WriteProjectConfig renders the drem.toml template and writes it to
// $HOME/.drem/projects/<projectName>/drem.toml. Returns the absolute path
// of the written file. Callers that pass an explicit home directory
// should use WriteProjectConfigAt instead.
func WriteProjectConfig(projectName string, data TemplateData) (string, error) {
	return WriteProjectConfigAt("", projectName, data)
}

// WriteProjectConfigAt is WriteProjectConfig with an explicit home
// directory override for tests and for the --home-dir CLI flag. An empty
// homeDir falls back to os.UserHomeDir.
func WriteProjectConfigAt(homeDir, projectName string, data TemplateData) (string, error) {
	if projectName == "" {
		return "", errors.New("projectName is required")
	}
	spec, err := compileProjectDeploymentSpec(homeDir, projectName, data)
	if err != nil {
		return "", err
	}
	rendered, err := RenderConfig(spec.TemplateData)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(spec.ProjectDir, 0o755); err != nil {
		return "", fmt.Errorf("create project dir %q: %w", spec.ProjectDir, err)
	}
	if err := os.WriteFile(spec.ConfigPath, rendered, 0o644); err != nil {
		return "", fmt.Errorf("write drem.toml %q: %w", spec.ConfigPath, err)
	}
	return spec.ConfigPath, nil
}
