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

// ForAgentTypeWithProfile resolves an AgentCLIConfig for the given agent type
// and profile using three-layer fallback:
//
//  1. Profile override (profiles.<name>.<role>) — wins when non-empty
//  2. [agents.<role>] defaults from Config.Agents
//  3. Hardcoded default (effort="medium", model="")
//
// An unknown or empty profile name silently falls back to layers 2 and 3.
func (c Config) ForAgentTypeWithProfile(at model.AgentType, profile string) model.AgentCLIConfig {
	var override AgentConfig
	if p, ok := c.Profiles[profile]; ok {
		override = profileAgentConfig(p, at)
	}

	base := c.Agents.ForAgentType(at)

	// Apply hardcoded default for effort if layer 2 left it empty.
	if base.Effort == "" {
		base.Effort = "medium"
	}

	// Layer 1 wins field-by-field when non-empty.
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Effort != "" {
		base.Effort = override.Effort
	}

	return base
}

// profileAgentConfig extracts the AgentConfig for the given agent type from a ProfileConfig.
func profileAgentConfig(p ProfileConfig, at model.AgentType) AgentConfig {
	switch at {
	case model.AgentClassifier:
		return p.Classifier
	case model.AgentPlanner:
		return p.Planner
	case model.AgentCoder:
		return p.Coder
	case model.AgentReviewer:
		return p.Reviewer
	case model.AgentFixer:
		return p.Fixer
	case model.AgentResearcher:
		return p.Researcher
	default:
		return AgentConfig{}
	}
}
