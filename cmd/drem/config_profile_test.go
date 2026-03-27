package main

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ptr returns a pointer to an AgentConfig literal, making table-driven test
// data readable inline.
func ptr(ac AgentConfig) *AgentConfig { return &ac }

// TestForAgentTypeWithProfile covers all layering scenarios for
// Config.ForAgentTypeWithProfile. Resolution order under test:
//  1. Profile override (non-empty fields from the matching profile's agent entry)
//  2. Base [agents.*] config
//  3. Hardcoded defaults
func TestForAgentTypeWithProfile(t *testing.T) {
	cases := []struct {
		name        string
		cfg         Config
		agentType   model.AgentType
		profileName string
		want        model.AgentCLIConfig
		wantErr     bool
	}{
		{
			// Profile specifies both model and effort for the target agent type.
			// Both fields must come from the profile, not from base config.
			name: "full override: profile sets both model and effort",
			cfg: Config{
				Agents: AgentsConfig{
					Coder: AgentConfig{Model: "base-model", Effort: "medium"},
				},
				Profiles: map[string]ProfileConfig{
					"fast": {Agents: ProfileAgentsConfig{
						Coder: ptr(AgentConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"}),
					}},
				},
			},
			agentType:   model.AgentCoder,
			profileName: "fast",
			want:        model.AgentCLIConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"},
		},
		{
			// Profile specifies only model; effort field is empty string in the
			// profile's AgentConfig, so effort must be inherited from base config.
			name: "partial override: profile sets model only, effort inherits from base",
			cfg: Config{
				Agents: AgentsConfig{
					Coder: AgentConfig{Model: "base-model", Effort: "high"},
				},
				Profiles: map[string]ProfileConfig{
					"quality": {Agents: ProfileAgentsConfig{
						Coder: ptr(AgentConfig{Model: "claude-opus-4-6"}),
					}},
				},
			},
			agentType:   model.AgentCoder,
			profileName: "quality",
			want:        model.AgentCLIConfig{Model: "claude-opus-4-6", Effort: "high"},
		},
		{
			// Profile specifies only effort; model field is empty string in the
			// profile's AgentConfig, so model must be inherited from base config.
			name: "partial override: profile sets effort only, model inherits from base",
			cfg: Config{
				Agents: AgentsConfig{
					Coder: AgentConfig{Model: "claude-sonnet-4-6", Effort: "medium"},
				},
				Profiles: map[string]ProfileConfig{
					"turbo": {Agents: ProfileAgentsConfig{
						Coder: ptr(AgentConfig{Effort: "low"}),
					}},
				},
			},
			agentType:   model.AgentCoder,
			profileName: "turbo",
			want:        model.AgentCLIConfig{Model: "claude-sonnet-4-6", Effort: "low"},
		},
		{
			// A non-empty profileName that doesn't exist in c.Profiles is always
			// a misconfiguration and must return an error.
			name:        "missing profile returns error",
			cfg:         Config{Profiles: map[string]ProfileConfig{}},
			agentType:   model.AgentCoder,
			profileName: "nonexistent",
			wantErr:     true,
		},
		{
			// An empty profileName means "no profile" — fall through to the base
			// agent config with no error. Nil Profiles map must not panic.
			name: "empty profile name falls through to base config",
			cfg: Config{
				Agents: AgentsConfig{
					Planner: AgentConfig{Model: "claude-opus-4-6", Effort: "high"},
				},
			},
			agentType:   model.AgentPlanner,
			profileName: "",
			want:        model.AgentCLIConfig{Model: "claude-opus-4-6", Effort: "high"},
		},
		{
			// Profile exists and overrides Coder, but the queried agent type is
			// Fixer. Fixer has no entry in the profile, so Fixer must resolve
			// entirely from the base config.
			name: "unspecified role: profile exists but does not override this agent type",
			cfg: Config{
				Agents: AgentsConfig{
					Fixer: AgentConfig{Model: "base-fixer", Effort: "medium"},
				},
				Profiles: map[string]ProfileConfig{
					"partial": {Agents: ProfileAgentsConfig{
						Coder: ptr(AgentConfig{Model: "override-coder"}),
						// Fixer intentionally absent
					}},
				},
			},
			agentType:   model.AgentFixer,
			profileName: "partial",
			want:        model.AgentCLIConfig{Model: "base-fixer", Effort: "medium"},
		},
		{
			// Profile overrides multiple agent types. Each must resolve
			// independently without bleed-through between roles. Here we query
			// Reviewer which has a partial override (effort only) in a profile
			// that also overrides Coder (full). Reviewer must get its own base
			// model, not Coder's.
			name: "multi-agent profile: overrides are isolated per agent type",
			cfg: Config{
				Agents: AgentsConfig{
					Coder:    AgentConfig{Model: "coder-base", Effort: "medium"},
					Reviewer: AgentConfig{Model: "reviewer-base", Effort: "medium"},
				},
				Profiles: map[string]ProfileConfig{
					"multi": {Agents: ProfileAgentsConfig{
						Coder:    ptr(AgentConfig{Model: "coder-override", Effort: "high"}),
						Reviewer: ptr(AgentConfig{Effort: "low"}),
					}},
				},
			},
			agentType:   model.AgentReviewer,
			profileName: "multi",
			want:        model.AgentCLIConfig{Model: "reviewer-base", Effort: "low"},
		},
		{
			// Researcher is a valid agent type with its own base config entry.
			// Full override through a profile must work for all supported types,
			// not just Coder.
			name: "researcher agent type: full override via profile",
			cfg: Config{
				Agents: AgentsConfig{
					Researcher: AgentConfig{Model: "researcher-base", Effort: "medium"},
				},
				Profiles: map[string]ProfileConfig{
					"research-fast": {Agents: ProfileAgentsConfig{
						Researcher: ptr(AgentConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"}),
					}},
				},
			},
			agentType:   model.AgentResearcher,
			profileName: "research-fast",
			want:        model.AgentCLIConfig{Model: "claude-haiku-4-5-20251001", Effort: "low"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cfg.ForAgentTypeWithProfile(tc.agentType, tc.profileName)

			if tc.wantErr {
				if err == nil {
					t.Errorf("ForAgentTypeWithProfile(%q, %q): expected error, got nil", tc.agentType, tc.profileName)
				}
				return
			}

			if err != nil {
				t.Fatalf("ForAgentTypeWithProfile(%q, %q): unexpected error: %v", tc.agentType, tc.profileName, err)
			}

			if got != tc.want {
				t.Errorf("ForAgentTypeWithProfile(%q, %q)\n  got  %+v\n  want %+v",
					tc.agentType, tc.profileName, got, tc.want)
			}
		})
	}
}
