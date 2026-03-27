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

	planner := cfg.ForAgentTypeWithProfile(model.AgentPlanner, "fast")
	if planner.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("fast/planner model: got %q, want claude-haiku-4-5-20251001", planner.Model)
	}
	if planner.Effort != "low" {
		t.Errorf("fast/planner effort: got %q, want low", planner.Effort)
	}

	coder := cfg.ForAgentTypeWithProfile(model.AgentCoder, "fast")
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

	reviewer := cfg.ForAgentTypeWithProfile(model.AgentReviewer, "fast")
	if reviewer.Model != "claude-sonnet-4-6" {
		t.Errorf("fast/reviewer model: got %q, want claude-sonnet-4-6 (default)", reviewer.Model)
	}
	if reviewer.Effort != "medium" {
		t.Errorf("fast/reviewer effort: got %q, want medium (default)", reviewer.Effort)
	}

	fixer := cfg.ForAgentTypeWithProfile(model.AgentFixer, "fast")
	if fixer.Model != "claude-sonnet-4-6" {
		t.Errorf("fast/fixer model: got %q, want claude-sonnet-4-6 (default)", fixer.Model)
	}
}

// TestProfilesDocExample_CheapProfileOverridesOnlyCoder verifies that the
// "cheap" profile only overrides the coder role.
func TestProfilesDocExample_CheapProfileOverridesOnlyCoder(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	coder := cfg.ForAgentTypeWithProfile(model.AgentCoder, "cheap")
	if coder.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("cheap/coder model: got %q, want claude-haiku-4-5-20251001", coder.Model)
	}

	// Planner not in "cheap" profile — must use default.
	planner := cfg.ForAgentTypeWithProfile(model.AgentPlanner, "cheap")
	if planner.Model != "claude-sonnet-4-6" {
		t.Errorf("cheap/planner model: got %q, want claude-sonnet-4-6 (default)", planner.Model)
	}
	if planner.Effort != "medium" {
		t.Errorf("cheap/planner effort: got %q, want medium (default)", planner.Effort)
	}
}

// TestProfilesDocExample_UnknownProfileFallsBackToDefaults verifies that
// requesting a profile name that does not exist in the config is safe and
// returns the default agent configuration.
func TestProfilesDocExample_UnknownProfileFallsBackToDefaults(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	coder := cfg.ForAgentTypeWithProfile(model.AgentCoder, "nonexistent")
	if coder.Model != "claude-sonnet-4-6" {
		t.Errorf("nonexistent/coder model: got %q, want claude-sonnet-4-6 (default)", coder.Model)
	}
	if coder.Effort != "medium" {
		t.Errorf("nonexistent/coder effort: got %q, want medium (default)", coder.Effort)
	}
}

// TestProfilesDocExample_EmptyProfileNameFallsBackToDefaults verifies that
// an empty profile name is equivalent to running with no profile active.
func TestProfilesDocExample_EmptyProfileNameFallsBackToDefaults(t *testing.T) {
	cfg := loadProfilesDocConfig(t)

	coder := cfg.ForAgentTypeWithProfile(model.AgentCoder, "")
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
		got := cfg.ForAgentTypeWithProfile(at, "fast")
		if got.Effort == "" {
			t.Errorf("fast/%v: effort must not be empty (missing default fallback)", at)
		}
	}
}
