package agent

// DirectPrepConfig holds connection and generation parameters for the
// direct SGLang prep agent. It embeds the base tool-calling agent config.
type DirectPrepConfig struct {
	DirectToolAgentConfig
}

// PrepPromptOpts contains the context needed to build a prep system prompt.
type PrepPromptOpts struct {
	TaskTitle         string
	TaskDescription   string
	EstimatedFiles    []string
	WorkDir           string
	ParentTitle       string
	ParentDescription string
	PlanJSON          string
}

// PrepSystemPrompt builds a system prompt for the prep agent.
func PrepSystemPrompt(opts PrepPromptOpts) string {
	return ""
}

// RunDirectPrep calls the SGLang tool-calling agent in prep mode.
// The caller is responsible for parsing the output JSON as PrepOutput.
func RunDirectPrep(cfg DirectPrepConfig, opts PrepPromptOpts, outputPath string) (*DirectToolAgentResult, error) {
	return nil, nil
}
