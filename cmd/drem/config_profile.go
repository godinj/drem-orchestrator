package main

import (
	"fmt"
	"regexp"

	"github.com/godinj/drem-orchestrator/internal/model"
)

var profileNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ProfileAgentsConfig holds sparse per-agent-type overrides within a named
// profile. A nil pointer for an agent type means that type inherits all values
// from the base [agents.*] configuration. Within a non-nil AgentConfig, an
// empty string field means "inherit from base" for that individual field.
type ProfileAgentsConfig struct {
	Classifier *AgentConfig `toml:"classifier"`
	Planner    *AgentConfig `toml:"planner"`
	Coder      *AgentConfig `toml:"coder"`
	Reviewer   *AgentConfig `toml:"reviewer"`
	Fixer      *AgentConfig `toml:"fixer"`
	Researcher *AgentConfig `toml:"researcher"`
}

// ProfileConfig represents a named configuration profile that layers on top of
// the base [agents.*] configuration. Only explicitly set (non-empty) fields
// override their corresponding defaults.
type ProfileConfig struct {
	Agents ProfileAgentsConfig `toml:"agents"`
}

// forAgentType returns the *AgentConfig pointer for the given agent type, or
// nil if the profile does not override that type.
func (p ProfileAgentsConfig) forAgentType(at model.AgentType) *AgentConfig {
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
		return nil
	}
}

// ValidateProfileName reports whether name is a valid profile identifier.
// Valid names contain only ASCII letters, digits, hyphens, and underscores
// and must be non-empty.
func ValidateProfileName(name string) error {
	if !profileNameRE.MatchString(name) {
		return fmt.Errorf("profile name %q must match ^[a-zA-Z0-9_-]+$", name)
	}
	return nil
}

// ForAgentTypeWithProfile resolves the effective AgentCLIConfig by layering
// (highest to lowest priority):
//
//  1. Profile override: if profileName is non-empty and the profile specifies
//     this agent type, non-empty fields in the profile agent config override the
//     resolved base values.
//  2. Base agent config: the [agents.<type>] config from the main configuration.
//  3. Hardcoded default (effort = "medium").
//
// An empty profileName falls through to the base agent config without error.
// A non-empty profileName that does not match any entry in c.Profiles returns
// an error — a missing profile is always a configuration mistake.
//
// TOML configuration example:
//
//	# Base per-agent configuration (layer 2)
//	[agents.coder]
//	  model  = "claude-sonnet-4-6"
//	  effort = "medium"
//
//	# Named profile — only the fields explicitly set override the base (layer 1).
//	# Fields omitted here inherit from [agents.coder] above.
//	[profiles.fast.agents.coder]
//	  effort = "low"   # overrides effort; model inherits "claude-sonnet-4-6"
//
//	[profiles.quality.agents.coder]
//	  model  = "claude-opus-4-6"  # overrides model; effort inherits "medium"
//	  effort = "high"
//
// Calling ForAgentTypeWithProfile(AgentCoder, "fast") returns
// {Model: "claude-sonnet-4-6", Effort: "low"}.
// Calling ForAgentTypeWithProfile(AgentCoder, "") returns the base config
// {Model: "claude-sonnet-4-6", Effort: "medium"} without consulting any profile.
func (c Config) ForAgentTypeWithProfile(agentType model.AgentType, profileName string) (model.AgentCLIConfig, error) {
	if profileName == "" {
		return c.Agents.ForAgentType(agentType), nil
	}

	profile, ok := c.Profiles[profileName]
	if !ok {
		return model.AgentCLIConfig{}, fmt.Errorf("config profile %q not found", profileName)
	}

	base := c.Agents.ForAgentType(agentType)

	override := profile.Agents.forAgentType(agentType)
	if override == nil {
		return base, nil
	}

	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Effort != "" {
		base.Effort = override.Effort
	}

	return base, nil
}
