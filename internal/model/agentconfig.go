package model

// AgentCLIConfig holds per-agent-type CLI flags for Claude Code invocations.
type AgentCLIConfig struct {
	Model  string // "" means inherit Claude CLI default
	Effort string // "low", "medium", "high"
}

// CLIArgs returns the command-line arguments for model and effort.
func (c AgentCLIConfig) CLIArgs() []string {
	var args []string
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	return args
}
