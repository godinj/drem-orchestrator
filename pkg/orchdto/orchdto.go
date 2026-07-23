// Package orchdto holds the public JSON wire types returned by the
// orchestrator HTTP API. It is imported by server-side handlers in
// internal/orchhttp, by the client library in pkg/orchclient, and by any
// consumer (Kyle, the TUI) that needs to decode API responses.
//
// The package is intentionally leaf-level: it has no dependencies outside
// the Go standard library so importers never pull in GORM, the orchestrator,
// or any internal/* code transitively. Schema changes to internal GORM
// models must not change these DTOs without a conscious decision; handlers
// marshal from models into these shapes so drift is explicit.
package orchdto

import (
	"encoding/json"
	"time"
)

// ProjectDTO describes a single project served by an orchestrator. The
// public /projects endpoint returns a slice of these; each orchestrator
// instance owns exactly one project today but the shape is a slice so Kyle
// can aggregate across projects without special-casing single-entry
// responses.
type ProjectDTO struct {
	Name        string `json:"name"`
	Language    string `json:"language"`
	OrchURL     string `json:"orch_url"`
	WorkerCount int    `json:"worker_count"`
}

// TaskDTO is the minimal task projection returned by the tasks endpoint.
// Callers that need full plan/context fields should query the orchestrator
// directly; the HTTP API intentionally returns a narrow shape so Kyle and
// the TUI do not become coupled to every field on the GORM model.
type TaskDTO struct {
	ID                    string                `json:"id"`
	Title                 string                `json:"title"`
	Status                string                `json:"status"`
	StateVersion          uint64                `json:"state_version"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
	AssignedWorker        string                `json:"assigned_worker"`
	ActiveAttemptCount    int                   `json:"active_attempt_count,omitempty"`
	ActiveAttempts        []TaskAttemptLeaseDTO `json:"active_attempts,omitempty"`
	Category              string                `json:"category,omitempty"`
	CurrentHealth         string                `json:"current_health,omitempty"`
	LatestFailureSummary  string                `json:"latest_failure_summary,omitempty"`
	LatestFailureType     string                `json:"latest_failure_type,omitempty"`
	LatestFailureAt       *time.Time            `json:"latest_failure_at,omitempty"`
	LatestFailureCurrent  *bool                 `json:"latest_failure_current,omitempty"`
	ReviewStatus          string                `json:"review_status,omitempty"`
	ReviewDetail          string                `json:"review_detail,omitempty"`
	ReviewRecommendation  string                `json:"review_recommendation,omitempty"`
	ReviewCoverage        string                `json:"review_coverage,omitempty"`
	ReviewIssues          []string              `json:"review_issues,omitempty"`
	ReviewFileOverlapRisk string                `json:"review_file_overlap_risk,omitempty"`
	ReviewIntegrationGap  *bool                 `json:"review_integration_gap,omitempty"`
}

// TaskSpecDTO is the authenticated task-creation contract used by Codex after
// observing a reference workflow. It intentionally carries artifact
// identifiers and hashes, never local media paths or media bodies.
type TaskSpecDTO struct {
	Title              string                       `json:"title"`
	Description        string                       `json:"description"`
	Actor              string                       `json:"actor"`
	IdempotencyKey     string                       `json:"idempotency_key"`
	Observation        *ReferenceObservationDTO     `json:"observation"`
	AcceptanceCriteria []TaskAcceptanceCriterionDTO `json:"acceptance_criteria"`
	IntegrationSeams   []TaskIntegrationSeamDTO     `json:"integration_seams,omitempty"`
	ProposedScope      []string                     `json:"proposed_scope"`
	Exclusions         []string                     `json:"exclusions"`
	Dependencies       []string                     `json:"dependencies,omitempty"`
	Uncertainty        []string                     `json:"uncertainty,omitempty"`
	OpenQuestions      []string                     `json:"open_questions,omitempty"`
	ExecutionPlan      *TaskExecutionPlanDTO        `json:"execution_plan,omitempty"`
}

// TaskIntegrationSeamDTO proves how a user-visible criterion reaches the
// production entrypoint. Source evidence is submitted as a content-addressed
// excerpt; missing edges name every file that must be in both task scope and
// the integration subtask. This turns call-chain completeness into an
// admission contract instead of a reviewer guess.
type TaskIntegrationSeamDTO struct {
	ID                    string                   `json:"id"`
	AcceptanceCriteriaIDs []string                 `json:"acceptance_criteria_ids"`
	EntryPoint            string                   `json:"entry_point"`
	SourceEvidence        []TaskSourceEvidenceDTO  `json:"source_evidence"`
	MissingEdges          []TaskIntegrationEdgeDTO `json:"missing_edges"`
	VerificationLevel     string                   `json:"verification_level"`
	VerificationSteps     []string                 `json:"verification_steps"`
}

type TaskSourceEvidenceDTO struct {
	Path          string `json:"path"`
	Symbol        string `json:"symbol"`
	Excerpt       string `json:"excerpt"`
	ExcerptSHA256 string `json:"excerpt_sha256"`
}

type TaskIntegrationEdgeDTO struct {
	Description   string   `json:"description"`
	RequiredFiles []string `json:"required_files"`
}

// TaskExecutionPlanDTO is an optional adapter-authored plan. Supplying it
// lets a trusted Codex adapter spend its repository/reference context once,
// while the orchestrator still validates and reviews the plan before any
// worker runs. Specs without this field retain the classifier/planner path.
type TaskExecutionPlanDTO struct {
	Subtasks      []TaskExecutionSubtaskDTO `json:"subtasks"`
	TDDExceptions []TaskTDDExceptionDTO     `json:"tdd_exceptions,omitempty"`
	Assumptions   []TaskPlanAssumptionDTO   `json:"assumptions,omitempty"`
}

type TaskExecutionSubtaskDTO struct {
	Title              string                     `json:"title"`
	Description        string                     `json:"description"`
	AgentType          string                     `json:"agent_type,omitempty"`
	Files              []string                   `json:"files"`
	Dependencies       []int                      `json:"dependencies,omitempty"`
	Priority           int                        `json:"priority,omitempty"`
	Phase              string                     `json:"phase"`
	TestsFor           []int                      `json:"tests_for,omitempty"`
	ModuleBoundaries   []TaskModuleBoundaryDTO    `json:"module_boundaries,omitempty"`
	InterfaceShapes    []TaskInterfaceShapeDTO    `json:"interface_shapes,omitempty"`
	InterfaceContracts []TaskInterfaceContractDTO `json:"interface_contracts,omitempty"`
}

// TaskModuleBoundaryDTO and TaskInterfaceShapeDTO make the adapter commit to
// the same implementation depth metadata that the SGLang plan reviewer
// evaluates. They are wire-only duplicates of the internal plan model so the
// public DTO package remains dependency-free.
type TaskModuleBoundaryDTO struct {
	Package     string `json:"package"`
	Description string `json:"description"`
	Exports     int    `json:"exports"`
}

type TaskInterfaceShapeDTO struct {
	Package   string   `json:"package"`
	Functions []string `json:"functions"`
	Types     []string `json:"types"`
}

// TaskInterfaceContractDTO describes one semantic seam instead of overloading
// a C++-looking string. State is one of existing, planned, or missing. Kind is
// one of cpp_function, cpp_type, registry_action, keymap_route, or call_edge.
// Fields that do not apply to a kind are omitted.
type TaskInterfaceContractDTO struct {
	Package           string `json:"package"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	OwnerFile         string `json:"owner_file"`
	Signature         string `json:"signature,omitempty"`
	Symbol            string `json:"symbol,omitempty"`
	ActionID          string `json:"action_id,omitempty"`
	CallbackSignature string `json:"callback_signature,omitempty"`
	Route             string `json:"route,omitempty"`
	TargetAction      string `json:"target_action,omitempty"`
	Caller            string `json:"caller,omitempty"`
	Callee            string `json:"callee,omitempty"`
}

