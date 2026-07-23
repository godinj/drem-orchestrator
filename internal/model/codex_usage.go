package model

import (
	"time"

	"github.com/google/uuid"
)

// CodexGoalUsage is append-only telemetry reported by the Codex thread that
// supervises an orchestrated task. It is intentionally separate from worker
// inference usage: TokensUsed is the final explicit-goal total returned by
// Codex, while WorkerAttempt tokens describe remote SGLang inference.
type CodexGoalUsage struct {
	ID              uuid.UUID `gorm:"type:text;primaryKey"`
	TaskID          uuid.UUID `gorm:"type:text;not null;index"`
	Actor           string    `gorm:"not null;index"`
	ThreadID        string    `gorm:"not null;index"`
	GoalObjective   string    `gorm:"type:text;not null"`
	GoalStatus      string    `gorm:"not null;index"`
	TokensUsed      int64     `gorm:"not null"`
	ElapsedMS       int64     `gorm:"not null"`
	Source          string    `gorm:"not null"`
	IdempotencyKey  string    `gorm:"not null;uniqueIndex"`
	RequestHash     string    `gorm:"not null"`
	UsageCapturedAt time.Time `gorm:"not null"`
	CreatedAt       time.Time
}
