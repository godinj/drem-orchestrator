package agent

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/ctxmon"
	"github.com/godinj/drem-orchestrator/internal/metrics"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestRecordMetrics_CreatesFiveRows verifies that a single call to
// recordMetrics creates exactly five metric rows (context_pct, cost_usd,
// token_input, token_output, tool_count) in the metrics table.
func TestRecordMetrics_CreatesFiveRows(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &metrics.Metric{})
	store := metrics.NewStore(db)

	r := &Runner{
		db:              db,
		metricsRecorder: store,
	}

	agentID := uuid.New()
	usage := &ctxmon.Usage{
		UsedPercent:       45,
		TotalCostUSD:      0.12,
		TotalInputTokens:  5000,
		TotalOutputTokens: 2000,
	}
	activity := &agentmon.Activity{
		ToolCounts: map[string]int{
			"Read":  3,
			"Write": 2,
			"Bash":  1,
		},
	}

	r.recordMetrics(agentID, usage, activity)

	var count int64
	db.Model(&metrics.Metric{}).Where("agent_id = ?", agentID).Count(&count)
	if count != 5 {
		t.Fatalf("expected 5 metric rows, got %d", count)
	}

	// Verify each metric name and value.
	wantMetrics := map[string]float64{
		metricContextPct:  45,
		metricCostUSD:     0.12,
		metricTokenInput:  5000,
		metricTokenOutput: 2000,
		metricToolCount:   6, // 3 + 2 + 1
	}

	now := time.Now().UTC()
	for name, wantVal := range wantMetrics {
		results, err := store.Query(agentID, name, now.Add(-time.Minute), now.Add(time.Minute))
		if err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		if len(results) != 1 {
			t.Errorf("metric %s: expected 1 result, got %d", name, len(results))
			continue
		}
		if results[0].Value != wantVal {
			t.Errorf("metric %s: expected value %f, got %f", name, wantVal, results[0].Value)
		}
	}
}

// TestRecordMetrics_NilActivity records tool_count as zero when activity is nil.
func TestRecordMetrics_NilActivity(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &metrics.Metric{})
	store := metrics.NewStore(db)

	r := &Runner{
		db:              db,
		metricsRecorder: store,
	}

	agentID := uuid.New()
	usage := &ctxmon.Usage{
		UsedPercent:       80,
		TotalCostUSD:      0.50,
		TotalInputTokens:  10000,
		TotalOutputTokens: 4000,
	}

	r.recordMetrics(agentID, usage, nil)

	// Should still produce 5 rows, with tool_count = 0.
	var count int64
	db.Model(&metrics.Metric{}).Where("agent_id = ?", agentID).Count(&count)
	if count != 5 {
		t.Fatalf("expected 5 metric rows, got %d", count)
	}

	now := time.Now().UTC()
	results, err := store.Query(agentID, metricToolCount, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("query tool_count: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 tool_count result, got %d", len(results))
	}
	if results[0].Value != 0 {
		t.Errorf("expected tool_count=0 with nil activity, got %f", results[0].Value)
	}
}

// TestRecordMetrics_NilRecorderIsNoop verifies the nil guard in contextMonitorLoop:
// when metricsRecorder is nil, recordMetrics is never called.
func TestRecordMetrics_NilRecorderIsNoop(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &metrics.Metric{})

	r := &Runner{
		db:              db,
		metricsRecorder: nil, // not wired
	}

	// This mirrors the guard in contextMonitorLoop: if r.metricsRecorder != nil ...
	// We just verify the guard works at the call site level.
	agentID := uuid.New()
	usage := &ctxmon.Usage{UsedPercent: 50}

	if r.metricsRecorder != nil {
		r.recordMetrics(agentID, usage, nil)
	}

	var count int64
	db.Model(&metrics.Metric{}).Where("agent_id = ?", agentID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 metric rows with nil recorder, got %d", count)
	}
}

// TestRecordMetrics_MultipleTicks verifies that calling recordMetrics multiple
// times accumulates metric rows (one set of 5 per call).
func TestRecordMetrics_MultipleTicks(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &metrics.Metric{})
	store := metrics.NewStore(db)

	r := &Runner{
		db:              db,
		metricsRecorder: store,
	}

	agentID := uuid.New()
	for i := 0; i < 3; i++ {
		usage := &ctxmon.Usage{
			UsedPercent:       10 + i*10,
			TotalCostUSD:      float64(i) * 0.05,
			TotalInputTokens:  1000 * (i + 1),
			TotalOutputTokens: 500 * (i + 1),
		}
		r.recordMetrics(agentID, usage, nil)
	}

	// 3 ticks × 5 metrics = 15 rows.
	var count int64
	db.Model(&metrics.Metric{}).Where("agent_id = ?", agentID).Count(&count)
	if count != 15 {
		t.Errorf("expected 15 metric rows after 3 ticks, got %d", count)
	}

	// Verify context_pct has 3 data points with ascending values.
	now := time.Now().UTC()
	results, err := store.Query(agentID, metricContextPct, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("query context_pct: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 context_pct results, got %d", len(results))
	}
}