type TaskTDDExceptionDTO struct {
	SubtaskIndex int    `json:"subtask_index"`
	Reason       string `json:"reason"`
}

type TaskPlanAssumptionDTO struct {
	Decision     string   `json:"decision"`
	Alternatives []string `json:"alternatives,omitempty"`
	WhyChosen    string   `json:"why_chosen"`
}

// ReviseTaskPlanRequest replaces only the mutable execution plan of a task
// parked at plan_review. The immutable TaskSpecification remains the source
// of truth for scope and acceptance criteria; the server validates the new
// plan against it before dispatching the guarded mutation.
type ReviseTaskPlanRequest struct {
	ExecutionPlan TaskExecutionPlanDTO `json:"execution_plan"`
	Reason        string               `json:"reason"`
}

// ReferenceObservationDTO captures the reproducible Cubase workflow that
// motivates a Canvas task. Steps are ordered by their position in the slice.
type ReferenceObservationDTO struct {
	SessionID          string                     `json:"session_id"`
	Product            string                     `json:"product"`
	ProductVersion     string                     `json:"product_version"`
	OS                 string                     `json:"os"`
	DisplayEnvironment string                     `json:"display_environment"`
	ObservedAt         time.Time                  `json:"observed_at"`
	ObserverActor      string                     `json:"observer_actor"`
	Preconditions      []string                   `json:"preconditions"`
	Steps              []ReferenceWorkflowStepDTO `json:"steps"`
	ExpectedBehavior   []string                   `json:"expected_behavior"`
	NegativeBehavior   []string                   `json:"negative_behavior"`
	Evidence           []ObservationEvidenceDTO   `json:"evidence"`
}

