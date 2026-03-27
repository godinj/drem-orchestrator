// Package metrics provides time-series metric storage for agent execution
// monitoring. It records periodic samples (context usage, cost, token counts)
// during agent runs and supports time-range queries for historical analysis.
//
// # Schema
//
// Each row in the metrics table captures a single time-series sample:
//
//	ID        — UUID primary key (auto-generated)
//	AgentID   — the agent that produced this sample
//	Name      — metric name (e.g. "context_pct", "cost_usd")
//	Timestamp — when the sample was taken (UTC)
//	Value     — the numeric value
//	Labels    — optional JSON-encoded key-value pairs for extra dimensions
//
// A composite index on (AgentID, Name, Timestamp) supports the primary query
// pattern: all samples of a given metric for one agent within a time window.
//
// # Query Patterns
//
//	// All context_pct samples for an agent in the last hour:
//	store.Query(agentID, "context_pct", time.Now().Add(-time.Hour), time.Now())
//
//	// Cost trajectory for an agent's entire run:
//	store.Query(agentID, "cost_usd", agent.CreatedAt, time.Now())
//
// Metrics survive agent completion. No automatic retention or cleanup is
// performed; consumers are responsible for pruning old data when needed.
package metrics

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Metric is the GORM model for the metrics table. Each row captures a single
// time-series sample: one named metric value for one agent at one point in
// time. Optional Labels provide additional dimensions for filtering.
type Metric struct {
	ID        uuid.UUID `gorm:"type:text;primaryKey"`
	AgentID   uuid.UUID `gorm:"type:text;not null;index:idx_agent_name_ts,priority:1"`
	Name      string    `gorm:"not null;index:idx_agent_name_ts,priority:2"`
	Timestamp time.Time `gorm:"not null;index:idx_agent_name_ts,priority:3"`
	Value     float64   `gorm:"type:float;not null"`
	Labels    string    `gorm:"type:text"`
	CreatedAt time.Time
}

// Store provides persistence operations for time-series agent metrics.
// It wraps a *gorm.DB and is the single entry point for recording and
// querying metric samples.
type Store struct {
	db *gorm.DB
}

// NewStore creates a Store backed by the given database connection and
// auto-migrates the Metric table.
func NewStore(db *gorm.DB) *Store {
	_ = db.AutoMigrate(&Metric{})
	return &Store{db: db}
}

// Record inserts a single metric sample for the given agent. Labels are
// optional; pass nil when no extra dimensions are needed. The timestamp is
// set to the current UTC time.
func (s *Store) Record(agentID uuid.UUID, name string, value float64, labels map[string]string) error {
	labelsJSON := ""
	if len(labels) > 0 {
		b, err := json.Marshal(labels)
		if err != nil {
			return fmt.Errorf("marshal labels for metric %q: %w", name, err)
		}
		labelsJSON = string(b)
	}
	m := Metric{
		AgentID:   agentID,
		Timestamp: time.Now().UTC(),
		Name:      name,
		Value:     value,
		Labels:    labelsJSON,
	}
	if err := s.db.Create(&m).Error; err != nil {
		return fmt.Errorf("record metric %q for agent %s: %w", name, agentID, err)
	}
	return nil
}

// Query returns all metric samples matching the given agent, metric name, and
// time range [start, end], ordered by timestamp ascending. Returns an empty
// (non-nil) slice when no results are found.
func (s *Store) Query(agentID uuid.UUID, name string, start, end time.Time) ([]Metric, error) {
	results := make([]Metric, 0)
	err := s.db.Where("agent_id = ? AND name = ? AND timestamp >= ? AND timestamp <= ?",
		agentID, name, start, end).
		Order("timestamp ASC").
		Find(&results).Error
	if err != nil {
		return results, fmt.Errorf("query metric %q for agent %s: %w", name, agentID, err)
	}
	return results, nil
}
