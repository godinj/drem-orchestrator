package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfig_ProjectSection_Language verifies that drem.toml's new
// [project] section decodes correctly into Config.Project, which the
// spawner-routed agent path in internal/agent consults.
func TestLoadConfig_ProjectSection_Language(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")
	data := []byte(`
[project]
  language = "cpp"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Project.Language != "cpp" {
		t.Errorf("Project.Language = %q, want cpp", cfg.Project.Language)
	}
	if cfg.EffectiveProjectLanguage() != "cpp" {
		t.Errorf("EffectiveProjectLanguage() = %q, want cpp", cfg.EffectiveProjectLanguage())
	}
}

// TestEffectiveProjectLanguage_LegacyTopLevel verifies the legacy
// top-level project_language is returned when the structured section is
// empty, so old drem.toml files keep working.
func TestEffectiveProjectLanguage_LegacyTopLevel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")
	data := []byte(`project_language = "go"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.EffectiveProjectLanguage() != "go" {
		t.Errorf("EffectiveProjectLanguage() = %q, want go", cfg.EffectiveProjectLanguage())
	}
}

// TestEffectiveProjectLanguage_StructuredWinsOverLegacy verifies the
// structured [project].language takes precedence over the legacy
// top-level project_language when both are set.
func TestEffectiveProjectLanguage_StructuredWinsOverLegacy(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")
	data := []byte(`project_language = "go"

[project]
  language = "cpp"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.EffectiveProjectLanguage() != "cpp" {
		t.Errorf("EffectiveProjectLanguage() = %q, want cpp (structured wins)", cfg.EffectiveProjectLanguage())
	}
}

// TestLoadConfig_AgentContainerImageOverrides verifies that per-agent
// container_image overrides flow through LoadConfig into
// AgentsConfig.ContainerImageOverrides().
func TestLoadConfig_AgentContainerImageOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")
	data := []byte(`
[agents.coder]
  container_image = "localhost:5000/drem-worker-go:v1.2"

[agents.merger]
  container_image = "localhost:5000/drem-merger:v1.2"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Agents.Coder.ContainerImage != "localhost:5000/drem-worker-go:v1.2" {
		t.Errorf("Coder.ContainerImage = %q", cfg.Agents.Coder.ContainerImage)
	}
	overrides := cfg.Agents.ContainerImageOverrides()
	if overrides["coder"] != "localhost:5000/drem-worker-go:v1.2" {
		t.Errorf("overrides[coder] = %q", overrides["coder"])
	}
	// Merger is not in AgentsConfig — ensure the schema parsed at least
	// the coder override. [agents.merger] is silently accepted at the
	// TOML layer (BurntSushi tolerates unknown tables by default) but
	// not surfaced through AgentsConfig.
}

// TestLoadConfig_ContainerImageOverrides_EmptyOnDefaults asserts that
// the overrides map is empty when no container_image fields are set,
// letting the resolver fall back to built-in defaults.
func TestLoadConfig_ContainerImageOverrides_EmptyOnDefaults(t *testing.T) {
	cfg := DefaultConfig()
	overrides := cfg.Agents.ContainerImageOverrides()
	if len(overrides) != 0 {
		t.Errorf("ContainerImageOverrides() on defaults = %v, want empty", overrides)
	}
}

// TestLoadConfig_TmuxTable_Tolerated verifies that a stale [tmux] table
// in a user drem.toml loads without error so the upgrade path is smooth.
// Prompt 17 removes this tolerance.
func TestLoadConfig_TmuxTable_Tolerated(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")
	data := []byte(`
[tmux]
  socket = "drem"
  config_file = "master/.tmux.conf"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig with [tmux] table: %v", err)
	}
}