type ReferenceWorkflowStepDTO struct {
	Action                string `json:"action"`
	Target                string `json:"target,omitempty"`
	ExpectedVisibleResult string `json:"expected_visible_result"`
}

type ObservationEvidenceDTO struct {
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	MediaType  string `json:"media_type"`
	Purpose    string `json:"purpose"`
}

type TaskAcceptanceCriterionDTO struct {
	ID                string   `json:"id"`
	Description       string   `json:"description"`
	VerificationSteps []string `json:"verification_steps"`
	ExpectedBehavior  []string `json:"expected_behavior"`
	NegativeBehavior  []string `json:"negative_behavior,omitempty"`
}

// CommandEvidenceDTO is one auditable command invocation used by the delivery
// protocol. Times are caller-observed and retained with the verification.
type CommandEvidenceDTO struct {
	Command    string    `json:"command"`
	Passed     bool      `json:"passed"`
	ExitCode   int       `json:"exit_code"`
	Output     string    `json:"output,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type DeliveryArtifactDTO struct {
	ID              string               `json:"id"`
	TaskID          string               `json:"task_id"`
	ArtifactVersion uint64               `json:"artifact_version"`
	Branch          string               `json:"branch"`
	CommitSHA       string               `json:"commit_sha"`
	BaseBranch      string               `json:"base_branch"`
	BaseSHA         string               `json:"base_sha"`
	Preliminary     []CommandEvidenceDTO `json:"preliminary_evidence"`
	CreatorActor    string               `json:"creator_actor"`
	CreatorSource   string               `json:"creator_source"`
	CreatedAt       time.Time            `json:"created_at"`
}

type VerificationRecordDTO struct {
	ID                     string                       `json:"id"`
	ArtifactID             string                       `json:"artifact_id"`
	ArtifactVersion        uint64                       `json:"artifact_version"`
	CommitSHA              string                       `json:"commit_sha"`
	VerifierActor          string                       `json:"verifier_actor"`
	EnvironmentFingerprint string                       `json:"environment_fingerprint"`
	Commands               []CommandEvidenceDTO         `json:"commands"`
	BinarySHA256           string                       `json:"binary_sha256,omitempty"`
	Result                 string                       `json:"result"`
	Notes                  string                       `json:"notes,omitempty"`
	Interactions           []VerificationInteractionDTO `json:"interactions,omitempty"`
	CreatedAt              time.Time                    `json:"created_at"`
}

type InteractionStepDTO struct {
	Action   string `json:"action"`
	Observed string `json:"observed"`
}

type InteractionEvidenceRefDTO struct {
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	MediaType  string `json:"media_type"`
}

type VerificationInteractionDTO struct {
	ID                    string                      `json:"id,omitempty"`
	AcceptanceCriterionID string                      `json:"acceptance_criterion_id"`
	ScenarioName          string                      `json:"scenario_name"`
	Steps                 []InteractionStepDTO        `json:"steps"`
	ObservedResult        string                      `json:"observed_result"`
	EvidenceRefs          []InteractionEvidenceRefDTO `json:"evidence_refs"`
	ApplicationVersion    string                      `json:"application_version"`
	HostEnvironment       string                      `json:"host_environment"`
	RunPID                int                         `json:"run_pid"`
	Result                string                      `json:"result"`
	Discrepancy           string                      `json:"discrepancy,omitempty"`
	CreatedAt             time.Time                   `json:"created_at,omitempty"`
}

type HostDirectAttestationDTO struct {
	AcceptanceCriteriaUnchanged bool `json:"acceptance_criteria_unchanged"`
	DependencyShapeUnchanged    bool `json:"dependency_shape_unchanged"`
	NoPersistenceOrSchema       bool `json:"no_persistence_or_schema"`
	NoSecurityOrAuth            bool `json:"no_security_or_auth"`
	NoCrossProcessOwnership     bool `json:"no_cross_process_ownership"`
	NoBuildOrReleasePolicy      bool `json:"no_build_or_release_policy"`
}

// DeliveryEnvelopeDTO is the single read contract a host verifier needs.
type DeliveryEnvelopeDTO struct {
	Task               TaskDTO                `json:"task"`
	Artifact           DeliveryArtifactDTO    `json:"artifact"`
	LatestVerification *VerificationRecordDTO `json:"latest_verification,omitempty"`
}

type VerifyDeliveryRequest struct {
	ObservedStateVersion   uint64                       `json:"observed_state_version"`
	ArtifactVersion        uint64                       `json:"artifact_version"`
	CommitSHA              string                       `json:"commit_sha"`
	Actor                  string                       `json:"actor"`
	EnvironmentFingerprint string                       `json:"environment_fingerprint"`
	Commands               []CommandEvidenceDTO         `json:"commands"`
	BinarySHA256           string                       `json:"binary_sha256,omitempty"`
	Result                 string                       `json:"result"`
	Notes                  string                       `json:"notes,omitempty"`
	Interactions           []VerificationInteractionDTO `json:"interactions,omitempty"`
	FailureMode            string                       `json:"failure_mode,omitempty"`
	FailureReason          string                       `json:"failure_reason,omitempty"`
	AllowedScope           []string                     `json:"allowed_scope,omitempty"`
	HostDirectAttestation  HostDirectAttestationDTO     `json:"host_direct_attestation,omitempty"`
	IdempotencyKey         string                       `json:"idempotency_key"`
}

type IntegrateDeliveryRequest struct {
	ObservedStateVersion uint64 `json:"observed_state_version"`
	ArtifactVersion      uint64 `json:"artifact_version"`
	CommitSHA            string `json:"commit_sha"`
	VerificationRecordID string `json:"verification_record_id"`
	Actor                string `json:"actor"`
	IdempotencyKey       string `json:"idempotency_key"`
}

type IntegrationAuthorizationDTO struct {
	ID              string `json:"integration_authorization_id"`
	TaskID          string `json:"task_id"`
	ArtifactVersion uint64 `json:"artifact_version"`
	CommitSHA       string `json:"commit_sha"`
}

type RequestDeliveryReworkRequest struct {
	ObservedStateVersion  uint64                   `json:"observed_state_version"`
	ArtifactVersion       uint64                   `json:"artifact_version"`
	CommitSHA             string                   `json:"commit_sha"`
	Actor                 string                   `json:"actor"`
	Reason                string                   `json:"reason"`
	Mode                  string                   `json:"mode"`
	AllowedScope          []string                 `json:"allowed_scope,omitempty"`
	HostDirectAttestation HostDirectAttestationDTO `json:"host_direct_attestation,omitempty"`
	IdempotencyKey        string                   `json:"idempotency_key"`
}

type DeliveryReworkRecordDTO struct {
	ID                  string `json:"rework_record_id"`
	TaskID              string `json:"task_id"`
	ArtifactVersion     uint64 `json:"artifact_version"`
	CommitSHA           string `json:"commit_sha"`
	Reason              string `json:"reason"`
	Mode                string `json:"mode"`
	HostReworkSessionID string `json:"host_rework_session_id,omitempty"`
}

type SubmitHostReworkRequest struct {
	ObservedStateVersion uint64 `json:"observed_state_version"`
	SessionID            string `json:"session_id"`
	CommitSHA            string `json:"commit_sha"`
	Actor                string `json:"actor"`
	IdempotencyKey       string `json:"idempotency_key"`
}

type HostReworkSubmissionDTO struct {
	ID                   string    `json:"submission_id"`
	SessionID            string    `json:"session_id"`
	TaskID               string    `json:"task_id"`
	PriorCommitSHA       string    `json:"prior_commit_sha"`
	ReplacementCommitSHA string    `json:"replacement_commit_sha"`
	ChangedPaths         []string  `json:"changed_paths"`
	CreatedAt            time.Time `json:"created_at"`
}

type AbandonHostReworkRequest struct {
	ObservedStateVersion uint64 `json:"observed_state_version"`
	SessionID            string `json:"session_id"`
	Actor                string `json:"actor"`
	Reason               string `json:"reason"`
	IdempotencyKey       string `json:"idempotency_key"`
}

type HostReworkSessionDTO struct {
	ID                   string     `json:"session_id"`
	TaskID               string     `json:"task_id"`
	OwnerActor           string     `json:"owner_actor"`
	Disposition          string     `json:"disposition"`
	PriorCommitSHA       string     `json:"prior_commit_sha"`
	ReplacementCommitSHA string     `json:"replacement_commit_sha,omitempty"`
	FinishedAt           *time.Time `json:"finished_at,omitempty"`
}

// TaskAttemptLeaseDTO describes the currently active execution lease for a
// task. It is intentionally small so task list consumers can distinguish a
// live/reserved attempt from a historical assignment without fetching the full
// attempt history endpoint.
type TaskAttemptLeaseDTO struct {
	AttemptID   string     `json:"attempt_id"`
	TaskID      string     `json:"task_id,omitempty"`
	WorkerID    string     `json:"worker_id,omitempty"`
	AgentID     string     `json:"agent_id,omitempty"`
	ContainerID string     `json:"container_id,omitempty"`
	Role        string     `json:"role,omitempty"`
	Branch      string     `json:"branch,omitempty"`
	LeaseState  string     `json:"lease_state,omitempty"`
	LeaseOwner  string     `json:"lease_owner,omitempty"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	StartedAt   time.Time  `json:"started_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at,omitempty"`
}

