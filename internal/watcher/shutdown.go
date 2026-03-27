package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultShutdownMaxWait is the default maximum time to wait for all
// registered components to stop during graceful shutdown.
const DefaultShutdownMaxWait = 5 * time.Minute

// component pairs a name with its stop function for shutdown ordering
// and error reporting.
type component struct {
	name   string
	stopFn func(ctx context.Context) error
}

// ShutdownManager orchestrates graceful shutdown of the watcher and its
// components. On SIGTERM/SIGINT the caller invokes Shutdown, which calls
// all registered stop functions with a deadline context. If the deadline
// expires, Shutdown returns an error indicating forced exit.
type ShutdownManager struct {
	maxWait    time.Duration
	logger     *slog.Logger
	mu         sync.Mutex
	components []component
}

// NewShutdownManager creates a ShutdownManager that allows up to maxWait
// for all components to stop cleanly.
func NewShutdownManager(maxWait time.Duration, logger *slog.Logger) *ShutdownManager {
	return &ShutdownManager{
		maxWait: maxWait,
		logger:  logger,
	}
}

// RegisterComponent adds a named component with its stop function. Stop
// functions are called in registration order during Shutdown. The stop
// function receives a context with a deadline equal to the remaining
// shutdown budget.
func (s *ShutdownManager) RegisterComponent(name string, stopFn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.components = append(s.components, component{name: name, stopFn: stopFn})
}

// Shutdown calls all registered stop functions in order, passing a context
// that is cancelled when maxWait elapses. Returns nil on clean shutdown.
// Returns an error listing which components failed or timed out.
func (s *ShutdownManager) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	comps := make([]component, len(s.components))
	copy(comps, s.components)
	s.mu.Unlock()

	deadline, cancel := context.WithTimeout(ctx, s.maxWait)
	defer cancel()

	var errs []error
	for _, c := range comps {
		s.logger.Info("stopping component", "name", c.name)
		if err := c.stopFn(deadline); err != nil {
			s.logger.Error("component stop failed", "name", c.name, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", c.name, err))
		} else {
			s.logger.Info("component stopped", "name", c.name)
		}
		// If the overall deadline has passed, stop trying further components.
		if deadline.Err() != nil {
			remaining := len(comps) - len(errs) - 1
			if remaining > 0 {
				errs = append(errs, fmt.Errorf("shutdown timeout: %d components not stopped", remaining))
			}
			break
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown completed with errors: %v", errs)
	}
	return nil
}
