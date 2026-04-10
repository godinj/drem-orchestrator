package model

// ProviderType identifies which CLI backend to use for an agent.
type ProviderType string

const (
	ProviderClaude   ProviderType = "claude"
	ProviderOpenCode ProviderType = "opencode"
)

// AgentCLIConfig holds per-agent-type CLI flags for agent invocations.
type AgentCLIConfig struct {
	Provider ProviderType // "" treated as ProviderClaude
	Model    string       // "" means inherit CLI default
	Effort   string       // "low", "medium", "high"
}

// EffectiveProvider returns the provider to use, defaulting to ProviderClaude
// when Provider is empty.
func (c AgentCLIConfig) EffectiveProvider() ProviderType {
	if c.Provider == "" {
		return ProviderClaude
	}
	return c.Provider
}

// CLIArgs returns the command-line arguments for model and effort/variant,
// dispatching by provider.
func (c AgentCLIConfig) CLIArgs() []string {
	switch c.EffectiveProvider() {
	case ProviderOpenCode:
		return c.openCodeCLIArgs()
	default:
		return c.claudeCLIArgs()
	}
}

func (c AgentCLIConfig) claudeCLIArgs() []string {
	var args []string
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	return args
}

func (c AgentCLIConfig) openCodeCLIArgs() []string {
	var args []string
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--variant", c.Effort)
	}
	args = append(args, "--format", "json", "--agent", "build")
	return args
}
