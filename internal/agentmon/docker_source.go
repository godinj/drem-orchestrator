// This file wires the Docker-stdout subscription into agentmon. A
// DockerSource subscribes to lifecycle events matching a label filter
// (typically drem.project=<name>) and spawns one tailContainer
// goroutine per running container. Die/destroy events cancel the
// corresponding tail. Ctx cancel shuts everything down and waits for
// the tails to drain.

package agentmon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/godinj/drem-orchestrator/internal/container"
)

// Ingestor is the seam between the Docker-stdout path and the HTTP
// upload. The production implementation is HTTPIngestor (in client.go);
// tests inject a channel-backed fake.
type Ingestor interface {
	Ingest(ctx context.Context, records []IngestRecord) error
}

// DockerSource owns a Docker event subscription and the per-container
// tail goroutines it spawns. One DockerSource should run per agentmon
// process; tests may run multiple against a single FakeRuntime.
type DockerSource struct {
	// Runtime is the container runtime used to subscribe to events
	// and open log streams. Required.
	Runtime container.Runtime
	// Ingestor receives every batch of parsed records. Required.
	Ingestor Ingestor
	// Project tags tails with the worker's project name when no
	// drem.worker-id label is present; currently informational.
	Project string
	// ContainerFilter restricts which containers the source watches.
	// In the per-project agentmon deployment (see README.md) this is
	// typically {"drem.project": Project}. An empty filter watches
	// every container the daemon exposes — useful only in tests.
	ContainerFilter container.EventFilter

	// state tracks active per-container tails so EventDie can cancel
	// the matching tail goroutine.
	mu    sync.Mutex
	tails map[string]context.CancelFunc
	wg    sync.WaitGroup
}

// Run subscribes to lifecycle events and drives tails until ctx is
// cancelled. Returns when the event channel closes and all tail
// goroutines have drained. Any error from SubscribeEvents is returned
// immediately; errors from individual tails are logged (see tail.go).
func (s *DockerSource) Run(ctx context.Context) error {
	if s.Runtime == nil {
		return fmt.Errorf("agentmon docker source: Runtime is required")
	}
	if s.Ingestor == nil {
		return fmt.Errorf("agentmon docker source: Ingestor is required")
	}
	s.mu.Lock()
	s.tails = make(map[string]context.CancelFunc)
	s.mu.Unlock()

	events, err := s.Runtime.SubscribeEvents(ctx, s.ContainerFilter)
	if err != nil {
		return fmt.Errorf("agentmon docker source: subscribe: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return nil
		case ev, ok := <-events:
			if !ok {
				s.shutdown()
				return nil
			}
			s.handleEvent(ctx, ev)
		}
	}
}

// handleEvent routes a single lifecycle event to start or cancel a
// per-container tail. Unknown event types are ignored.
func (s *DockerSource) handleEvent(ctx context.Context, ev container.Event) {
	switch ev.Type {
	case container.EventStart:
		s.startTail(ctx, ev)
	case container.EventDie, container.EventOOM, container.EventDestroy:
		s.stopTail(ev.ContainerID)
	}
}

// startTail spawns a tail goroutine for the container unless one is
// already active. Duplicate EventStart messages for the same container
// — which Docker occasionally emits after a short restart — are
// coalesced into a single tail.
func (s *DockerSource) startTail(parent context.Context, ev container.Event) {
	s.mu.Lock()
	if _, exists := s.tails[ev.ContainerID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.tails[ev.ContainerID] = cancel
	s.mu.Unlock()

	workerID := ev.Labels["drem.worker-id"]
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.removeTail(ev.ContainerID)
		if err := tailContainer(ctx, s.Runtime, s.Ingestor, ev.ContainerID, workerID, ev.Labels); err != nil {
			slog.Warn("agentmon docker tail exited",
				"container", ev.ContainerID, "err", err)
		}
	}()
}

// stopTail cancels the tail goroutine for containerID. Safe to call
// when no tail exists.
func (s *DockerSource) stopTail(containerID string) {
	s.mu.Lock()
	cancel, ok := s.tails[containerID]
	if ok {
		delete(s.tails, containerID)
	}
	s.mu.Unlock()
	if ok {
		cancel()
	}
}

// removeTail drops a container's entry from the tail map without
// cancelling (used by a tail's defer when it exits naturally, for
// example because the log stream closed).
func (s *DockerSource) removeTail(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tails, containerID)
}

// shutdown cancels every active tail and blocks until they all return.
// Called when ctx is cancelled or the event channel closes.
func (s *DockerSource) shutdown() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.tails))
	for _, c := range s.tails {
		cancels = append(cancels, c)
	}
	s.tails = map[string]context.CancelFunc{}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	s.wg.Wait()
}