// TaskCommentDTO is the public projection returned after appending a task
// comment through the HTTP API.
type TaskCommentDTO struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkerDTO describes a single worker (agent) — its container, branch,
// current task, and liveness timestamps. Returned by /workers/:id and
// /projects/:name/workers.
type WorkerDTO struct {
	ID                   string     `json:"id"`
	ContainerID          string     `json:"container_id"`
	Project              string     `json:"project"`
	AgentType            string     `json:"agent_type"`
	Branch               string     `json:"branch"`
	Status               string     `json:"status"`
	StartedAt            time.Time  `json:"started_at"`
	LastHeartbeat        time.Time  `json:"last_heartbeat"`
	CurrentTask          string     `json:"current_task"`
	Provider             string     `json:"provider"`
	ModelID              string     `json:"model_id"`
	Effort               string     `json:"effort"`
	CompletedAt          *time.Time `json:"completed_at"`
	ExitReason           string     `json:"exit_reason"`
	TotalCostUSD         float64    `json:"total_cost_usd"`
	FinalContextPct      int        `json:"final_context_pct"`
	TokensIn             int        `json:"tokens_in"`
	TokensOut            int        `json:"tokens_out"`
	ConstraintViolations int        `json:"constraint_violations"`
}

// WorkerAttemptDTO is the public projection of a task execution attempt
// attributed to a worker/container. New attempts are backed by a durable
// WorkerAttempt row; older history may still be projected from spawn events
// or Agent rows.
type WorkerAttemptDTO struct {
	AttemptID             string     `json:"attempt_id"`
	TaskID                string     `json:"task_id"`
	WorkerID              string     `json:"worker_id"`
	AgentID               string     `json:"agent_id"`
	Source                string     `json:"source,omitempty"`
	SourceEventID         string     `json:"source_event_id,omitempty"`
	ContainerID           string     `json:"container_id"`
	WorkerLabel           string     `json:"worker_label"`
	AgentType             string     `json:"agent_type"`
	Branch                string     `json:"branch"`
	LeaseState            string     `json:"lease_state,omitempty"`
	LeaseOwner            string     `json:"lease_owner,omitempty"`
	LeaseUntil            *time.Time `json:"lease_until,omitempty"`
	Provider              string     `json:"provider"`
	ModelID               string     `json:"model_id"`
	Effort                string     `json:"effort"`
	Status                string     `json:"status"`
	StartedAt             time.Time  `json:"started_at"`
	CompletedAt           *time.Time `json:"completed_at"`
	LastHeartbeat         time.Time  `json:"last_heartbeat"`
	ExitReason            string     `json:"exit_reason"`
	FailureClassification string     `json:"failure_classification"`
	FirstError            string     `json:"first_error"`
	FailedAt              *time.Time `json:"failed_at,omitempty"`
	TokensIn              int        `json:"tokens_in"`
	TokensOut             int        `json:"tokens_out"`
	TotalCostUSD          float64    `json:"total_cost_usd"`
	FinalContextPct       int        `json:"final_context_pct"`
	ConstraintViolations  int        `json:"constraint_violations"`
	ArtifactURI           string     `json:"artifact_uri,omitempty"`
}

