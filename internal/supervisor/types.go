package supervisor

// FailureDiagnosis is the supervisor's evaluation of why an agent failed.
type FailureDiagnosis struct {
	RootCause            string `json:"root_cause"`
	Category             string `json:"category"` // transient, prompt_issue, code_error, environment, unknown
	ShouldRetry          bool   `json:"should_retry"`
	RetryStrategy        string `json:"retry_strategy"` // same_prompt, modified_prompt, different_approach
	PromptAdjustment     string `json:"prompt_adjustment"`
	MaxAdditionalRetries int    `json:"max_additional_retries"`
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

// PlanDepthReview is the supervisor's evaluation of a plan that failed
// the depth score. It determines whether the plan can be adjusted or
// the task concept is fundamentally flawed.
type PlanDepthReview struct {
	Assessment      string   `json:"assessment"`       // "adjustable" or "fundamentally_shallow"
	ShallowAreas    []string `json:"shallow_areas"`    // specific subtasks or modules that lack depth
	Recommendations []string `json:"recommendations"`  // actionable steps to improve depth
	RejectionReason string   `json:"rejection_reason"` // human-readable explanation for the task comment
}

// DepthConstraintDiagnosis is the supervisor's evaluation of depth constraint
// failures on the integration worktree. It identifies where violations occurred
// and recommends next steps.
type DepthConstraintDiagnosis struct {
	Violations      []DepthViolation `json:"violations"`
	RootCause       string           `json:"root_cause"`       // why depth constraints failed
	Recommendation  string           `json:"recommendation"`   // what to do next
	RejectionReason string           `json:"rejection_reason"` // for the task comment
}

// DepthViolation describes a single depth constraint violation.
type DepthViolation struct {
	Package     string `json:"package"`      // e.g., "internal/orchestrator"
	Metric      string `json:"metric"`       // "export_ratio" or "pass_through_count"
	ActualValue string `json:"actual_value"` // e.g., "0.25" or "7"
	Limit       string `json:"limit"`        // e.g., "0.15" or "3"
	Suggestion  string `json:"suggestion"`   // specific fix suggestion
}
