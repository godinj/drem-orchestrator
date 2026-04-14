package main

import (
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestConfig_ValidateModelCapabilities_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	result := cfg.ValidateModelCapabilities()
	if result.HasErrors() {
		t.Errorf("DefaultConfig should have no errors, got: %v", result.Errors)
	}
}

func TestConfig_ValidateModelCapabilities_ValidModels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents = AgentsConfig{
		Classifier: AgentConfig{Model: "claude-sonnet-4-6"},
		Planner:    AgentConfig{Model: "claude-sonnet-4-6"},
		Coder:      AgentConfig{Model: "claude-sonnet-4-6"},
		Reviewer:   AgentConfig{Model: "claude-sonnet-4-6"},
		Fixer:      AgentConfig{Model: "claude-sonnet-4-6"},
		Researcher: AgentConfig{Model: "claude-sonnet-4-6"},
	}
	result := cfg.ValidateModelCapabilities()
	if result.HasErrors() {
		t.Errorf("all claude-sonnet-4-6 should have no errors, got: %v", result.Errors)
	}
	if len(result.Warnings) > 0 {
		t.Errorf("all claude-sonnet-4-6 should have no warnings, got: %v", result.Warnings)
	}
}

func TestConfig_ValidateModelCapabilities_IncompatibleModel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents = AgentsConfig{
		Planner: AgentConfig{Model: "claude-3-haiku-20240307"},
	}
	result := cfg.ValidateModelCapabilities()
	if !result.HasErrors() {
		t.Fatal("expected errors for planner with claude-3-haiku, got none")
	}

	found := false
	for _, e := range result.Errors {
		if e.AgentType == model.AgentPlanner && e.ModelID == "claude-3-haiku-20240307" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error for planner agent, got: %v", result.Errors)
	}
}

func TestConfig_ValidateModelCapabilities_ProfileOverride(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents = AgentsConfig{
		Classifier: AgentConfig{Model: "claude-sonnet-4-6"},
		Planner:    AgentConfig{Model: "claude-sonnet-4-6"},
		Coder:      AgentConfig{Model: "claude-sonnet-4-6"},
		Reviewer:   AgentConfig{Model: "claude-sonnet-4-6"},
		Fixer:      AgentConfig{Model: "claude-sonnet-4-6"},
		Researcher: AgentConfig{Model: "claude-sonnet-4-6"},
	}
	cfg.Profiles = map[string]ProfileConfig{
		"fast": {
			Coder: AgentConfig{Model: "claude-3-haiku-20240307"},
		},
	}

	result := cfg.ValidateModelCapabilities()
	if !result.HasErrors() {
		t.Fatal("expected errors for profile 'fast' coder override, got none")
	}

	foundProfile := false
	for _, e := range result.Errors {
		if e.AgentType == model.AgentCoder && strings.Contains(e.ModelID, "claude-3-haiku") {
			foundProfile = true
			break
		}
	}
	if !foundProfile {
		t.Errorf("expected error mentioning coder in profile, got: %v", result.Errors)
	}

	// The error or warning output should mention the profile name.
	summary := result.Summary()
	if !strings.Contains(summary, "fast") {
		t.Errorf("expected summary to mention profile 'fast', got: %s", summary)
	}
}

func TestConfig_ValidateModelCapabilities_UnknownModelWarning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents = AgentsConfig{
		Classifier: AgentConfig{Model: "unknown-model-123"},
	}
	result := cfg.ValidateModelCapabilities()
	if result.HasErrors() {
		t.Errorf("unknown model should produce warnings not errors, got errors: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected at least one warning for unknown model, got none")
	}

	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "unknown-model-123") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("expected warning mentioning 'unknown-model-123', got: %v", result.Warnings)
	}
}