// AdoptFailedChildRequest identifies the exact repaired head a host Codex
// task wants the orchestrator to admit in place of a rejected worker result.
type AdoptFailedChildRequest struct {
	CommitSHA string `json:"commit_sha"`
}

// WorkerHistoryDTO wraps a worker's recent state transitions and exit
// events. The Events slice is ordered oldest-first.
type WorkerHistoryDTO struct {
	WorkerID string               `json:"worker_id"`
	Events   []WorkerHistoryEntry `json:"events"`
}

// WorkerHistoryEntry is one row of a worker's transition log.
type WorkerHistoryEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`
	Detail    string          `json:"detail"`
	ExitCode  int             `json:"exit_code"`
	Details   json.RawMessage `json:"details,omitempty"`
}

// EventDTO is one structured event emitted by agentmon and surfaced via
// GET /events. Payload is left as a raw JSON message so new event types
// can be added without recompiling clients that only care about Timestamp
// and Type.
type EventDTO struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// HealthIssueDTO is one operator-facing health finding from the orchestrator.
// Findings are intentionally read-only. RecommendedAction may name a narrow
// dry-run recovery or inspection step; mutations use separate endpoints.
type HealthIssueDTO struct {
	Type                string                 `json:"type"`
	Severity            string                 `json:"severity"`
	TaskID              string                 `json:"task_id,omitempty"`
	WorkerID            string                 `json:"worker_id,omitempty"`
	Role                string                 `json:"role,omitempty"`
	Branch              string                 `json:"branch,omitempty"`
	AttemptIDs          []string               `json:"attempt_ids,omitempty"`
	BlockedDependencies []BlockedDependencyDTO `json:"blocked_dependencies,omitempty"`
	GateFailure         *GateFailureDTO        `json:"gate_failure,omitempty"`
	Status              string                 `json:"status,omitempty"`
	DetectedAt          time.Time              `json:"detected_at"`
	AgeSeconds          int64                  `json:"age_seconds,omitempty"`
	LastEvent           *time.Time             `json:"last_event,omitempty"`
	Message             string                 `json:"message"`
	RecommendedAction   string                 `json:"recommended_action,omitempty"`
}

// BlockedDependencyDTO is a structured parent-readiness blocker surfaced on
// health issues so operators do not need to inspect task context JSON.
type BlockedDependencyDTO struct {
	TaskID       string `json:"task_id,omitempty"`
	DependencyID string `json:"dependency_id,omitempty"`
	Phase        string `json:"phase,omitempty"`
	Status       string `json:"status,omitempty"`
	Message      string `json:"message,omitempty"`
}

// GateFailureDTO describes current branch hygiene or acceptance gate evidence.
type GateFailureDTO struct {
	Gate    string `json:"gate,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// StaleAssignmentRecoveryRequest asks the orchestrator to classify or repair a
