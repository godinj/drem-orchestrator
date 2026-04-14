package main

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/capability"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// headlessAgentTypes lists agent types that are dispatched by the orchestrator
// and should have their model capabilities validated.
var headlessAgentTypes = []model.AgentType{
	model.AgentClassifier,
	model.AgentPlanner,
	model.AgentCoder,
	model.AgentReviewer,
	model.AgentFixer,
	model.AgentResearcher,
}

// ValidateModelCapabilities checks that every configured model supports the
// capabilities required by its assigned agent type. It validates both base
// agent configs and all profile overrides.
func (c Config) ValidateModelCapabilities() *capability.ValidationResult {
	// Validate base agent configs.
	configs := make(map[model.AgentType]model.AgentCLIConfig, len(headlessAgentTypes))
	for _, at := range headlessAgentTypes {
		configs[at] = c.Agents.ForAgentType(at)
	}
	result := capability.Validate(configs)

	// Validate each profile's overrides.
	for profileName := range c.Profiles {
		profileConfigs := make(map[model.AgentType]model.AgentCLIConfig, len(headlessAgentTypes))
		for _, at := range headlessAgentTypes {
			cfg, err := c.ForAgentTypeWithProfile(at, profileName)
			if err != nil {
				continue
			}
			profileConfigs[at] = cfg
		}
		profileResult := capability.Validate(profileConfigs)
		for i := range profileResult.Errors {
			profileResult.Errors[i].ModelID = fmt.Sprintf("%s (profile %q)", profileResult.Errors[i].ModelID, profileName)
		}
		result.Errors = append(result.Errors, profileResult.Errors...)
		for _, w := range profileResult.Warnings {
			result.Warnings = append(result.Warnings, fmt.Sprintf("profile %q: %s", profileName, w))
		}
	}

	return result
}
