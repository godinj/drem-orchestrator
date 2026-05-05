package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	OrchestratorHealthStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "drem_orchestrator_health_status",
		Help: "Current health status of the orchestrator (1 for healthy, 0 for unhealthy).",
	}, []string{"component"})

	TaskStateCount = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drem_orchestrator_task_state_total",
		Help: "Total number of tasks in various states.",
	}, []string{"state"})

	TaskThroughput = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drem_orchestrator_task_throughput_total",
		Help: "Total number of tasks processed per second.",
	}, []string{"operation"})

	WorkerActiveTasks = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "drem_orchestrator_worker_active_tasks",
		Help: "Number of active tasks currently being handled by workers.",
	}, []string{"worker_id"})

	TaskFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drem_orchestrator_task_failures_total",
		Help: "Total number of task failures.",
	}, []string{"reason", "task_type"})

	TaskRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drem_orchestrator_task_retries_total",
		Help: "Total number of task retries.",
	}, []string{"task_type"})

	ServiceUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "drem_orchestrator_service_up",
		Help: "Indicates if the orchestrator service is up (1) or down (0).",
	})
)

// RegisterMetrics registers the operator metrics collectors with reg.
func RegisterMetrics(reg *prometheus.Registry) error {
	OrchestratorHealthStatus.WithLabelValues("api")
	TaskStateCount.WithLabelValues("running")
	TaskThroughput.WithLabelValues("create")
	WorkerActiveTasks.WithLabelValues("worker-1")
	TaskFailures.WithLabelValues("timeout", "task-type-a")
	TaskRetries.WithLabelValues("task-type-a")

	collectors := []prometheus.Collector{
		OrchestratorHealthStatus,
		TaskStateCount,
		TaskThroughput,
		WorkerActiveTasks,
		TaskFailures,
		TaskRetries,
		ServiceUp,
	}
	for _, collector := range collectors {
		if err := reg.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// WorkerMetrics holds metrics for a worker agent.
type WorkerMetrics struct {
	mu             sync.Mutex
	activeTasks    int
	idleState      bool
	taskAttempts   int
	workerFailures int
	taskSuccesses  int
	taskErrors     int
	taskDurations  []float64
}

// NewWorkerMetrics creates a new WorkerMetrics instance.
func NewWorkerMetrics() *WorkerMetrics {
	return &WorkerMetrics{}
}

// IncActiveTasks increments the active task count.
func (m *WorkerMetrics) IncActiveTasks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeTasks++
}

// DecActiveTasks decrements the active task count.
func (m *WorkerMetrics) DecActiveTasks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeTasks--
}

// SetIdleState sets the idle/ready state of the worker.
func (m *WorkerMetrics) SetIdleState(idle bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idleState = idle
}

// IncTaskAttempts increments the number of task attempts.
func (m *WorkerMetrics) IncTaskAttempts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskAttempts++
}

// IncWorkerFailures increments the number of worker failures.
func (m *WorkerMetrics) IncWorkerFailures() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerFailures++
}

// IncTaskSuccesses increments the number of successful tasks.
func (m *WorkerMetrics) IncTaskSuccesses() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskSuccesses++
}

// IncTaskErrors increments the number of task errors.
func (m *WorkerMetrics) IncTaskErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskErrors++
}

// ObserveTaskDuration records the duration of a task.
func (m *WorkerMetrics) ObserveTaskDuration(duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskDurations = append(m.taskDurations, duration)
}

// GetActiveTasks returns the current number of active tasks.
func (m *WorkerMetrics) GetActiveTasks() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTasks
}

// IsIdle returns whether the worker is idle.
func (m *WorkerMetrics) IsIdle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idleState
}

// GetTaskAttempts returns the number of task attempts.
func (m *WorkerMetrics) GetTaskAttempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taskAttempts
}

// GetWorkerFailures returns the number of worker failures.
func (m *WorkerMetrics) GetWorkerFailures() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workerFailures
}

// GetTaskSuccesses returns the number of successful tasks.
func (m *WorkerMetrics) GetTaskSuccesses() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taskSuccesses
}

// GetTaskErrors returns the number of task errors.
func (m *WorkerMetrics) GetTaskErrors() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taskErrors
}

// GetTaskDurations returns the recorded task durations.
func (m *WorkerMetrics) GetTaskDurations() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taskDurations
}
