package capability

import (
	"fmt"
	"sort"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ValidationResult holds errors and warnings from capability validation.
type ValidationResult struct {
	Errors   []ValidationError
	Warnings []string
}

// HasErrors reports whether any validation errors were found.
func (r *ValidationResult) HasErrors() bool { return len(r.Errors) > 0 }

// Summary joins all errors and warnings into a single string separated by newlines.
func (r *ValidationResult) Summary() string {
	var lines []string
	for _, e := range r.Errors {
		lines = append(lines, e.Error())
	}
	lines = append(lines, r.Warnings...)
	return strings.Join(lines, "\n")
}

// ValidationError describes a model missing required capabilities for an agent type.
type ValidationError struct {
	AgentType model.AgentType
	ModelID   string
	Missing   []Capability
}

// Error formats the validation error as a human-readable string.
func (e ValidationError) Error() string {
	caps := make([]string, len(e.Missing))
	for i, c := range e.Missing {
		caps[i] = string(c)
	}
	return fmt.Sprintf("agent %s: model %s lacks required capabilities: %s",
		e.AgentType, e.ModelID, strings.Join(caps, ", "))
}

// Validate checks each agent config's model against that agent type's required
// capabilities. Empty models and OpenCode providers are skipped. Unknown models
// produce warnings; known models with missing capabilities produce errors.
func Validate(configs map[model.AgentType]model.AgentCLIConfig) *ValidationResult {
	result := &ValidationResult{}

	// Sort agent types for deterministic output.
	types := make([]model.AgentType, 0, len(configs))
	for at := range configs {
		types = append(types, at)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	for _, at := range types {
		cfg := configs[at]

		if cfg.Model == "" {
			continue
		}
		if cfg.EffectiveProvider() == model.ProviderOpenCode {
			continue
		}

		caps, known := LookupModel(cfg.Model)
		if !known {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("agent %s: unknown model %q — cannot verify capabilities", at, cfg.Model))
			continue
		}

		required := RequirementsFor(at)
		var missing []Capability
		for cap := range required {
			if !caps.Has(cap) {
				missing = append(missing, cap)
			}
		}
		if len(missing) > 0 {
			sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
			result.Errors = append(result.Errors, ValidationError{
				AgentType: at,
				ModelID:   cfg.Model,
				Missing:   missing,
			})
		}
	}

	return result
}
