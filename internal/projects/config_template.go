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

//go:embed templates/project-drem.toml.tmpl
var configTemplateFS embed.FS

// configTemplateName is the filename of the embedded drem.toml template.
const configTemplateName = "templates/project-drem.toml.tmpl"

// configFilename is the name of the per-project orchestrator config file
// written into ~/.drem/projects/<name>/. It mirrors the in-container
// filename (orch reads drem.toml from its CWD).
const configFilename = "drem.toml"

// RenderConfig executes the drem.toml template with data and returns the
// rendered bytes. Only BareRepoPath, Language, and ProjectName are read;
// other TemplateData fields are ignored. The rendered file enables
// direct-classifier and points the direct-tool endpoint at gq on drem-net,
// which are the correct defaults for a containerized deployment.
func RenderConfig(data TemplateData) ([]byte, error) {
	if data.BareRepoPath == "" {
		return nil, errors.New("RenderConfig: BareRepoPath is required")
	}
	if data.Language == "" {
		return nil, errors.New("RenderConfig: Language is required")
	}
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
	if data.ProjectName == "" {
		data.ProjectName = projectName
	}
	rendered, err := RenderConfig(data)
	if err != nil {
		return "", err
	}
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		homeDir = h
	}
	dir := filepath.Join(homeDir, ".drem", "projects", projectName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create project dir %q: %w", dir, err)
	}
	path := filepath.Join(dir, configFilename)
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return "", fmt.Errorf("write drem.toml %q: %w", path, err)
	}
	return path, nil
}
