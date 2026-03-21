package model

import (
	"time"

	"github.com/google/uuid"
)

// BugReport represents a structured problem report filed by any agent type.
// It surfaces issues (broken builds, flaky tests, unclear requirements, etc.)
// to human operators for triage and optional promotion into tasks.
type BugReport struct {
	ID                  uuid.UUID         `gorm:"type:text;primaryKey"`
	Title               string            `gorm:"not null"`
	Description         string            `gorm:"not null"`
	Category            BugReportCategory `gorm:"not null"`
	Severity            BugReportSeverity `gorm:"not null"`
	Status              BugReportStatus   `gorm:"not null;default:open"`
	ReproductionContext string
	AgentID             *uuid.UUID `gorm:"type:text;index"`
	TaskID              *uuid.UUID `gorm:"type:text;index"`
	ProjectID           uuid.UUID  `gorm:"type:text;not null;index"`
	PromotedTaskID      *uuid.UUID `gorm:"type:text"`
	CreatedAt           time.Time
	UpdatedAt           time.Time

	// Associations
	Agent       *Agent             `gorm:"foreignKey:AgentID"`
	Task        *Task              `gorm:"foreignKey:TaskID"`
	Project     Project            `gorm:"foreignKey:ProjectID"`
	PromotedTask *Task             `gorm:"foreignKey:PromotedTaskID"`
	Comments    []BugReportComment `gorm:"foreignKey:BugReportID"`
}

// BugReportComment stores a user or system comment on a bug report.
type BugReportComment struct {
	ID          uuid.UUID `gorm:"type:text;primaryKey"`
	BugReportID uuid.UUID `gorm:"type:text;not null;index"`
	Author      string    `gorm:"not null"` // "user" or "system"
	Body        string    `gorm:"not null"`
	CreatedAt   time.Time
}
