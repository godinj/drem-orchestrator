package main

import "github.com/godinj/drem-orchestrator/internal/model"

// ProfileConfig holds per-role model/effort overrides for a named profile.
// Only non-empty fields are applied; empty fields fall through to [agents.*]
// defaults and then to hardcoded defaults.
type ProfileConfig struct {
	Classifier AgentConfig `toml:"classifier"`
	Planner    AgentConfig `toml:"planner"`
	Coder      AgentConfig `toml:"coder"`
	Reviewer   AgentConfig `toml:"reviewer"`
	Fixer      AgentConfig `toml:"fixer"`
	Researcher AgentConfig `toml:"researcher"`
}

// ForAgentTypeWithProfile resolves an AgentCLIConfig for the given profile and
// agent type using three-layer fallback:
//
//  1. Profile override (profiles.<name>.<role>) — wins when non-empty
//  2. [agents.<role>] defaults from Config.Agents
//  3. Hardcoded default (effort="medium", model="")
//
// An unknown profile name silently falls back to layers 2 and 3.
func (c Config) ForAgentTypeWithProfile(profile string, at model.AgentType) model.AgentCLIConfig {
	// Stub: returns hardcoded default. Implementation pending.
	return model.AgentCLIConfig{Effort: "medium"}
}
