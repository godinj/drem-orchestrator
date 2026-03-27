package watcher

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MetricsStore persists and retrieves TurnMetric records for the lifecycle manager.
type MetricsStore struct {
	db *gorm.DB
}

// NewMetricsStore creates a MetricsStore backed by db.
func NewMetricsStore(db *gorm.DB) *MetricsStore {
	return &MetricsStore{db: db}
}

// RecordTurn persists a LifecycleResult to the turn_metrics table.
func (s *MetricsStore) RecordTurn(result *LifecycleResult) error {
	metric := TurnMetric{
		ID:           uuid.New(),
		Agent:        result.Agent,
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		ExitStatus:   result.ExitStatus,
		Duration:     result.Duration,
		StartedAt:    result.StartedAt,
		EndedAt:      result.EndedAt,
	}
	if result.ErrorDetails != "" {
		msg := result.ErrorDetails
		metric.ErrorDetails = &msg
	}
	return s.db.Create(&metric).Error
}

// GetTurns returns all TurnMetric records ordered by created_at ascending.
func (s *MetricsStore) GetTurns() ([]TurnMetric, error) {
	var turns []TurnMetric
	err := s.db.Order("created_at asc").Find(&turns).Error
	return turns, err
}
