package model

import (
	"time"

	"github.com/google/uuid"
)

// Project represents a top-level orchestrated repository.
type Project struct {
	ID            uuid.UUID `gorm:"type:text;primaryKey"`
	Name          string    `gorm:"uniqueIndex;not null"`
	BareRepoPath  string    `gorm:"not null"`
	DefaultBranch string    `gorm:"default:master"`
	Language      string    `gorm:"default:go;index"`
	Description   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Tasks         []Task               `gorm:"foreignKey:ProjectID"`
	Agents        []Agent              `gorm:"foreignKey:ProjectID"`
	PromptAssets  []ProjectPromptAsset `gorm:"foreignKey:ProjectID"`
}

// ProjectPromptAsset stores project-scoped prompt/template fragments seeded
// from source defaults and then owned by the project DB for operational edits.
type ProjectPromptAsset struct {
	ID          uuid.UUID `gorm:"type:text;primaryKey"`
	ProjectID   uuid.UUID `gorm:"type:text;not null;index;uniqueIndex:idx_project_prompt_asset"`
	Kind        string    `gorm:"not null;uniqueIndex:idx_project_prompt_asset"`
	Name        string    `gorm:"not null;uniqueIndex:idx_project_prompt_asset"`
	Language    string    `gorm:"not null;index"`
	Content     string    `gorm:"type:text;not null"`
	ContentHash string    `gorm:"not null"`
	Version     string    `gorm:"not null"`
	Active      bool      `gorm:"default:true;index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Task represents a unit of work tracked by the orchestrator.
type Task struct {
	ID              uuid.UUID    `gorm:"type:text;primaryKey"`
	ProjectID       uuid.UUID    `gorm:"type:text;not null;index"`
	ParentTaskID    *uuid.UUID   `gorm:"type:text;index"`
	Title           string       `gorm:"not null"`
	Description     string       `gorm:"not null"`
	Status          TaskStatus   `gorm:"not null;default:backlog"`
	StateVersion    uint64       `gorm:"not null;default:1"`
	Category        TaskCategory `gorm:"not null;default:standard"`
	Priority        int          `gorm:"default:0"`
	ComplexityScore int          `gorm:"default:0"`
	Labels          JSONArray    `gorm:"type:text"`
	DependencyIDs   JSONArray    `gorm:"type:text"`
	AssignedAgentID *uuid.UUID   `gorm:"type:text"`
	Plan            JSONField    `gorm:"type:text"`
	PlanFeedback    string
	TestPlan        string
	TestFeedback    string
	WorktreeBranch  string
	WorktreeBaseSHA string
	PRUrl           string

	// TDD fields (used for subtasks)
	Phase    string    `gorm:"default:''"` // "test", "implementation", "integration", or ""
	TestsFor JSONArray `gorm:"type:text"`  // indices of impl subtasks this test covers (test-phase only)

	// TDD fields (used for parent tasks)
	TDDExceptions    JSONField `gorm:"type:text"`     // planner-declared TDD exceptions
	NeedsHumanReview bool      `gorm:"default:false"` // set when fixer escalates to human

	Context JSONField `gorm:"type:text"`

	CreatedAt     time.Time
	UpdatedAt     time.Time
	Project       Project       `gorm:"foreignKey:ProjectID"`
	ParentTask    *Task         `gorm:"foreignKey:ParentTaskID"`
	Subtasks      []Task        `gorm:"foreignKey:ParentTaskID"`
	AssignedAgent *Agent        `gorm:"foreignKey:AssignedAgentID"`
	Events        []TaskEvent   `gorm:"foreignKey:TaskID"`
	Comments      []TaskComment `gorm:"foreignKey:TaskID"`
}

// BranchAcceptanceRecord is append-only typed evidence of the worker branch
// admission decision. Task.Context may mirror it for compatibility but is not
// authoritative for delivery.
type BranchAcceptanceRecord struct {
	ID         uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID     uuid.UUID `gorm:"type:text;not null;index"`
	AgentID    uuid.UUID `gorm:"type:text;not null;index"`
	Branch     string    `gorm:"not null;index"`
	Accepted   bool      `gorm:"not null;index"`
	BaseBranch string    `gorm:"not null"`
	BaseSHA    string    `gorm:"not null"`
	HeadSHA    string    `gorm:"not null;index"`
	Details    JSONField `gorm:"type:text;not null"`
	Actor      string    `gorm:"not null"`
	Source     string    `gorm:"not null"`
	CreatedAt  time.Time
}

// DeliveryArtifact freezes the exact branch commit and target base that move
// from container-safe testing to host-authoritative verification. Artifact
// versions are monotonic per task; invalidation is retained as audit history.
type DeliveryArtifact struct {
	ID                   uuid.UUID  `gorm:"type:text;primaryKey"`
	TaskID               uuid.UUID  `gorm:"type:text;not null;index;uniqueIndex:idx_delivery_artifact_task_version"`
	PreliminaryGateRunID *uuid.UUID `gorm:"type:text;index"` // nil only for pre-migration history
	ArtifactVersion      uint64     `gorm:"not null;uniqueIndex:idx_delivery_artifact_task_version"`
	Branch               string     `gorm:"not null"`
	CommitSHA            string     `gorm:"not null;index"`
	BaseBranch           string     `gorm:"not null"`
	BaseSHA              string     `gorm:"not null"`
	PreliminaryEvidence  JSONField  `gorm:"type:text;not null"`
	CreatorActor         string     `gorm:"not null"`
	CreatorSource        string     `gorm:"not null"`
	InvalidationReason   string
	InvalidatedAt        *time.Time `gorm:"index"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// PreliminaryGateRun is append-only evidence that the deterministic gate ran
// in an isolated checkout at the exact accepted head and base. A passing run
// is created in the same transaction as the artifact it authorizes.
type PreliminaryGateRun struct {
	ID                     uuid.UUID              `gorm:"type:text;primaryKey"`
	TaskID                 uuid.UUID              `gorm:"type:text;not null;index"`
	Branch                 string                 `gorm:"not null"`
	CommitSHA              string                 `gorm:"not null;index"`
	BaseBranch             string                 `gorm:"not null"`
	BaseSHA                string                 `gorm:"not null"`
	WorkspaceID            string                 `gorm:"not null"`
	EnvironmentFingerprint string                 `gorm:"not null"`
	CommandEvidence        JSONField              `gorm:"type:text;not null"`
	Outcome                PreliminaryGateOutcome `gorm:"not null;index"`
	Actor                  string                 `gorm:"not null"`
	Source                 string                 `gorm:"not null"`
	StartedAt              time.Time
	FinishedAt             time.Time
	CreatedAt              time.Time
}

// VerificationRecord is append-only native or automated verification
// evidence for one immutable delivery artifact.
type VerificationRecord struct {
	ID                     uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID                 uuid.UUID `gorm:"type:text;not null;index"`
	DeliveryArtifactID     uuid.UUID `gorm:"type:text;not null;index"`
	ArtifactVersion        uint64    `gorm:"not null"`
	CommitSHA              string    `gorm:"not null;index"`
	VerifierActor          string    `gorm:"not null"`
	EnvironmentFingerprint string    `gorm:"not null"`
	CommandEvidence        JSONField `gorm:"type:text;not null"`
	BinarySHA256           string
	Result                 VerificationResult `gorm:"not null;index"`
	Notes                  string
	IdempotencyKey         string `gorm:"not null;uniqueIndex"`
	RequestHash            string `gorm:"not null"`
	CreatedAt              time.Time
}

// IntegrationAuthorization records the explicit decision to permit default
// branch mutation for a verified artifact. Auto-merge policy creates the same
// record with an orchestrator actor.
type IntegrationAuthorization struct {
	ID                   uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID               uuid.UUID `gorm:"type:text;not null;index"`
	DeliveryArtifactID   uuid.UUID `gorm:"type:text;not null;index"`
	VerificationRecordID uuid.UUID `gorm:"type:text;not null;index"`
	ArtifactVersion      uint64    `gorm:"not null"`
	CommitSHA            string    `gorm:"not null"`
	BaseSHA              string    `gorm:"not null"`
	Actor                string    `gorm:"not null"`
	Source               string    `gorm:"not null"`
	IdempotencyKey       string    `gorm:"not null;uniqueIndex"`
	RequestHash          string    `gorm:"not null"`
	CreatedAt            time.Time
}

// DeliveryReworkRecord is the append-only audit record for an explicit
// decision to reject a current artifact without misrepresenting that decision
// as a failed command-based verification.
type DeliveryReworkRecord struct {
	ID                 uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID             uuid.UUID `gorm:"type:text;not null;index"`
	DeliveryArtifactID uuid.UUID `gorm:"type:text;not null;index"`
	ArtifactVersion    uint64    `gorm:"not null"`
	CommitSHA          string    `gorm:"not null"`
	Actor              string    `gorm:"not null"`
	Source             string    `gorm:"not null"`
	Reason             string    `gorm:"not null"`
	IdempotencyKey     string    `gorm:"not null;uniqueIndex"`
	RequestHash        string    `gorm:"not null"`
	CreatedAt          time.Time
}

// MergeCompletion is the immutable terminal link between the accepted
// delivery evidence and the commit written to the integration branch.
type MergeCompletion struct {
	ID                         uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID                     uuid.UUID `gorm:"type:text;not null;uniqueIndex"`
	DeliveryArtifactID         uuid.UUID `gorm:"type:text;not null;index"`
	VerificationRecordID       uuid.UUID `gorm:"type:text;not null;index"`
	IntegrationAuthorizationID uuid.UUID `gorm:"type:text;not null;index"`
	ArtifactCommitSHA          string    `gorm:"not null"`
	VerifiedBaseSHA            string    `gorm:"not null"`
	MergeCommitSHA             string    `gorm:"not null;index"`
	Actor                      string    `gorm:"not null"`
	Source                     string    `gorm:"not null"`
	CreatedAt                  time.Time
}

// Agent represents a Claude Code agent working on tasks.
type Agent struct {
	ID             uuid.UUID   `gorm:"type:text;primaryKey"`
	ProjectID      uuid.UUID   `gorm:"type:text;not null;index"`
	AgentType      AgentType   `gorm:"not null"`
	Name           string      `gorm:"not null"`
	Status         AgentStatus `gorm:"not null;default:idle"`
	CurrentTaskID  *uuid.UUID  `gorm:"type:text"`
	WorktreePath   string
	WorktreeBranch string
	TmuxSession    string
	MemorySummary  string
	HeartbeatAt    *time.Time
	Config         JSONField `gorm:"type:text"`

	// Enrichment fields (populated at spawn/completion time)
	Provider             string     `gorm:"column:provider;default:''"`         // "" means Claude (backwards compatible)
	ModelID              string     `gorm:"column:model_id;default:'';index"`   // model identifier (populated at spawn time)
	Effort               string     `gorm:"column:effort;default:''"`           // effort level (populated at spawn time)
	CompletedAt          *time.Time `gorm:"column:completed_at;index"`          // when agent finished (populated at completion)
	ExitReason           string     `gorm:"column:exit_reason;default:''"`      // why agent stopped (populated at completion)
	TotalCostUSD         float64    `gorm:"column:total_cost_usd;type:float"`   // total API cost in USD (populated at completion)
	FinalContextPct      int        `gorm:"column:final_context_pct;default:0"` // final context % (populated at completion)
	TokensIn             int        `gorm:"column:tokens_in;default:0"`         // total input tokens consumed (populated at completion)
	TokensOut            int        `gorm:"column:tokens_out;default:0"`        // total output tokens consumed (populated at completion)
	ConstraintViolations int        `gorm:"column:constraint_violations;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// WorkerAttempt is the durable identity for one spawned worker execution.
// Container logs are still physically scoped by runtime container, but this
// row is the stable task-attempt handle exposed by the HTTP API.
type WorkerAttempt struct {
	ID                      uuid.UUID  `gorm:"type:text;primaryKey"`
	TaskID                  uuid.UUID  `gorm:"type:text;not null;index"`
	AgentID                 *uuid.UUID `gorm:"type:text;index"`
	Source                  string     `gorm:"not null;default:'';index"`
	SourceEventID           *uuid.UUID `gorm:"type:text;index"`
	WorkerID                string     `gorm:"index"`
	ContainerID             string     `gorm:"index"`
	AgentType               string
	Branch                  string `gorm:"not null;default:'';index"`
	Image                   string
	State                   string     `gorm:"not null;default:'reserved';index"`
	LeaseOwner              string     `gorm:"not null;default:'';index"`
	LeaseExpiresAt          *time.Time `gorm:"index"`
	FailureClassification   string     `gorm:"not null;default:'';index"`
	FirstError              string     `gorm:"type:text"`
	FailedAt                *time.Time `gorm:"index"`
	TokensIn                int        `gorm:"default:0"`
	TokensOut               int        `gorm:"default:0"`
	TotalCostUSD            float64    `gorm:"type:float"`
	FinalContextPct         int        `gorm:"default:0"`
	ArtifactURI             string     `gorm:"not null;default:''"`
	ArtifactMetadataJSON    string     `gorm:"type:text"`
	CompletedAt             *time.Time `gorm:"index"`
	PromptAssetVersionsJSON string     `gorm:"type:text"`
	RenderedPromptHash      string
	RenderedPromptPath      string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

const (
	WorkerAttemptReserved   = "reserved"
	WorkerAttemptRunning    = "running"
	WorkerAttemptCompleted  = "completed"
	WorkerAttemptFailed     = "failed"
	WorkerAttemptAborted    = "aborted"
	WorkerAttemptSuperseded = "superseded"
)

// AttemptEvent records lifecycle events for a specific worker attempt. TaskID is
// duplicated intentionally so callers can query either a task's full attempt
// timeline or a single attempt without joining through worker_attempts.
type AttemptEvent struct {
	ID        uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID    uuid.UUID `gorm:"type:text;not null;index;index:idx_attempt_events_task_created,priority:1"`
	AttemptID uuid.UUID `gorm:"type:text;not null;index;index:idx_attempt_events_attempt_created,priority:1"`
	State     string    `gorm:"not null;default:'';index;index:idx_attempt_events_state_created,priority:1"`
	Type      string    `gorm:"not null;index;index:idx_attempt_events_type_created,priority:1"`
	Details   JSONField `gorm:"type:text"`
	Actor     string    `gorm:"not null;default:''"`
	CreatedAt time.Time `gorm:"index;index:idx_attempt_events_task_created,priority:2;index:idx_attempt_events_attempt_created,priority:2;index:idx_attempt_events_state_created,priority:2;index:idx_attempt_events_type_created,priority:2"`
}

// TaskEvent records a status change or other significant event on a task.
type TaskEvent struct {
	ID        uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID    uuid.UUID `gorm:"type:text;not null;index"`
	EventType string    `gorm:"not null"`
	OldValue  string
	NewValue  string
	Details   JSONField `gorm:"type:text"`
	Actor     string    `gorm:"not null"`
	CreatedAt time.Time
}

// Memory stores agent memory fragments for context persistence and compaction.
type Memory struct {
	ID         uuid.UUID  `gorm:"type:text;primaryKey"`
	AgentID    uuid.UUID  `gorm:"type:text;not null;index"`
	TaskID     *uuid.UUID `gorm:"type:text;index"`
	Content    string     `gorm:"not null"`
	MemoryType string     `gorm:"not null"`
	Metadata   JSONField  `gorm:"type:text"`
	CreatedAt  time.Time
}

// TaskComment stores a user or system comment on a task, forming a
// conversational thread that agents receive at spawn time.
type TaskComment struct {
	ID        uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID    uuid.UUID `gorm:"type:text;not null;index"`
	Author    string    `gorm:"not null"` // "user" or "system"
	Body      string    `gorm:"not null"`
	CreatedAt time.Time
}

// SubtaskPlan is the plan item produced by planner agents during task
// decomposition.
type SubtaskPlan struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AgentType      string   `json:"agent_type"`
	EstimatedFiles []string `json:"estimated_files"`
	Phase          string   `json:"phase,omitempty"`
	TestsFor       []int    `json:"tests_for,omitempty"`

	// Depth metadata (populated by planner when designing for depth)
	ModuleBoundaries []ModuleBoundary `json:"module_boundaries,omitempty"`
	InterfaceShapes  []InterfaceShape `json:"interface_shapes,omitempty"`
}

// ModuleBoundary describes a module boundary defined in a plan subtask.
// It captures the planner's intent about what a module encapsulates and
// where its boundary lies.
type ModuleBoundary struct {
	Package     string `json:"package"`     // e.g., "internal/constraints/depth"
	Description string `json:"description"` // what this module encapsulates
	Exports     int    `json:"exports"`     // expected number of exported symbols
}

// InterfaceShape describes the intended public interface of a module.
// It captures the planner's commitment to a specific API surface.
type InterfaceShape struct {
	Package   string   `json:"package"`   // e.g., "internal/constraints/depth"
	Functions []string `json:"functions"` // expected exported function signatures
	Types     []string `json:"types"`     // expected exported type names
}