// single task assignment. Exactly one of DryRun or Apply must be true.
type StaleAssignmentRecoveryRequest struct {
	DryRun            bool       `json:"dry_run"`
	Apply             bool       `json:"apply"`
	Actor             string     `json:"actor,omitempty"`
	ObservedStatus    string     `json:"observed_status,omitempty"`
	ObservedUpdatedAt *time.Time `json:"observed_updated_at,omitempty"`
}

// StaleAssignmentRecoveryDTO is returned by successful stale-assignment
// recovery classification or repair. Unsafe live assignments return 409.
type StaleAssignmentRecoveryDTO struct {
	TaskID         string `json:"task_id"`
	Status         string `json:"status"`
	AssignedWorker string `json:"assigned_worker,omitempty"`
	WorkerStatus   string `json:"worker_status,omitempty"`
	Classification string `json:"classification"`
	Safe           bool   `json:"safe"`
	Applied        bool   `json:"applied"`
	Message        string `json:"message"`
}

// TaskRecoveryRequest asks the orchestrator to classify or apply one narrow
// break-glass recovery action. Unsupported actions return a refusal result.
type TaskRecoveryRequest struct {
	DryRun            bool       `json:"dry_run"`
	Apply             bool       `json:"apply"`
	Actor             string     `json:"actor,omitempty"`
	ObservedStatus    string     `json:"observed_status,omitempty"`
	ObservedUpdatedAt *time.Time `json:"observed_updated_at,omitempty"`
}

