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
