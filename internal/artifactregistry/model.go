// Package artifactregistry tracks artifact authority metadata for Drem's
// operating layer. Artifact bodies stay in their native storage; this package
// records whether those bodies are current, admissible, visible, and relevant.
package artifactregistry

import (
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

type ArtifactStatus string

const (
	StatusCandidate  ArtifactStatus = "candidate"
	StatusDraft      ArtifactStatus = "draft"
	StatusAccepted   ArtifactStatus = "accepted"
	StatusActive     ArtifactStatus = "active"
	StatusSuperseded ArtifactStatus = "superseded"
	StatusLegacy     ArtifactStatus = "legacy"
	StatusArchived   ArtifactStatus = "archived"
	StatusRejected   ArtifactStatus = "rejected"
	StatusScratch    ArtifactStatus = "scratch"
	StatusStale      ArtifactStatus = "stale"
	StatusUnknown    ArtifactStatus = "unknown"
)

type AuthorityClass string

const (
	AuthoritySourceOfTruth    AuthorityClass = "source_of_truth"
	AuthorityRegistryMetadata AuthorityClass = "registry_metadata"
	AuthorityDecision         AuthorityClass = "decision"
	AuthorityEvidence         AuthorityClass = "evidence"
	AuthorityHistorical       AuthorityClass = "historical_context"
	AuthorityDerivedSummary   AuthorityClass = "derived_summary"
	AuthorityTransient        AuthorityClass = "transient"
	AuthorityInadmissible     AuthorityClass = "inadmissible"
)

type EvidenceTrust string

const (
	TrustHigh        EvidenceTrust = "high"
	TrustMedium      EvidenceTrust = "medium"
	TrustLow         EvidenceTrust = "low"
	TrustUnknown     EvidenceTrust = "unknown"
	TrustInvalidated EvidenceTrust = "invalidated"
)

type ValidationStatus string

const (
	ValidationUnknown ValidationStatus = "unknown"
	ValidationValid   ValidationStatus = "valid"
	ValidationWarning ValidationStatus = "warning"
	ValidationInvalid ValidationStatus = "invalid"
)

type DirectiveStatus string

const (
	DirectiveActive     DirectiveStatus = "active"
	DirectivePaused     DirectiveStatus = "paused"
	DirectiveSuperseded DirectiveStatus = "superseded"
	DirectiveCompleted  DirectiveStatus = "completed"
	DirectiveRejected   DirectiveStatus = "rejected"
)

type AdmissionDecision string

const (
	DecisionAdmit               AdmissionDecision = "admit"
	DecisionAdmitMinimal        AdmissionDecision = "admit_minimal"
	DecisionAdmitHistorical     AdmissionDecision = "admit_as_historical_context"
	DecisionAdmitWeakEvidence   AdmissionDecision = "admit_as_weak_evidence"
	DecisionExcludeStale        AdmissionDecision = "exclude_stale"
	DecisionExcludeSuperseded   AdmissionDecision = "exclude_superseded"
	DecisionExcludeVisibility   AdmissionDecision = "exclude_visibility"
	DecisionExcludeIrrelevant   AdmissionDecision = "exclude_irrelevant"
	DecisionExcludeInadmissible AdmissionDecision = "exclude_inadmissible"
	DecisionEscalateConflict    AdmissionDecision = "escalate_conflict"
)

// Artifact is authoritative metadata for a content body that lives elsewhere.
type Artifact struct {
	ID               uuid.UUID       `gorm:"type:text;primaryKey"`
	ArtifactType     string          `gorm:"not null;index:idx_artifacts_type_status,priority:1"`
	ContentURI       string          `gorm:"not null;uniqueIndex"`
	ContentHash      string          `gorm:"index"`
	Title            string          `gorm:"not null"`
	Owner            string          `gorm:"index"`
	ProjectID        string          `gorm:"index:idx_artifacts_project_status,priority:1"`
	TaskID           string          `gorm:"index:idx_artifacts_task_status,priority:1"`
	PersonaScope     string          `gorm:"index:idx_artifacts_persona_status,priority:1"`
	AgentRoleScope   string          `gorm:"index:idx_artifacts_agent_status,priority:1"`
	WorkflowScope    string          `gorm:"index:idx_artifacts_workflow_status,priority:1"`
	TopicTags        model.JSONArray `gorm:"type:text"`
	Status           ArtifactStatus  `gorm:"not null;default:unknown;index:idx_artifacts_type_status,priority:2;index:idx_artifacts_project_status,priority:2;index:idx_artifacts_task_status,priority:2;index:idx_artifacts_persona_status,priority:2;index:idx_artifacts_agent_status,priority:2;index:idx_artifacts_workflow_status,priority:2"`
	AuthorityClass   AuthorityClass  `gorm:"not null;default:transient;index"`
	Admissibility    string          `gorm:"not null;default:unknown"`
	EvidenceTrust    EvidenceTrust   `gorm:"not null;default:unknown;index"`
	Confidence       EvidenceTrust   `gorm:"not null;default:unknown"`
	VisibilityScope  string          `gorm:"index"`
	ValidFrom        *time.Time      `gorm:"index"`
	ValidUntil       *time.Time      `gorm:"index"`
	LastSeenAt       *time.Time
	LastValidatedAt  *time.Time
	ValidationStatus ValidationStatus `gorm:"not null;default:unknown;index"`
	ValidationErrors model.JSONArray  `gorm:"type:text"`
	CanonicalID      *uuid.UUID       `gorm:"type:text;index"`
	SupersedesID     *uuid.UUID       `gorm:"type:text;index"`
	SupersededByID   *uuid.UUID       `gorm:"type:text;index"`
	StalenessReason  string
	LegacyReason     string
	Metadata         model.JSONField `gorm:"type:text"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (Artifact) TableName() string { return "artifact_registry_artifacts" }

// ArtifactLink records a typed relationship between two artifacts.
type ArtifactLink struct {
	ID               uuid.UUID `gorm:"type:text;primaryKey"`
	SourceArtifactID uuid.UUID `gorm:"type:text;not null;index:idx_artifact_links_source_type,priority:1"`
	TargetArtifactID uuid.UUID `gorm:"type:text;not null;index"`
	LinkType         string    `gorm:"not null;index:idx_artifact_links_source_type,priority:2"`
	Reason           string
	Confidence       EvidenceTrust `gorm:"not null;default:unknown"`
	CreatedAt        time.Time
}

func (ArtifactLink) TableName() string { return "artifact_registry_artifact_links" }

// Directive represents a larger operator directive, goal, or operating intent.
type Directive struct {
	ID                    uuid.UUID       `gorm:"type:text;primaryKey"`
	DirectiveType         string          `gorm:"not null;index:idx_directives_type_status,priority:1"`
	Title                 string          `gorm:"not null"`
	Description           string          `gorm:"type:text"`
	Owner                 string          `gorm:"index"`
	Status                DirectiveStatus `gorm:"not null;default:active;index:idx_directives_type_status,priority:2"`
	Priority              int             `gorm:"default:0;index"`
	AuthorityRank         int             `gorm:"default:0;index"`
	ScopeType             string          `gorm:"index:idx_directives_scope,priority:1"`
	ScopeKey              string          `gorm:"index:idx_directives_scope,priority:2"`
	AcceptedBy            string
	AcceptedAt            *time.Time
	ValidFrom             *time.Time
	ValidUntil            *time.Time
	SuccessCriteria       string `gorm:"type:text"`
	NonGoals              string `gorm:"type:text"`
	SupersededByDirective *uuid.UUID
	SourceArtifactID      *uuid.UUID      `gorm:"type:text;index"`
	Metadata              model.JSONField `gorm:"type:text"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (Directive) TableName() string { return "artifact_registry_directives" }

// ArtifactDirectiveLink records how an artifact relates to a directive.
type ArtifactDirectiveLink struct {
	ID          uuid.UUID     `gorm:"type:text;primaryKey"`
	ArtifactID  uuid.UUID     `gorm:"type:text;not null;index:idx_artifact_directive_artifact,priority:1"`
	DirectiveID uuid.UUID     `gorm:"type:text;not null;index:idx_artifact_directive_directive,priority:1"`
	LinkType    string        `gorm:"not null;index:idx_artifact_directive_artifact,priority:2;index:idx_artifact_directive_directive,priority:2"`
	Strength    int           `gorm:"default:0"`
	Rationale   string        `gorm:"type:text"`
	Confidence  EvidenceTrust `gorm:"not null;default:unknown"`
	CreatedAt   time.Time
}

func (ArtifactDirectiveLink) TableName() string { return "artifact_registry_artifact_directive_links" }

// ContextAdmissionDecision records why an artifact was admitted or excluded
// for a specific persona or worker turn.
type ContextAdmissionDecision struct {
	ID                 uuid.UUID         `gorm:"type:text;primaryKey"`
	ContextPacketID    uuid.UUID         `gorm:"type:text;index"`
	TaskID             string            `gorm:"index"`
	ProjectID          string            `gorm:"index"`
	Persona            string            `gorm:"index"`
	AgentRole          string            `gorm:"index"`
	WorkflowStage      string            `gorm:"index"`
	ArtifactID         uuid.UUID         `gorm:"type:text;not null;index"`
	Decision           AdmissionDecision `gorm:"not null;index"`
	Reason             string            `gorm:"type:text"`
	TrustAtAdmission   EvidenceTrust     `gorm:"not null;default:unknown"`
	GoalRelevanceScore int               `gorm:"default:0"`
	Metadata           model.JSONField   `gorm:"type:text"`
	CreatedAt          time.Time
}

func (ContextAdmissionDecision) TableName() string {
	return "artifact_registry_context_admission_decisions"
}

// ContextPacket is an audit record for a minimal context set delivered to a
// persona or worker agent.
type ContextPacket struct {
	ID               uuid.UUID       `gorm:"type:text;primaryKey"`
	TaskID           string          `gorm:"index"`
	ProjectID        string          `gorm:"index"`
	Persona          string          `gorm:"index"`
	AgentRole        string          `gorm:"index"`
	WorkflowStage    string          `gorm:"index"`
	ActiveDirectives model.JSONArray `gorm:"type:text"`
	Summary          string          `gorm:"type:text"`
	Metadata         model.JSONField `gorm:"type:text"`
	CreatedAt        time.Time
}

func (ContextPacket) TableName() string { return "artifact_registry_context_packets" }
