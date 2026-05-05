package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	err := RegisterMetrics(reg)
	require.NoError(t, err, "RegisterMetrics should not return an error")

	// Check if all expected metrics are registered
	expectedMetrics := []string{
		"drem_orchestrator_health_status",
		"drem_orchestrator_task_state_total",
		"drem_orchestrator_task_throughput_total",
		"drem_orchestrator_worker_active_tasks",
		"drem_orchestrator_task_failures_total",
		"drem_orchestrator_task_retries_total",
		"drem_orchestrator_service_up",
	}

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	for _, expectedName := range expectedMetrics {
		found := false
		for _, mf := range metricFamilies {
			if mf.GetName() == expectedName {
				found = true
				break
			}
		}
		assert.True(t, found, "Metric %s should be registered", expectedName)
	}
}

func TestMetricsRegistrationAndLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	err := RegisterMetrics(reg)
	require.NoError(t, err)

	t.Run("OrchestratorHealthStatus", func(t *testing.T) {
		OrchestratorHealthStatus.WithLabelValues("api").Set(1)
		val := testutil.ToFloat64(OrchestratorHealthStatus.WithLabelValues("api"))
		assert.Equal(t, 1.0, val)
	})

	t.Run("TaskStateCount", func(t *testing.T) {
		TaskStateCount.WithLabelValues("running").Inc()
		val := testutil.ToFloat64(TaskStateCount.WithLabelValues("running"))
		assert.Equal(t, 1.0, val)
	})

	t.Run("TaskThroughput", func(t *testing.T) {
		TaskThroughput.WithLabelValues("create").Inc()
		val := testutil.ToFloat64(TaskThroughput.WithLabelValues("create"))
		assert.Equal(t, 1.0, val)
	})

	t.Run("WorkerActiveTasks", func(t *testing.T) {
		WorkerActiveTasks.WithLabelValues("worker-1").Set(5)
		val := testutil.ToFloat64(WorkerActiveTasks.WithLabelValues("worker-1"))
		assert.Equal(t, 5.0, val)
	})

	t.Run("TaskFailures", func(t *testing.T) {
		TaskFailures.WithLabelValues("timeout", "task-type-a").Inc()
		val := testutil.ToFloat64(TaskFailures.WithLabelValues("timeout", "task-type-a"))
		assert.Equal(t, 1.0, val)
	})

	t.Run("TaskRetries", func(t *testing.T) {
		TaskRetries.WithLabelValues("task-type-a").Inc()
		val := testutil.ToFloat64(TaskRetries.WithLabelValues("task-type-a"))
		assert.Equal(t, 1.0, val)
	})

	t.Run("ServiceUp", func(t *testing.T) {
		ServiceUp.Set(1)
		val := testutil.ToFloat64(ServiceUp)
		assert.Equal(t, 1.0, val)
	})
}

func TestMetricMetadata(t *testing.T) {
	reg := prometheus.NewRegistry()
	err := RegisterMetrics(reg)
	require.NoError(t, err)

	expectedMetrics := []struct {
		name string
		help string
	}{
		{"drem_orchestrator_health_status", "Current health status of the orchestrator (1 for healthy, 0 for unhealthy)."},
		{"drem_orchestrator_task_state_total", "Total number of tasks in various states."},
		{"drem_orchestrator_task_throughput_total", "Total number of tasks processed per second."},
		{"drem_orchestrator_worker_active_tasks", "Number of active tasks currently being handled by workers."},
		{"drem_orchestrator_task_failures_total", "Total number of task failures."},
		{"drem_orchestrator_task_retries_total", "Total number of task retries."},
		{"drem_orchestrator_service_up", "Indicates if the orchestrator service is up (1) or down (0)."},
	}

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	for _, expected := range expectedMetrics {
		found := false
		for _, mf := range metricFamilies {
			if mf.GetName() == expected.name {
				found = true
				assert.Equal(t, expected.help, mf.GetHelp(), "Help text mismatch for %s", expected.name)
				break
			}
		}
		assert.True(t, found, "Metric %s not found in registry", expected.name)
	}
}
