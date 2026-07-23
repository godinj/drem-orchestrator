package orchdto

import "time"

// TaskReportDTO is a single read-only measurement snapshot for an
// orchestrated delivery. It joins the parent task, child work, inference
// attempts, native verification, Computer Use evidence, and host rework so a
// Codex adapter does not have to reconstruct the lifecycle from unrelated
// endpoints.
type TaskReportDTO struct {
	Project             string                `json:"project"`
	Task                TaskDTO               `json:"task"`
	GeneratedAt         time.Time             `json:"generated_at"`
	WallDurationMS      int64                 `json:"wall_duration_ms"`
	Children            []TaskReportChildDTO  `json:"children"`
	Phases              []TaskReportPhaseDTO  `json:"phases"`
	Attempts            []WorkerAttemptDTO    `json:"attempts"`
	CodexGoals          []CodexGoalUsageDTO   `json:"codex_goals"`
	Totals              TaskReportTotalsDTO   `json:"totals"`
	MeasurementCoverage TaskReportCoverageDTO `json:"measurement_coverage"`
	Warnings            []string              `json:"warnings,omitempty"`
}

type TaskReportChildDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Phase     string    `json:"phase,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskReportPhaseDTO contains cumulative time across every visit to the phase.
// Rework loops therefore add to the original verification/host-rework totals
// instead of overwriting the first pass.
type TaskReportPhaseDTO struct {
	Name          string `json:"name"`
	DurationMS    int64  `json:"duration_ms"`
	Visits        int    `json:"visits"`
	TokensIn      int    `json:"tokens_in"`
	TokensOut     int    `json:"tokens_out"`
	InferenceRuns int    `json:"inference_runs"`
}

type TaskReportTotalsDTO struct {
	TokensIn              int   `json:"tokens_in"`
	TokensOut             int   `json:"tokens_out"`
	WorkerAttempts        int   `json:"worker_attempts"`
	CompletedAttempts     int   `json:"completed_attempts"`
	FailedAttempts        int   `json:"failed_attempts"`
	AbortedAttempts       int   `json:"aborted_attempts"`
	ArtifactVersions      int   `json:"artifact_versions"`
	VerificationRuns      int   `json:"verification_runs"`
	ComputerUseRuns       int   `json:"computer_use_runs"`
	HostReworkSessions    int   `json:"host_rework_sessions"`
	HostReworkSubmissions int   `json:"host_rework_submissions"`
	CodexGoalCount        int   `json:"codex_goal_count"`
	CodexTokensUsed       int64 `json:"codex_tokens_used"`
	CodexElapsedMS        int64 `json:"codex_elapsed_ms"`
}

// TaskReportCoverageDTO distinguishes a measured zero from missing historical
// instrumentation. Eligible runs are terminal inference attempts and explicit
// inference-usage events; pre-inference aborted reservations are excluded.
type TaskReportCoverageDTO struct {
	EligibleInferenceRuns   int     `json:"eligible_inference_runs"`
	MeasuredInferenceRuns   int     `json:"measured_inference_runs"`
	UnmeasuredInferenceRuns int     `json:"unmeasured_inference_runs"`
	Percent                 float64 `json:"percent"`
	ExternalCodexMeasured   bool    `json:"external_codex_measured"`
}
