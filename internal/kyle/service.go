package kyle

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/godinj/drem-orchestrator/internal/projects"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// DefaultPollInterval is the wall-clock cadence Kyle uses to refresh every
// project's snapshot when the operator does not override it via flag. The
// value is a compromise between freshness for C-Suite's reporting needs and
// the load a tight poll puts on each orchestrator's SQLite handle.
const DefaultPollInterval = 30 * time.Second

// Service is the Kyle runtime. It owns the cache, the per-project HTTP
// clients, the poll loop, and the HTTP handlers. Construct via New; the
// zero value is not usable.
type Service struct {
	registry       *projects.Registry
	dockerQueryURL string
	pollInterval   time.Duration
	cache          *Cache

	mu      sync.Mutex
	clients map[string]*orchclient.Client
	nowFn   func() time.Time
}

// New constructs a Service that will poll every project in reg every
// interval and serve Docker query requests by proxying to dockerQueryURL.
// An empty dockerQueryURL disables the /docker/query endpoint (it returns
// 503 so operators see a clear error rather than a cryptic 404).
//
// The cache's staleness threshold is 2 * interval, matching the PRD.
func New(reg *projects.Registry, dockerQueryURL string, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	return &Service{
		registry:       reg,
		dockerQueryURL: dockerQueryURL,
		pollInterval:   interval,
		cache:          NewCache(2 * interval),
		clients:        make(map[string]*orchclient.Client),
		nowFn:          time.Now,
	}
}

// Run blocks until ctx is cancelled, polling every registered project on
// pollInterval boundaries. The first poll runs immediately so callers
// observe a populated cache without waiting a full tick. Any per-project
// failure is logged but never causes Run to exit — the only exit path is
// ctx.Done.
func (s *Service) Run(ctx context.Context) error {
	s.pollAll(ctx)
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.pollAll(ctx)
		}
	}
}

// Routes returns an http.Handler with every Kyle endpoint mounted. A fresh
// ServeMux is returned per call so tests can mount the handler standalone.
//
// Endpoints:
//   - GET /world                 unified JSON across all projects
//   - GET /world/summary         compact text digest
//   - GET /projects/:name        one project's detailed state
//   - GET /events                merged event stream (filter via since/project)
//   - GET /docker/query          proxy to docker-query-proxy (allowlisted)
//   - GET /healthz               liveness probe
func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /world", s.handleWorld)
	mux.HandleFunc("GET /world/summary", s.handleWorldSummary)
	mux.HandleFunc("GET /projects/{name}", s.handleProject)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /docker/query", s.DockerQueryHandler)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return recoverPanics(logRequests(mux))
}

// Cache exposes the underlying cache for tests. Production code interacts
// with the cache indirectly via the HTTP endpoints or the poll loop.
func (s *Service) Cache() *Cache { return s.cache }

// SetClient injects a pre-built orchclient.Client for project, bypassing
// the default constructor. Tests use this to point the poller at an
// httptest.NewServer without round-tripping through the registry's
// OrchURL.
func (s *Service) SetClient(project string, c *orchclient.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[project] = c
}

// SetNow replaces the clock used by the cache's staleness check and the
// poll loop's timestamping. Tests use this to make "stale" deterministic.
func (s *Service) SetNow(fn func() time.Time) {
	s.nowFn = fn
	s.cache.nowFn = fn
}

// now returns the current time via the service's injectable clock.
func (s *Service) now() time.Time { return s.nowFn() }

// WorldResponse is the JSON shape returned by GET /world. Each snapshot
// is annotated with its staleness flag so UIs can render a warning badge
// without re-deriving it from FetchedAt.
type WorldResponse struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Projects    []ProjectSnapshot `json:"projects"`
}

func (s *Service) handleWorld(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, WorldResponse{
		GeneratedAt: s.now(),
		Projects:    s.cache.All(),
	})
}

func (s *Service) handleWorldSummary(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(RenderSummary(s.now(), s.cache.All())))
}

func (s *Service) handleProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap, ok := s.cache.Get(name)
	if !ok {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleEvents merges per-project event streams into a single newest-first
// slice. ?since=RFC3339 drops older events; ?project=<name> restricts to
// one project. Kyle does not call the upstream orchestrator here — it
// serves from the cache populated by the poll loop so that event reads are
// cheap and decoupled from orchestrator availability.
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since time.Time
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "invalid since (want RFC3339)", http.StatusBadRequest)
			return
		}
		since = t
	}
	projectFilter := q.Get("project")

	var merged []orchdto.EventDTO
	for _, snap := range s.cache.All() {
		if projectFilter != "" && snap.Project != projectFilter {
			continue
		}
		for _, ev := range snap.RecentEvents {
			if !since.IsZero() && ev.Timestamp.Before(since) {
				continue
			}
			merged = append(merged, ev)
		}
	}
	// Newest-first ordering across the merged slice.
	for i := 1; i < len(merged); i++ {
		for j := i; j > 0 && merged[j-1].Timestamp.Before(merged[j].Timestamp); j-- {
			merged[j-1], merged[j] = merged[j], merged[j-1]
		}
	}
	writeJSON(w, http.StatusOK, merged)
}

func (s *Service) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// writeJSON encodes body as JSON, setting the status code and content
// type. Encoding errors are logged but not surfaced to the client, which
// has already seen WriteHeader.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("kyle writeJSON failed", "err", err)
	}
}
