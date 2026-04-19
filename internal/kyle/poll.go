package kyle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/godinj/drem-orchestrator/internal/projects"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
)

// eventsRecentLimit bounds the number of events Kyle keeps per project per
// snapshot. Kyle is a reporting surface, not an event archive — callers
// that need the full log query the orchestrator directly.
const eventsRecentLimit = 50

// tasksListLimit bounds the task-list request so Kyle's memory footprint
// stays proportional to the number of in-flight tasks, not to the project's
// full history. The orchestrator returns newest-first, so the slice we keep
// represents the most recent tasks.
const tasksListLimit = 200

// pollOnce fetches workers/tasks/events for a single project and replaces
// the cache entry on success. On failure the previous snapshot is kept and
// only RecordFailure is called so /world continues to serve *something*
// while the upstream orchestrator is unavailable.
//
// Every individual HTTP call is bounded by a per-call context derived from
// ctx; a slow orchestrator does not stall the whole poll cycle indefinitely.
func (s *Service) pollOnce(ctx context.Context, project projects.Project) error {
	client, err := s.clientFor(project)
	if err != nil {
		s.cache.RecordFailure(project.Name, s.now(), err)
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, orchclient.DefaultTimeout)
	defer cancel()

	workers, err := client.ListWorkers(callCtx, project.Name)
	if err != nil {
		s.cache.RecordFailure(project.Name, s.now(), fmt.Errorf("list workers: %w", err))
		return err
	}
	tasks, err := client.ListTasks(callCtx, project.Name, orchclient.TaskFilter{Limit: tasksListLimit})
	if err != nil {
		s.cache.RecordFailure(project.Name, s.now(), fmt.Errorf("list tasks: %w", err))
		return err
	}
	events, err := client.Events(callCtx, time.Time{}, eventsRecentLimit)
	if err != nil {
		s.cache.RecordFailure(project.Name, s.now(), fmt.Errorf("events: %w", err))
		return err
	}

	s.cache.Set(ProjectSnapshot{
		Project:      project.Name,
		Tasks:        tasks,
		Workers:      workers,
		RecentEvents: events,
		FetchedAt:    s.now(),
	})
	return nil
}

// clientFor returns an orchclient.Client for project, constructing and
// memoising one on first use. Construction is cheap (no I/O), but caching
// keeps the map available for tests that want to swap in a fake.
func (s *Service) clientFor(project projects.Project) (*orchclient.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[project.Name]; ok {
		return c, nil
	}
	if project.OrchURL == "" {
		return nil, fmt.Errorf("project %q has no orch_url", project.Name)
	}
	c := orchclient.New(project.OrchURL)
	s.clients[project.Name] = c
	return c, nil
}

// pollAll runs pollOnce for every registered project, sequentially. We keep
// this serial because the set of projects is small (one per orchestrator)
// and the HTTP calls themselves are bounded by their own timeouts; adding
// goroutines would not meaningfully reduce wall time and would complicate
// failure logging.
func (s *Service) pollAll(ctx context.Context) {
	for _, p := range s.registry.Projects {
		if err := s.pollOnce(ctx, p); err != nil {
			slog.Warn("kyle poll failed", "project", p.Name, "err", err)
		}
	}
}
