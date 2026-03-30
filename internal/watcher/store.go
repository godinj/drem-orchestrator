// Package watcher provides lifecycle management, metrics recording, and
// trigger logic for C-Suite agents across Phase 2–4.
package watcher

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TurnMetric is the GORM model for the turn_metrics table. Each row captures
// timing, token usage, and outcome for one completed agent execution cycle.
// Fields cover both the store API (TokensIn/TokensOut/DurationMs) and the
// lifecycle API (InputTokens/OutputTokens/Duration) so a single table serves
// both subsystems.
type TurnMetric struct {
	ID              uuid.UUID `gorm:"type:text;primaryKey"`
	Agent           string    `gorm:"index;not null"`
	StartedAt       time.Time
	EndedAt         time.Time
	DurationMs      int
	Duration        time.Duration
	TokensIn        int
	TokensOut       int
	InputTokens     int
	OutputTokens    int
	EventsProcessed int
	MessagesSent    int
	ExitStatus      int
	ErrorDetails    *string `gorm:"type:text"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TurnResult carries the data for a completed agent turn submitted via
// RecordTurn. The store assigns the row UUID and persists the record.
type TurnResult struct {
	Agent           string
	StartedAt       time.Time
	EndedAt         time.Time
	DurationMs      int
	TokensIn        int
	TokensOut       int
	EventsProcessed int
	MessagesSent    int
	ExitStatus      int
	ErrorDetails    *string
}

// Store provides persistence operations for watcher metrics.
// It wraps a *gorm.DB and is the single entry point for all watcher persistence.
type Store struct {
	db *gorm.DB
}

// NewStore creates a Store backed by the given database connection and
// auto-migrates the TurnMetric table.
func NewStore(db *gorm.DB) *Store {
	_ = db.AutoMigrate(&TurnMetric{})
	return &Store{db: db}
}

// RecordTurn inserts a new TurnMetric row from the given TurnResult.
// The store assigns a UUID to the new row via the GORM BeforeCreate callback.
func (s *Store) RecordTurn(result TurnResult) error {
	m := TurnMetric{
		Agent:           result.Agent,
		StartedAt:       result.StartedAt,
		EndedAt:         result.EndedAt,
		DurationMs:      result.DurationMs,
		TokensIn:        result.TokensIn,
		TokensOut:       result.TokensOut,
		InputTokens:     result.TokensIn,
		OutputTokens:    result.TokensOut,
		EventsProcessed: result.EventsProcessed,
		MessagesSent:    result.MessagesSent,
		ExitStatus:      result.ExitStatus,
		ErrorDetails:    result.ErrorDetails,
	}
	if err := s.db.Create(&m).Error; err != nil {
		return fmt.Errorf("record turn for agent %q: %w", result.Agent, err)
	}
	return nil
}

// QueryTurns returns the most recent turns for the named agent, ordered by
// started_at descending. At most limit rows are returned. Returns an empty
// (non-nil) slice when no results are found.
func (s *Store) QueryTurns(agent string, limit int) ([]TurnMetric, error) {
	turns := make([]TurnMetric, 0)
	err := s.db.Where("agent = ?", agent).
		Order("started_at DESC").
		Limit(limit).
		Find(&turns).Error
	if err != nil {
		return turns, fmt.Errorf("query turns for agent %q: %w", agent, err)
	}
	return turns, nil
}
