package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// BeforeCreate generates a UUID for a new Project if one is not already set.
func (p *Project) BeforeCreate(_ *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// Task represents a unit of work tracked by the orchestrator.
type Task struct {
	ID              uuid.UUID  `gorm:"type:text;primaryKey"`
	ProjectID       uuid.UUID  `gorm:"type:text;not null;index"`
	ParentTaskID    *uuid.UUID `gorm:"type:text;index"`
	Title           string     `gorm:"not null"`
	Description     string     `gorm:"not null"`
	Status          TaskStatus `gorm:"not null;default:backlog"`
	Priority        int        `gorm:"default:0"`
	Labels          JSONArray  `gorm:"type:text"`
	DependencyIDs   JSONArray  `gorm:"type:text"`
	AssignedAgentID *uuid.UUID `gorm:"type:text"`
	Plan            JSONField  `gorm:"type:text"`
	PlanFeedback    string
	TestPlan        string
	TestFeedback    string
	WorktreeBranch  string
	PRUrl           string
	Context         JSONField `gorm:"type:text"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Project         Project     `gorm:"foreignKey:ProjectID"`
	ParentTask      *Task       `gorm:"foreignKey:ParentTaskID"`
	Subtasks        []Task      `gorm:"foreignKey:ParentTaskID"`
	AssignedAgent   *Agent        `gorm:"foreignKey:AssignedAgentID"`
	Events          []TaskEvent   `gorm:"foreignKey:TaskID"`
	Comments        []TaskComment `gorm:"foreignKey:TaskID"`
}

// BeforeCreate generates a UUID for a new Task if one is not already set.
func (t *Task) BeforeCreate(_ *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
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
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BeforeCreate generates a UUID for a new Agent if one is not already set.
func (a *Agent) BeforeCreate(_ *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
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

// BeforeCreate generates a UUID for a new TaskEvent if one is not already set.
func (e *TaskEvent) BeforeCreate(_ *gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
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

// BeforeCreate generates a UUID for a new Memory if one is not already set.
func (m *Memory) BeforeCreate(_ *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
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

// BeforeCreate generates a UUID for a new TaskComment if one is not already set.
func (c *TaskComment) BeforeCreate(_ *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// SubtaskPlan is the plan item produced by planner agents during task
// decomposition.
type SubtaskPlan struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AgentType      string   `json:"agent_type"`
	EstimatedFiles []string `json:"estimated_files"`
}
