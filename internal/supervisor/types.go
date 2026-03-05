package supervisor

// FailureDiagnosis is the supervisor's evaluation of why an agent failed.
type FailureDiagnosis struct {
	RootCause           string `json:"root_cause"`
	Category            string `json:"category"` // transient, prompt_issue, code_error, environment, unknown
	ShouldRetry         bool   `json:"should_retry"`
	RetryStrategy       string `json:"retry_strategy"` // same_prompt, modified_prompt, different_approach
	PromptAdjustment    string `json:"prompt_adjustment"`
	MaxAdditionalRetries int   `json:"max_additional_retries"`
}

// FeedbackIntegration is the supervisor's synthesis of user feedback
// (plan rejection or test failure) into actionable guidance.
type FeedbackIntegration struct {
	Summary           string   `json:"summary"`
	KeyIssues         []string `json:"key_issues"`
	SuggestedApproach string   `json:"suggested_approach"`
}

// MergeConflictAnalysis is the supervisor's evaluation of merge conflicts.
type MergeConflictAnalysis struct {
	Severity           string            `json:"severity"` // trivial, moderate, complex
	ConflictSummaries  map[string]string `json:"conflict_summaries"`
	ResolutionStrategy string            `json:"resolution_strategy"` // auto_resolve, spawn_agent, manual
	ResolutionHints    string            `json:"resolution_hints"`
}

// BuildFailureDiagnosis is the supervisor's evaluation of a build failure.
type BuildFailureDiagnosis struct {
	RootCause     string   `json:"root_cause"`
	AffectedFiles []string `json:"affected_files"`
	SuggestedFix  string   `json:"suggested_fix"`
	CanAutoFix    bool     `json:"can_auto_fix"`
}
