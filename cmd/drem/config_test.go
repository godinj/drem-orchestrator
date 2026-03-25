package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestDefaultConfigTmuxSocket(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TmuxSocket != "drem" {
		t.Errorf("TmuxSocket: got %q, want %q", cfg.TmuxSocket, "drem")
	}
}

func TestDefaultConfigTmuxConfigFile(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TmuxConfigFile != "master/.tmux.conf" {
		t.Errorf("TmuxConfigFile: got %q, want %q", cfg.TmuxConfigFile, "master/.tmux.conf")
	}
}

func TestLoadConfigTmuxOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	data := []byte(`tmux_socket = "custom"
tmux_config_file = "custom.conf"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.TmuxSocket != "custom" {
		t.Errorf("TmuxSocket: got %q, want %q", cfg.TmuxSocket, "custom")
	}
	if cfg.TmuxConfigFile != "custom.conf" {
		t.Errorf("TmuxConfigFile: got %q, want %q", cfg.TmuxConfigFile, "custom.conf")
	}
}

func TestLoadConfigTmuxDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// TOML file with no tmux fields — defaults must survive.
	data := []byte(`database_path = "./other.db"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.TmuxSocket != "drem" {
		t.Errorf("TmuxSocket: got %q, want %q", cfg.TmuxSocket, "drem")
	}
	if cfg.TmuxConfigFile != "master/.tmux.conf" {
		t.Errorf("TmuxConfigFile: got %q, want %q", cfg.TmuxConfigFile, "master/.tmux.conf")
	}
	// Verify the explicit override still applied.
	if cfg.DatabasePath != "./other.db" {
		t.Errorf("DatabasePath: got %q, want %q", cfg.DatabasePath, "./other.db")
	}
}

func TestDefaultConfigAgentDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Planner.Effort != "medium" {
		t.Errorf("Planner.Effort: got %q, want %q", cfg.Agents.Planner.Effort, "medium")
	}
	if cfg.Agents.Supervisor.Effort != "low" {
		t.Errorf("Supervisor.Effort: got %q, want %q", cfg.Agents.Supervisor.Effort, "low")
	}
	if cfg.Agents.Coder.Model != "" {
		t.Errorf("Coder.Model: got %q, want empty", cfg.Agents.Coder.Model)
	}
}

func TestLoadConfigAgentOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	data := []byte(`
[agents.planner]
  model = "claude-opus-4-6"
  effort = "high"

[agents.supervisor]
  model = "claude-haiku-4-5-20251001"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Agents.Planner.Model != "claude-opus-4-6" {
		t.Errorf("Planner.Model: got %q, want %q", cfg.Agents.Planner.Model, "claude-opus-4-6")
	}
	if cfg.Agents.Planner.Effort != "high" {
		t.Errorf("Planner.Effort: got %q, want %q", cfg.Agents.Planner.Effort, "high")
	}
	if cfg.Agents.Supervisor.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Supervisor.Model: got %q, want %q", cfg.Agents.Supervisor.Model, "claude-haiku-4-5-20251001")
	}
	// Supervisor effort not overridden — default must survive.
	if cfg.Agents.Supervisor.Effort != "low" {
		t.Errorf("Supervisor.Effort: got %q, want %q", cfg.Agents.Supervisor.Effort, "low")
	}
	// Coder not mentioned — defaults must survive.
	if cfg.Agents.Coder.Effort != "medium" {
		t.Errorf("Coder.Effort: got %q, want %q", cfg.Agents.Coder.Effort, "medium")
	}
}

func TestAgentsConfigForAgentType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Planner = AgentConfig{Model: "claude-opus-4-6", Effort: "high"}

	got := cfg.Agents.ForAgentType(model.AgentPlanner)
	if got.Model != "claude-opus-4-6" || got.Effort != "high" {
		t.Errorf("ForAgentType(Planner) = %+v, want {Model:claude-opus-4-6 Effort:high}", got)
	}

	got = cfg.Agents.ForAgentType(model.AgentCoder)
	if got.Model != "" || got.Effort != "medium" {
		t.Errorf("ForAgentType(Coder) = %+v, want {Model: Effort:medium}", got)
	}
}

func TestAgentsConfigSupervisorCLIConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Supervisor = AgentConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"}

	got := cfg.Agents.SupervisorCLIConfig()
	if got.Model != "claude-haiku-4-5-20251001" || got.Effort != "low" {
		t.Errorf("SupervisorCLIConfig() = %+v", got)
	}
}

func TestAgentsConfigInteractiveSupervisorCLIConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.InteractiveSupervisor = AgentConfig{Model: "claude-opus-4-6", Effort: "high"}

	got := cfg.Agents.InteractiveSupervisorCLIConfig()
	if got.Model != "claude-opus-4-6" || got.Effort != "high" {
		t.Errorf("InteractiveSupervisorCLIConfig() = %+v", got)
	}
}
