// Package projectconfig reads the narrow host-side configuration needed by
// HTTP-only clients. It deliberately does not expose project mutation or pull
// the internal project/deployment package into dremctl.
package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

type Project struct {
	Name         string `toml:"name"`
	BareRepoPath string `toml:"bare_repo_path"`
	OrchURL      string `toml:"orch_url"`
}

type registryFile struct {
	Projects []Project `toml:"projects"`
}

type composeFile struct {
	Services struct {
		Orch struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"orch"`
	} `yaml:"services"`
}

// Load returns one registered project plus the mutation token retained in its
// generated compose state. The token is never formatted into errors.
func Load(home, name string) (Project, string, error) {
	registryPath := filepath.Join(home, ".drem", "projects.toml")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return Project{}, "", fmt.Errorf("read registry %s: %w", registryPath, err)
	}
	var registry registryFile
	if err := toml.Unmarshal(raw, &registry); err != nil {
		return Project{}, "", fmt.Errorf("parse registry %s: %w", registryPath, err)
	}
	var project Project
	found := false
	for i := range registry.Projects {
		if registry.Projects[i].Name == name {
			project = registry.Projects[i]
			found = true
			break
		}
	}
	if !found {
		return Project{}, "", fmt.Errorf("project %q is not registered in %s", name, registryPath)
	}
	composePath := filepath.Join(home, ".drem", "projects", name, "compose.yml")
	composeRaw, err := os.ReadFile(composePath)
	if err != nil {
		return project, "", fmt.Errorf("read project compose %s: %w", composePath, err)
	}
	var compose composeFile
	if err := yaml.Unmarshal(composeRaw, &compose); err != nil {
		return project, "", fmt.Errorf("parse project compose %s: %w", composePath, err)
	}
	return project, compose.Services.Orch.Environment["DREM_AGENTMON_TOKEN"], nil
}
