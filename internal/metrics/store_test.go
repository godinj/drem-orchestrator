package metrics_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestRecord(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &metrics.Metric{})
	store := metrics.NewStore(db)

	t.Run("basic record creates a row", func(t *testing.T) {
		agentID := uuid.New()
		err := store.Record(agentID, "context_pct", 42.5, nil)
		if err != nil {
			t.Fatalf("Record: %v", err)
		}

		var count int64
		db.Model(&metrics.Metric{}).Where("agent_id = ?", agentID).Count(&count)
		if count != 1 {
			t.Errorf("expected 1 row, got %d", count)
		}
	})

	t.Run("record with labels stores JSON", func(t *testing.T) {
		agentID := uuid.New()
		labels := map[string]string{"source": "context_monitor"}
		err := store.Record(agentID, "cost_usd", 0.05, labels)
		if err != nil {
			t.Fatalf("Record with labels: %v", err)
		}

		var m metrics.Metric
		db.Where("agent_id = ?", agentID).First(&m)
		if m.Labels == "" {
			t.Error("expected non-empty labels JSON")
		}
		if m.Name != "cost_usd" {
			t.Errorf("expected name=cost_usd, got %q", m.Name)
		}
	})

	t.Run("record with nil labels stores empty string", func(t *testing.T) {
		agentID := uuid.New()
		err := store.Record(agentID, "token_input", 1500, nil)
		if err != nil {
			t.Fatalf("Record: %v", err)
		}

		var m metrics.Metric
		db.Where("agent_id = ?", agentID).First(&m)
		if m.Labels != "" {
			t.Errorf("expected empty labels, got %q", m.Labels)
		}
	})

	t.Run("multiple records for same agent", func(t *testing.T) {
		agentID := uuid.New()
		for i := 0; i < 5; i++ {
			err := store.Record(agentID, "context_pct", float64(20+i*10), nil)
			if err != nil {
				t.Fatalf("Record %d: %v", i, err)
			}
		}

		var count int64
		db.Model(&metrics.Metric{}).Where("agent_id = ?", agentID).Count(&count)
		if count != 5 {
			t.Errorf("expected 5 rows, got %d", count)
		}
	})
}

func TestQuery(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &metrics.Metric{})
	store := metrics.NewStore(db)
	agentID := uuid.New()

	// Seed 5 metrics with known timestamps, 1 minute apart.
	baseTime := time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		m := metrics.Metric{
			ID:        uuid.New(),
			AgentID:   agentID,
			Timestamp: baseTime.Add(time.Duration(i) * time.Minute),
			Name:      "context_pct",
			Value:     float64(20 + i*10),
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed metric %d: %v", i, err)
		}
	}

	t.Run("full range returns all points time-ordered", func(t *testing.T) {
		start := baseTime.Add(-time.Second)
		end := baseTime.Add(5 * time.Minute)
		results, err := store.Query(agentID, "context_pct", start, end)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 5 {
			t.Fatalf("expected 5 results, got %d", len(results))
		}
		// Verify ascending order.
		for i := 1; i < len(results); i++ {
			if results[i].Timestamp.Before(results[i-1].Timestamp) {
				t.Errorf("results not time-ordered at index %d", i)
			}
		}
		if results[0].Value != 20 {
			t.Errorf("expected first value=20, got %f", results[0].Value)
		}
		if results[4].Value != 60 {
			t.Errorf("expected last value=60, got %f", results[4].Value)
		}
	})

	t.Run("partial time range", func(t *testing.T) {
		// Minutes 1–3 inclusive (index 1, 2, 3).
		start := baseTime.Add(1 * time.Minute)
		end := baseTime.Add(3 * time.Minute)
		results, err := store.Query(agentID, "context_pct", start, end)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 results, got %d", len(results))
		}
	})

	t.Run("no data returns empty non-nil slice", func(t *testing.T) {
		unknownAgent := uuid.New()
		results, err := store.Query(unknownAgent, "context_pct", baseTime, baseTime.Add(time.Hour))
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if results == nil {
			t.Fatal("expected non-nil slice")
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("single point", func(t *testing.T) {
		singleAgent := uuid.New()
		ts := baseTime.Add(10 * time.Minute)
		m := metrics.Metric{
			ID:        uuid.New(),
			AgentID:   singleAgent,
			Timestamp: ts,
			Name:      "cost_usd",
			Value:     1.23,
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed single: %v", err)
		}

		results, err := store.Query(singleAgent, "cost_usd", ts.Add(-time.Second), ts.Add(time.Second))
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Value != 1.23 {
			t.Errorf("expected value=1.23, got %f", results[0].Value)
		}
	})

	t.Run("filters by metric name", func(t *testing.T) {
		// Add a cost_usd metric for the same agent at baseTime.
		m := metrics.Metric{
			ID:        uuid.New(),
			AgentID:   agentID,
			Timestamp: baseTime,
			Name:      "cost_usd",
			Value:     0.99,
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed cost: %v", err)
		}

		results, err := store.Query(agentID, "cost_usd", baseTime.Add(-time.Second), baseTime.Add(time.Second))
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 cost_usd result, got %d", len(results))
		}
		if results[0].Name != "cost_usd" {
			t.Errorf("expected name=cost_usd, got %q", results[0].Name)
		}
	})

	t.Run("filters by agent ID", func(t *testing.T) {
		otherAgent := uuid.New()
		m := metrics.Metric{
			ID:        uuid.New(),
			AgentID:   otherAgent,
			Timestamp: baseTime,
			Name:      "context_pct",
			Value:     99,
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed other agent: %v", err)
		}

		results, err := store.Query(otherAgent, "context_pct", baseTime.Add(-time.Second), baseTime.Add(time.Second))
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result for other agent, got %d", len(results))
		}
		if results[0].Value != 99 {
			t.Errorf("expected value=99, got %f", results[0].Value)
		}
	})
}
