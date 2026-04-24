package model

import (
	"testing"
)

func TestAgentCLIConfig_CLIArgs(t *testing.T) {
	tests := []struct {
		name string
		cfg  AgentCLIConfig
		want []string
	}{
		{
			name: "empty config returns nil",
			cfg:  AgentCLIConfig{},
			want: nil,
		},
		{
			name: "effort only",
			cfg:  AgentCLIConfig{Effort: "medium"},
			want: []string{"--effort", "medium"},
		},
		{
			name: "model only",
			cfg:  AgentCLIConfig{Model: "claude-opus-4-6"},
			want: []string{"--model", "claude-opus-4-6"},
		},
		{
			name: "model and effort",
			cfg:  AgentCLIConfig{Model: "claude-sonnet-4-6", Effort: "high"},
			want: []string{"--model", "claude-sonnet-4-6", "--effort", "high"},
		},
		// Explicit Claude provider produces same args as default.
		{
			name: "explicit claude provider: model and effort",
			cfg:  AgentCLIConfig{Provider: ProviderClaude, Model: "claude-sonnet-4-6", Effort: "high"},
			want: []string{"--model", "claude-sonnet-4-6", "--effort", "high"},
		},
		// OpenCode provider tests.
		{
			name: "opencode: model and effort maps to --variant",
			cfg:  AgentCLIConfig{Provider: ProviderOpenCode, Model: "ollama/qwen3-coder", Effort: "minimal"},
			want: []string{"--model", "ollama/qwen3-coder", "--variant", "minimal", "--format", "json", "--agent", "build"},
		},
		{
			name: "opencode: model only, no effort",
			cfg:  AgentCLIConfig{Provider: ProviderOpenCode, Model: "ollama/qwen3-coder"},
			want: []string{"--model", "ollama/qwen3-coder", "--format", "json", "--agent", "build"},
		},
		{
			name: "opencode: effort only, no model",
			cfg:  AgentCLIConfig{Provider: ProviderOpenCode, Effort: "high"},
			want: []string{"--variant", "high", "--format", "json", "--agent", "build"},
		},
		{
			name: "opencode: empty model and effort still emits format and agent",
			cfg:  AgentCLIConfig{Provider: ProviderOpenCode},
			want: []string{"--format", "json", "--agent", "build"},
		},
		{
			name: "codex: model and effort maps to reasoning config",
			cfg:  AgentCLIConfig{Provider: ProviderCodex, Model: "gpt-5.5", Effort: "high"},
			want: []string{"--model", "gpt-5.5", "-c", "model_reasoning_effort=\"high\""},
		},
		{
			name: "codex: effort only",
			cfg:  AgentCLIConfig{Provider: ProviderCodex, Effort: "medium"},
			want: []string{"-c", "model_reasoning_effort=\"medium\""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.CLIArgs()
			if len(got) != len(tt.want) {
				t.Fatalf("CLIArgs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("CLIArgs()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAgentCLIConfig_EffectiveProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider ProviderType
		want     ProviderType
	}{
		{"empty defaults to claude", "", ProviderClaude},
		{"explicit claude", ProviderClaude, ProviderClaude},
		{"explicit opencode", ProviderOpenCode, ProviderOpenCode},
		{"explicit codex", ProviderCodex, ProviderCodex},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AgentCLIConfig{Provider: tt.provider}
			if got := cfg.EffectiveProvider(); got != tt.want {
				t.Errorf("EffectiveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}
