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

// --- Profile parsing and resolution tests ---

// TestLoadConfigProfileParsing verifies that a drem.toml with [profiles.fast]
// and [profiles.cheap] sections is correctly decoded into Config.Profiles.
func TestLoadConfigProfileParsing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")

	data := []byte(`
[profiles.fast.coder]
  model = "claude-opus-4-6"
  effort = "high"
[profiles.fast.planner]
  model = "claude-opus-4-6"
  effort = "high"

[profiles.cheap.coder]
  model = "claude-haiku-4-5-20251001"
  effort = "low"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if len(cfg.Profiles) != 2 {
		t.Fatalf("Profiles len: got %d, want 2", len(cfg.Profiles))
	}

	fast, ok := cfg.Profiles["fast"]
	if !ok {
		t.Fatal("Profiles[fast] not found")
	}
	if fast.Coder.Model != "claude-opus-4-6" {
		t.Errorf("fast.Coder.Model: got %q, want %q", fast.Coder.Model, "claude-opus-4-6")
	}
	if fast.Coder.Effort != "high" {
		t.Errorf("fast.Coder.Effort: got %q, want %q", fast.Coder.Effort, "high")
	}
	if fast.Planner.Model != "claude-opus-4-6" {
		t.Errorf("fast.Planner.Model: got %q, want %q", fast.Planner.Model, "claude-opus-4-6")
	}

	cheap, ok := cfg.Profiles["cheap"]
	if !ok {
		t.Fatal("Profiles[cheap] not found")
	}
	if cheap.Coder.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("cheap.Coder.Model: got %q, want %q", cheap.Coder.Model, "claude-haiku-4-5-20251001")
	}
	if cheap.Coder.Effort != "low" {
		t.Errorf("cheap.Coder.Effort: got %q, want %q", cheap.Coder.Effort, "low")
	}
	// Planner not set in cheap — must be zero value.
	if cheap.Planner.Model != "" || cheap.Planner.Effort != "" {
		t.Errorf("cheap.Planner should be zero, got %+v", cheap.Planner)
	}
}

// TestForAgentTypeWithProfile_ProfileOverrideWins verifies that a profile's
// model/effort values take precedence over the [agents.*] defaults.
func TestForAgentTypeWithProfile_ProfileOverrideWins(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles = map[string]ProfileConfig{
		"fast": {
			Coder: AgentConfig{Model: "claude-opus-4-6", Effort: "high"},
		},
	}

	got := cfg.ForAgentTypeWithProfile("fast", model.AgentCoder)

	if got.Model != "claude-opus-4-6" {
		t.Errorf("Model: got %q, want %q", got.Model, "claude-opus-4-6")
	}
	if got.Effort != "high" {
		t.Errorf("Effort: got %q, want %q", got.Effort, "high")
	}
}

// TestForAgentTypeWithProfile_FallsBackToAgentsDefault verifies that when a
// profile exists but does not specify a role, the [agents.*] default is used.
func TestForAgentTypeWithProfile_FallsBackToAgentsDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Planner = AgentConfig{Model: "claude-sonnet-4-6", Effort: "medium"}
	cfg.Profiles = map[string]ProfileConfig{
		"fast": {
			// Coder overridden, but Planner is not specified.
			Coder: AgentConfig{Model: "claude-opus-4-6", Effort: "high"},
		},
	}

	got := cfg.ForAgentTypeWithProfile("fast", model.AgentPlanner)

	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("Model: got %q, want %q", got.Model, "claude-sonnet-4-6")
	}
	if got.Effort != "medium" {
		t.Errorf("Effort: got %q, want %q", got.Effort, "medium")
	}
}

// TestForAgentTypeWithProfile_FallsBackToHardcodedDefault verifies that when
// neither the profile nor [agents.*] specifies the role, effort defaults to
// "medium" and model is empty.
func TestForAgentTypeWithProfile_FallsBackToHardcodedDefault(t *testing.T) {
	cfg := DefaultConfig()
	// Wipe the agents default for Fixer to simulate an unspecified role.
	cfg.Agents.Fixer = AgentConfig{}
	cfg.Profiles = map[string]ProfileConfig{
		"fast": {}, // Fixer not specified in profile either.
	}

	got := cfg.ForAgentTypeWithProfile("fast", model.AgentFixer)

	if got.Model != "" {
		t.Errorf("Model: got %q, want empty", got.Model)
	}
	if got.Effort != "medium" {
		t.Errorf("Effort: got %q, want %q", got.Effort, "medium")
	}
}

// TestForAgentTypeWithProfile_UnknownProfileReturnsDefault verifies that an
// unknown profile name does not panic and returns the [agents.*] default.
func TestForAgentTypeWithProfile_UnknownProfileReturnsDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Coder = AgentConfig{Model: "claude-sonnet-4-6", Effort: "medium"}
	cfg.Profiles = map[string]ProfileConfig{
		"fast": {Coder: AgentConfig{Model: "claude-opus-4-6", Effort: "high"}},
	}

	// "unknown" does not exist — must not panic and must return agents default.
	got := cfg.ForAgentTypeWithProfile("unknown", model.AgentCoder)

	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("Model: got %q, want %q", got.Model, "claude-sonnet-4-6")
	}
	if got.Effort != "medium" {
		t.Errorf("Effort: got %q, want %q", got.Effort, "medium")
	}
}

// TestForAgentTypeWithProfile_PartialOverrideInheritance verifies that a
// profile that only overrides coder_model leaves planner, reviewer, and fixer
// inheriting from [agents.*] defaults.
func TestForAgentTypeWithProfile_PartialOverrideInheritance(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Planner = AgentConfig{Effort: "medium"}
	cfg.Agents.Reviewer = AgentConfig{Effort: "medium"}
	cfg.Agents.Fixer = AgentConfig{Effort: "medium"}
	cfg.Profiles = map[string]ProfileConfig{
		"cheap": {
			Coder: AgentConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"},
			// Planner, Reviewer, Fixer not set.
		},
	}

	for _, tc := range []struct {
		at         model.AgentType
		wantModel  string
		wantEffort string
	}{
		{model.AgentCoder, "claude-haiku-4-5-20251001", "low"},
		{model.AgentPlanner, "", "medium"},
		{model.AgentReviewer, "", "medium"},
		{model.AgentFixer, "", "medium"},
	} {
		got := cfg.ForAgentTypeWithProfile("cheap", tc.at)
		if got.Model != tc.wantModel {
			t.Errorf("cheap/%v Model: got %q, want %q", tc.at, got.Model, tc.wantModel)
		}
		if got.Effort != tc.wantEffort {
			t.Errorf("cheap/%v Effort: got %q, want %q", tc.at, got.Effort, tc.wantEffort)
		}
	}
}

// TestForAgentTypeWithProfile_MultipleProfilesCoexist verifies that two
// profiles can be parsed and resolved independently without interfering.
func TestForAgentTypeWithProfile_MultipleProfilesCoexist(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles = map[string]ProfileConfig{
		"fast": {
			Coder: AgentConfig{Model: "claude-opus-4-6", Effort: "high"},
		},
		"cheap": {
			Coder: AgentConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"},
		},
	}

	fast := cfg.ForAgentTypeWithProfile("fast", model.AgentCoder)
	cheap := cfg.ForAgentTypeWithProfile("cheap", model.AgentCoder)

	if fast.Model != "claude-opus-4-6" {
		t.Errorf("fast Model: got %q, want %q", fast.Model, "claude-opus-4-6")
	}
	if fast.Effort != "high" {
		t.Errorf("fast Effort: got %q, want %q", fast.Effort, "high")
	}
	if cheap.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("cheap Model: got %q, want %q", cheap.Model, "claude-haiku-4-5-20251001")
	}
	if cheap.Effort != "low" {
		t.Errorf("cheap Effort: got %q, want %q", cheap.Effort, "low")
	}
}

// TestLoadConfigIntegrationProfileRoundTrip is a realistic end-to-end test
// that creates a drem.toml with both [profiles.fast] and [profiles.cheap],
// parses it via LoadConfig, then resolves ForAgentTypeWithProfile for each
// profile and every relevant agent type to verify correct layering.
func TestLoadConfigIntegrationProfileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")

	data := []byte(`
[agents.planner]
  effort = "medium"
[agents.coder]
  effort = "medium"
[agents.reviewer]
  effort = "medium"
[agents.fixer]
  effort = "medium"

[profiles.fast.coder]
  model = "claude-opus-4-6"
  effort = "high"
[profiles.fast.planner]
  model = "claude-opus-4-6"
  effort = "high"

[profiles.cheap.coder]
  model = "claude-haiku-4-5-20251001"
  effort = "low"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// fast: coder and planner overridden; reviewer and fixer inherit from [agents.*]
	fastCoder := cfg.ForAgentTypeWithProfile("fast", model.AgentCoder)
	if fastCoder.Model != "claude-opus-4-6" || fastCoder.Effort != "high" {
		t.Errorf("fast/coder: got %+v, want {claude-opus-4-6 high}", fastCoder)
	}

	fastPlanner := cfg.ForAgentTypeWithProfile("fast", model.AgentPlanner)
	if fastPlanner.Model != "claude-opus-4-6" || fastPlanner.Effort != "high" {
		t.Errorf("fast/planner: got %+v, want {claude-opus-4-6 high}", fastPlanner)
	}

	fastReviewer := cfg.ForAgentTypeWithProfile("fast", model.AgentReviewer)
	if fastReviewer.Model != "" || fastReviewer.Effort != "medium" {
		t.Errorf("fast/reviewer: got %+v, want { medium}", fastReviewer)
	}

	// cheap: only coder overridden; planner inherits from [agents.*]
	cheapCoder := cfg.ForAgentTypeWithProfile("cheap", model.AgentCoder)
	if cheapCoder.Model != "claude-haiku-4-5-20251001" || cheapCoder.Effort != "low" {
		t.Errorf("cheap/coder: got %+v, want {claude-haiku-4-5-20251001 low}", cheapCoder)
	}

	cheapPlanner := cfg.ForAgentTypeWithProfile("cheap", model.AgentPlanner)
	if cheapPlanner.Model != "" || cheapPlanner.Effort != "medium" {
		t.Errorf("cheap/planner: got %+v, want { medium}", cheapPlanner)
	}
}
