package watcher

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MetricsStore persists and retrieves TurnMetric records.
type MetricsStore struct {
	db *gorm.DB
}

// NewMetricsStore creates a MetricsStore backed by db.
func NewMetricsStore(db *gorm.DB) *MetricsStore {
	return &MetricsStore{db: db}
}

// RecordTurn persists a TurnResult to the turn_metrics table.
func (s *MetricsStore) RecordTurn(result *TurnResult) error {
	metric := TurnMetric{
		ID:           uuid.New(),
		Agent:        result.Agent,
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		ExitStatus:   result.ExitStatus,
		ErrorDetails: result.ErrorDetails,
		Duration:     result.Duration,
		StartedAt:    result.StartedAt,
		EndedAt:      result.EndedAt,
	}
	return s.db.Create(&metric).Error
}

// GetTurns returns all TurnMetric records ordered by created_at ascending.
func (s *MetricsStore) GetTurns() ([]TurnMetric, error) {
	var turns []TurnMetric
	err := s.db.Order("created_at asc").Find(&turns).Error
	return turns, err
}
