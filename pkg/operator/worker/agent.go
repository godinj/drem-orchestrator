package worker

import (
	"context"

	"github.com/godinj/drem-orchestrator/pkg/operator/metrics"
)

// Worker represents a worker agent.
type Worker struct {
	Metrics *metrics.WorkerMetrics
}

// NewWorker creates a new worker agent.
func NewWorker(m *metrics.WorkerMetrics) *Worker {
	return &Worker{
		Metrics: m,
	}
}

// Start starts the worker agent.
func (w *Worker) Start(ctx context.Context) error {
	return nil
}

// Stop stops the worker agent.
func (w *Worker) Stop() error {
	return nil
}

// SubmitTask submits a task to the worker.
func (w *Worker) SubmitTask(ctx context.Context, taskID string) error {
	return nil
}