// TaskRecoveryDTO describes why a recovery action would apply or be refused.
type TaskRecoveryDTO struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	Action        string `json:"action"`
	Safe          bool   `json:"safe"`
	Applied       bool   `json:"applied"`
	RefusalReason string `json:"refusal_reason,omitempty"`
	Policy        string `json:"policy"`
	Evidence      string `json:"evidence"`
	Result        string `json:"result"`
	Message       string `json:"message"`
	AffectedCount int    `json:"affected_count,omitempty"`
}

// RecoveryAuditRequest is the public payload accepted by Kyle's narrow
// recovery audit endpoint. It records why an autonomous action was allowed and
// what result it produced without exposing a generic task-event write surface.
type RecoveryAuditRequest struct {
	Actor          string `json:"actor"`
	PolicyRule     string `json:"policy_rule"`
	Evidence       string `json:"evidence"`
	Surface        string `json:"surface"`
	Action         string `json:"action"`
	Result         string `json:"result"`
	NextFollowUp   string `json:"next_follow_up"`
	SupportedPath  bool   `json:"supported_path"`
	BreakGlassPath bool   `json:"break_glass_path"`
}

// IngestRequest is the body accepted by POST /internal/logs. Each element
// of Records is a discriminated-union record tagged by a "type" field; the
// server decodes and routes per record.
type IngestRequest struct {
	Records []json.RawMessage `json:"records"`
}

// IngestResponse is returned (with status 202) once all records in a batch
// have been written. The count mirrors len(IngestRequest.Records) on
// success; all-or-nothing semantics mean a partial count is never reported.
type IngestResponse struct {
	Accepted int `json:"accepted"`
}
