// Package kyle implements the Drem reporting agent: a single globally-
// running service that polls every registered project's orchestrator over
// HTTP and exposes a unified read-only view to C-Suite.
//
// Kyle is strictly read-only. It never writes to any orchestrator's SQLite
// database and holds no authority over task state. Its job is to aggregate
// pkg/orchdto snapshots across projects and present them on /world,
// /projects/:name, and /events HTTP endpoints. A sidecar docker-query-proxy
// gives Kyle a narrow, allowlisted view of host Docker state via
// /docker/query.
//
// This package is the reporting surface described in
// docs/prd-containerization.md. The invariant we rely on — "Kyle talks to
// the orchestrator only through HTTP" — is enforced by importing
// pkg/orchclient rather than anything from internal/orchhttp or
// internal/orchestrator.
package kyle

import (
	"sync"
	"time"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// ProjectSnapshot bundles everything Kyle has last fetched from a single
// project's orchestrator. It is the unit of data held in the cache and
// returned on the /world and /projects/:name endpoints.
//
// FetchedAt is the time the snapshot was most recently populated; a caller
// (see Cache.All) may compare it to the poll interval to decide whether the
// snapshot is stale. Stale is non-fatal: the snapshot is still served so
// clients see *something* even when the upstream orchestrator is briefly
// unavailable.
type ProjectSnapshot struct {
	Project      string              `json:"project"`
	Tasks        []orchdto.TaskDTO   `json:"tasks"`
	Workers      []orchdto.WorkerDTO `json:"workers"`
	RecentEvents []orchdto.EventDTO  `json:"recent_events"`
	FetchedAt    time.Time           `json:"fetched_at"`
	Stale        bool                `json:"stale,omitempty"`
	LastError    string              `json:"last_error,omitempty"`
}

// Cache is the in-memory snapshot store populated by the poll loop and read
// by the HTTP handlers. The zero value is not ready for use; construct via
// NewCache so the staleness threshold is set.
type Cache struct {
	mu          sync.RWMutex
	byProject   map[string]*ProjectSnapshot
	staleAfter  time.Duration
	lastAttempt map[string]time.Time
	nowFn       func() time.Time
}

// NewCache returns an empty cache configured to mark snapshots stale when
// their FetchedAt is older than staleAfter relative to the current time.
// The PRD uses 2 * PollInterval as the default threshold — see Service.New.
func NewCache(staleAfter time.Duration) *Cache {
	return &Cache{
		byProject:   make(map[string]*ProjectSnapshot),
		lastAttempt: make(map[string]time.Time),
		staleAfter:  staleAfter,
		nowFn:       time.Now,
	}
}

// Set replaces the cached snapshot for snap.Project. The caller is
// responsible for populating FetchedAt; Cache does not observe wall time
// on successful writes so the poll loop's clock is the single source of
// truth.
func (c *Cache) Set(snap ProjectSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := snap
	c.byProject[snap.Project] = &copy
	c.lastAttempt[snap.Project] = snap.FetchedAt
}

// RecordFailure notes that the most recent poll for project failed with
// err. The previous snapshot — if any — is retained; only the stale marker
// and LastError are updated. This lets /world keep serving the last good
// data even while an orchestrator is briefly unavailable.
func (c *Cache) RecordFailure(project string, at time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt[project] = at
	snap, ok := c.byProject[project]
	if !ok {
		// No prior success — synthesise an empty stale snapshot so
		// /projects/:name still returns the project envelope.
		c.byProject[project] = &ProjectSnapshot{
			Project:   project,
			Stale:     true,
			LastError: err.Error(),
		}
		return
	}
	snap.LastError = err.Error()
	// Staleness is computed at read time; setting it here would race the
	// reader's clock. Leave Stale alone and let Get/All decide.
}

// Get returns the snapshot for project and true, or (nil, false) if the
// project has never been polled. The returned snapshot is a value copy, so
// callers may mutate it freely without racing the poll loop.
func (c *Cache) Get(project string) (ProjectSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.byProject[project]
	if !ok {
		return ProjectSnapshot{}, false
	}
	out := *snap
	out.Stale = c.isStaleLocked(project)
	return out, true
}

// All returns every snapshot in the cache, sorted by project name for
// stable output. Each returned value has its Stale field set to reflect
// the current time.
func (c *Cache) All() []ProjectSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ProjectSnapshot, 0, len(c.byProject))
	for name, snap := range c.byProject {
		copy := *snap
		copy.Stale = c.isStaleLocked(name)
		out = append(out, copy)
	}
	// Sort stabilises /world output and the summary digest; avoid
	// importing sort just for this — insertion sort is fine for the
	// handful of projects Kyle tracks.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Project > out[j].Project; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// isStaleLocked computes the staleness flag for project. Must be called
// with c.mu held (read or write is fine).
func (c *Cache) isStaleLocked(project string) bool {
	if c.staleAfter <= 0 {
		return false
	}
	last, ok := c.lastAttempt[project]
	if !ok || last.IsZero() {
		return true
	}
	return c.nowFn().Sub(last) > c.staleAfter
}
