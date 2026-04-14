package capability

import (
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestValidate_AllCompatibleModels(t *testing.T) {
	configs := map[model.AgentType]model.AgentCLIConfig{
		model.AgentClassifier: {Model: "claude-opus-4-6"},
		model.AgentPlanner:    {Model: "claude-opus-4-6"},
		model.AgentCoder:      {Model: "claude-opus-4-6"},
		model.AgentReviewer:   {Model: "claude-opus-4-6"},
		model.AgentFixer:      {Model: "claude-opus-4-6"},
		model.AgentResearcher: {Model: "claude-opus-4-6"},
	}

	result := Validate(configs)

	if result.HasErrors() {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
	if len(result.Warnings) > 0 {
		t.Errorf("expected no warnings, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

func TestValidate_IncompatibleModel(t *testing.T) {
	configs := map[model.AgentType]model.AgentCLIConfig{
		model.AgentPlanner: {Model: "claude-3-haiku-20240307"},
	}

	result := Validate(configs)

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	ve := result.Errors[0]
	if ve.AgentType != model.AgentPlanner {
		t.Errorf("expected AgentType planner, got %s", ve.AgentType)
	}
	if ve.ModelID != "claude-3-haiku-20240307" {
		t.Errorf("expected ModelID claude-3-haiku-20240307, got %s", ve.ModelID)
	}
	foundThinking := false
	for _, c := range ve.Missing {
		if c == ExtendedThinking {
			foundThinking = true
		}
	}
	if !foundThinking {
		t.Errorf("expected Missing to contain ExtendedThinking, got %v", ve.Missing)
	}
}

func TestValidate_UnknownModel_Warning(t *testing.T) {
	configs := map[model.AgentType]model.AgentCLIConfig{
		model.AgentCoder: {Model: "some-unknown-model"},
	}

	result := Validate(configs)

	if result.HasErrors() {
		t.Errorf("expected no errors for unknown model, got %d", len(result.Errors))
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(result.Warnings))
	}
	if !strings.Contains(strings.ToLower(result.Warnings[0]), "unknown model") {
		t.Errorf("expected warning to contain 'unknown model', got %q", result.Warnings[0])
	}
}

func TestValidate_EmptyModel_Skipped(t *testing.T) {
	configs := map[model.AgentType]model.AgentCLIConfig{
		model.AgentCoder: {Model: ""},
	}

	result := Validate(configs)

	if result.HasErrors() {
		t.Errorf("expected no errors for empty model, got %d", len(result.Errors))
	}
	if len(result.Warnings) > 0 {
		t.Errorf("expected no warnings for empty model, got %d", len(result.Warnings))
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	configs := map[model.AgentType]model.AgentCLIConfig{
		model.AgentPlanner: {Model: "claude-3-haiku-20240307"},
		model.AgentCoder:   {Model: "claude-3-haiku-20240307"},
	}

	result := Validate(configs)

	if len(result.Errors) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(result.Errors))
	}
}

func TestValidate_OpenCodeProvider_Skipped(t *testing.T) {
	configs := map[model.AgentType]model.AgentCLIConfig{
		model.AgentCoder: {
			Provider: model.ProviderOpenCode,
			Model:    "ollama/qwen3-coder",
		},
	}

	result := Validate(configs)

	if result.HasErrors() {
		t.Errorf("expected no errors for OpenCode provider, got %d", len(result.Errors))
	}
	if len(result.Warnings) > 0 {
		t.Errorf("expected no warnings for OpenCode provider, got %d", len(result.Warnings))
	}
}
