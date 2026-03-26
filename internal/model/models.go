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
	Description   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Tasks         []Task  `gorm:"foreignKey:ProjectID"`
	Agents        []Agent `gorm:"foreignKey:ProjectID"`
}

// Task represents a unit of work tracked by the orchestrator.
type Task struct {
	ID              uuid.UUID    `gorm:"type:text;primaryKey"`
	ProjectID       uuid.UUID    `gorm:"type:text;not null;index"`
	ParentTaskID    *uuid.UUID   `gorm:"type:text;index"`
	Title           string       `gorm:"not null"`
	Description     string       `gorm:"not null"`
	Status          TaskStatus   `gorm:"not null;default:backlog"`
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

// Agent represents a Claude Code agent working on tasks.
type Agent struct {
	ID              uuid.UUID   `gorm:"type:text;primaryKey"`
	ProjectID       uuid.UUID   `gorm:"type:text;not null;index"`
	AgentType       AgentType   `gorm:"not null"`
	Name            string      `gorm:"not null"`
	Status          AgentStatus `gorm:"not null;default:idle"`
	CurrentTaskID   *uuid.UUID  `gorm:"type:text"`
	WorktreePath    string
	WorktreeBranch  string
	TmuxSession     string
	MemorySummary   string
	HeartbeatAt     *time.Time
	Config          JSONField  `gorm:"type:text"`
	CompletedAt     *time.Time // time when agent completion was processed
	ExitReason      string     // mapped exit reason (success, error, context_limit, killed, timeout)
	TotalCostUSD    float64    // cumulative API cost from last context monitor reading
	FinalContextPct int        // final context window usage percentage from last reading
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
