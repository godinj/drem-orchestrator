package main

// TestProfilesDocExample* are living documentation tests that show how
// multi-profile drem.toml configuration works end-to-end. The TOML block
// below is a realistic starting point — copy it into a real drem.toml and
// adjust model names as needed.
//
// Resolution order for each agent type:
//  1. Profile-specific override (if the named profile exists and specifies that role)
//  2. Default [agents.<role>] config
//  3. Hardcoded defaults (effort = "medium", model = "")
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const exampleMultiProfileTOML = `
bare_repo_path = "/repos/myproject.git"
default_branch = "master"

# Default models applied when no profile is active or a profile does not
# override a particular role.
[agents.planner]
  model = "claude-sonnet-4-6"
  effort = "medium"

[agents.coder]
  model = "claude-sonnet-4-6"
  effort = "medium"

[agents.reviewer]
  model = "claude-sonnet-4-6"
  effort = "medium"

[agents.fixer]
  model = "claude-sonnet-4-6"
  effort = "medium"

# "fast" profile — swap planner and coder to Haiku for speed-sensitive runs.
# Roles not listed here (reviewer, fixer, …) inherit from [agents.*] above.
[profiles.fast.planner]
  model = "claude-haiku-4-5-20251001"
  effort = "low"

[profiles.fast.coder]
  model = "claude-haiku-4-5-20251001"
  effort = "low"

# "cheap" profile — only the coder is swapped to reduce cost.
# Everything else (planner, reviewer, …) inherits from [agents.*].
[profiles.cheap.coder]
  model = "claude-haiku-4-5-20251001"
`

func loadProfilesDocConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "drem.toml")
	if err := os.WriteFile(cfgPath, []byte(exampleMultiProfileTOML), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// TestProfilesDocExample_FastProfileOverridesCoderAndPlanner verifies that the
// "fast" profile swaps both the planner and the coder to Haiku.
func TestProfilesDocExample_FastProfileOverridesCoderAndPlanner(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	planner, err := cfg.ForAgentTypeWithProfile(model.AgentPlanner, "fast")
	if err != nil {
		t.Fatalf("fast/planner unexpected error: %v", err)
	}
	if planner.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("fast/planner model: got %q, want claude-haiku-4-5-20251001", planner.Model)
	}
	if planner.Effort != "low" {
		t.Errorf("fast/planner effort: got %q, want low", planner.Effort)
	}

	coder, err := cfg.ForAgentTypeWithProfile(model.AgentCoder, "fast")
	if err != nil {
		t.Fatalf("fast/coder unexpected error: %v", err)
	}
	if coder.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("fast/coder model: got %q, want claude-haiku-4-5-20251001", coder.Model)
	}
	if coder.Effort != "low" {
		t.Errorf("fast/coder effort: got %q, want low", coder.Effort)
	}
}

// TestProfilesDocExample_FastProfileInheritsUnspecifiedRoles verifies that roles
// not listed in the "fast" profile fall back to the default [agents.*] config.
func TestProfilesDocExample_FastProfileInheritsUnspecifiedRoles(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	reviewer, err := cfg.ForAgentTypeWithProfile(model.AgentReviewer, "fast")
	if err != nil {
		t.Fatalf("fast/reviewer unexpected error: %v", err)
	}
	if reviewer.Model != "claude-sonnet-4-6" {
		t.Errorf("fast/reviewer model: got %q, want claude-sonnet-4-6 (default)", reviewer.Model)
	}
	if reviewer.Effort != "medium" {
		t.Errorf("fast/reviewer effort: got %q, want medium (default)", reviewer.Effort)
	}

	fixer, err := cfg.ForAgentTypeWithProfile(model.AgentFixer, "fast")
	if err != nil {
		t.Fatalf("fast/fixer unexpected error: %v", err)
	}
	if fixer.Model != "claude-sonnet-4-6" {
		t.Errorf("fast/fixer model: got %q, want claude-sonnet-4-6 (default)", fixer.Model)
	}
}

// TestProfilesDocExample_CheapProfileOverridesOnlyCoder verifies that the
// "cheap" profile only overrides the coder role.
func TestProfilesDocExample_CheapProfileOverridesOnlyCoder(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	coder, err := cfg.ForAgentTypeWithProfile(model.AgentCoder, "cheap")
	if err != nil {
		t.Fatalf("cheap/coder unexpected error: %v", err)
	}
	if coder.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("cheap/coder model: got %q, want claude-haiku-4-5-20251001", coder.Model)
	}

	// Planner not in "cheap" profile — must use default.
	planner, err := cfg.ForAgentTypeWithProfile(model.AgentPlanner, "cheap")
	if err != nil {
		t.Fatalf("cheap/planner unexpected error: %v", err)
	}
	if planner.Model != "claude-sonnet-4-6" {
		t.Errorf("cheap/planner model: got %q, want claude-sonnet-4-6 (default)", planner.Model)
	}
	if planner.Effort != "medium" {
		t.Errorf("cheap/planner effort: got %q, want medium (default)", planner.Effort)
	}
}

// TestProfilesDocExample_UnknownProfileReturnsError verifies that requesting a
// profile name that does not exist in the config returns a non-nil error.
func TestProfilesDocExample_UnknownProfileReturnsError(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	_, err := cfg.ForAgentTypeWithProfile(model.AgentCoder, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown profile name, got nil")
	}
}

// TestProfilesDocExample_EmptyProfileNameFallsBackToDefaults verifies that
// an empty profile name is equivalent to running with no profile active and
// returns nil error.
func TestProfilesDocExample_EmptyProfileNameFallsBackToDefaults(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	coder, err := cfg.ForAgentTypeWithProfile(model.AgentCoder, "")
	if err != nil {
		t.Fatalf("empty profile must not return error, got: %v", err)
	}
	if coder.Model != "claude-sonnet-4-6" {
		t.Errorf("empty profile/coder model: got %q, want claude-sonnet-4-6 (default)", coder.Model)
	}
}

// TestProfilesDocExample_AllAgentTypesResolveUnderFastProfile verifies that
// every agent type can be resolved without panic under the "fast" profile.
func TestProfilesDocExample_AllAgentTypesResolveUnderFastProfile(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	agentTypes := []model.AgentType{
		model.AgentClassifier,
		model.AgentPlanner,
		model.AgentCoder,
		model.AgentReviewer,
		model.AgentFixer,
		model.AgentResearcher,
	}

	for _, at := range agentTypes {
		got, err := cfg.ForAgentTypeWithProfile(at, "fast")
		if err != nil {
			t.Fatalf("fast/%v unexpected error: %v", at, err)
		}
		if got.Effort == "" {
			t.Errorf("fast/%v: effort must not be empty (missing default fallback)", at)
		}
	}
}
