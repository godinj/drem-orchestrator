package capability

import "github.com/godinj/drem-orchestrator/internal/model"

// ValidationResult holds errors and warnings from capability validation.
type ValidationResult struct {
	Errors   []ValidationError
	Warnings []string
}

// HasErrors reports whether any validation errors were found.
func (r *ValidationResult) HasErrors() bool { return len(r.Errors) > 0 }

// ValidationError describes a model missing required capabilities for an agent type.
type ValidationError struct {
	AgentType model.AgentType
	ModelID   string
	Missing   []Capability
}

// Validate checks agent configs against model capabilities.
// Stub: returns empty result — real logic in subtask 3.
func Validate(configs map[model.AgentType]model.AgentCLIConfig) *ValidationResult {
	return &ValidationResult{}
}
