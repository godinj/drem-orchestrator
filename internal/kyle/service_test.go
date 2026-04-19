package kyle_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/kyle"
	"github.com/godinj/drem-orchestrator/internal/projects"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// fakeOrch is a tiny httptest-backed stand-in for an orchestrator. Each
// field is JSON-marshalled verbatim when the matching endpoint is hit, so
// tests can control exactly what Kyle will see.
type fakeOrch struct {
	workers []orchdto.WorkerDTO
	tasks   []orchdto.TaskDTO
	events  []orchdto.EventDTO
	// fail, when non-zero, causes the server to return 500 on every call.
	fail int32
	hits int32
}

func (f *fakeOrch) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects/{name}/workers", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.hits, 1)
		if atomic.LoadInt32(&f.fail) != 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeJSON(w, f.workers)
	})
	mux.HandleFunc("GET /projects/{name}/tasks", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.hits, 1)
		if atomic.LoadInt32(&f.fail) != 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeJSON(w, f.tasks)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.hits, 1)
		if atomic.LoadInt32(&f.fail) != 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeJSON(w, f.events)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// registryWith builds a Registry pointed at the given fake orchestrators.
// It uses testutil.SetupBareRepo so Add's bare-repo validation passes; the
// repo is otherwise unused by Kyle.
func registryWith(t *testing.T, servers map[string]*httptest.Server) *projects.Registry {
	t.Helper()
	dir := t.TempDir()
	reg := &projects.Registry{Path: filepath.Join(dir, "projects.toml")}
	for name, srv := range servers {
		require.NoError(t, reg.Add(projects.Project{
			Name:         name,
			BareRepoPath: testutil.SetupBareRepo(t),
			Language:     projects.LanguageGo,
			OrchURL:      srv.URL,
		}))
	}
	return reg
}

// TestPollOnce_PopulatesCache wires up two fake orchestrators, runs a
// single poll cycle, and verifies each project's snapshot reflects the
// fake's canned state.
func TestPollOnce_PopulatesCache(t *testing.T) {
	p1 := &fakeOrch{
		workers: []orchdto.WorkerDTO{{ID: "w1", AgentType: "coder-go", Status: "working"}},
		tasks:   []orchdto.TaskDTO{{ID: "t1", Title: "hello", Status: "planning"}},
		events:  []orchdto.EventDTO{{Timestamp: time.Unix(1700000000, 0).UTC(), Type: "commit"}},
	}
	p2 := &fakeOrch{
		workers: []orchdto.WorkerDTO{{ID: "w2", AgentType: "merger", Status: "working"}},
		tasks:   []orchdto.TaskDTO{{ID: "t2", Status: "in_progress"}, {ID: "t3", Status: "merging"}},
	}
	s1 := httptest.NewServer(p1.handler())
	defer s1.Close()
	s2 := httptest.NewServer(p2.handler())
	defer s2.Close()

	reg := registryWith(t, map[string]*httptest.Server{
		"alpha": s1,
		"beta":  s2,
	})

	svc := kyle.New(reg, "", 30*time.Second)
	// Poll cycle via Run with an immediately-cancelled context: Run calls
	// pollAll once before blocking on the ticker, so cancelling after a
	// short delay guarantees exactly one pass.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		_, ok := svc.Cache().Get("alpha")
		return ok
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	alpha, ok := svc.Cache().Get("alpha")
	require.True(t, ok)
	require.Equal(t, "alpha", alpha.Project)
	require.Len(t, alpha.Workers, 1)
	require.Equal(t, "w1", alpha.Workers[0].ID)
	require.Len(t, alpha.Tasks, 1)
	require.Len(t, alpha.RecentEvents, 1)
	require.False(t, alpha.Stale)

	beta, ok := svc.Cache().Get("beta")
	require.True(t, ok)
	require.Len(t, beta.Tasks, 2)
}

// TestRoutes_World verifies /world returns both projects and that the
// /projects/:name route returns the detailed view for one project.
func TestRoutes_World(t *testing.T) {
	p1 := &fakeOrch{workers: []orchdto.WorkerDTO{{ID: "w1", Status: "working"}}}
	s1 := httptest.NewServer(p1.handler())
	defer s1.Close()
	reg := registryWith(t, map[string]*httptest.Server{"alpha": s1})

	svc := kyle.New(reg, "", 30*time.Second)
	// Direct cache seed via pollOnce-equivalent: call ListWorkers/etc via
	// Run briefly, or inject a client and use the cache. We use Run here
	// because it exercises the real wiring.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		_, ok := svc.Cache().Get("alpha")
		return ok
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	mux := svc.Routes()

	// /world
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/world", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var world kyle.WorldResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &world))
	require.Len(t, world.Projects, 1)
	require.Equal(t, "alpha", world.Projects[0].Project)

	// /projects/alpha
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/projects/alpha", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var snap kyle.ProjectSnapshot
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &snap))
	require.Equal(t, "alpha", snap.Project)

	// /projects/missing → 404
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/projects/missing", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// TestRoutes_WorldSummary asserts the plain-text summary endpoint returns
// a block of the expected shape.
func TestRoutes_WorldSummary(t *testing.T) {
	reg := registryWith(t, nil)
	svc := kyle.New(reg, "", 30*time.Second)
	// Seed cache directly via the exported Set path.
	svc.Cache().Set(kyle.ProjectSnapshot{
		Project:   "alpha",
		Workers:   []orchdto.WorkerDTO{{ID: "w1", AgentType: "coder-go", Status: "working"}},
		Tasks:     []orchdto.TaskDTO{{ID: "t1", Status: "planning"}},
		FetchedAt: time.Unix(1700000000, 0).UTC(),
	})
	svc.SetNow(func() time.Time { return time.Unix(1700000000, 0).UTC() })

	rr := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/world/summary", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Header().Get("Content-Type"), "text/plain")
	body := rr.Body.String()
	require.Contains(t, body, "alpha:")
	require.Contains(t, body, "1 running")
	require.Contains(t, body, "1 in-flight")
}

// TestPoll_FailureRetainsOldSnapshot: when the upstream orchestrator
// starts returning 500, Kyle keeps the previous snapshot and marks it
// stale instead of dropping it.
func TestPoll_FailureRetainsOldSnapshot(t *testing.T) {
	p := &fakeOrch{
		workers: []orchdto.WorkerDTO{{ID: "w1", Status: "working"}},
	}
	srv := httptest.NewServer(p.handler())
	defer srv.Close()

	reg := registryWith(t, map[string]*httptest.Server{"alpha": srv})
	svc := kyle.New(reg, "", 30*time.Second)

	// First poll: seed the cache via the real poll path.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		snap, ok := svc.Cache().Get("alpha")
		return ok && len(snap.Workers) == 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	// Flip the fake to failure mode; trigger another Run cycle.
	atomic.StoreInt32(&p.fail, 1)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		_ = svc.Run(ctx2)
	}()
	// Wait for at least one additional hit to happen post-failure, then
	// stop. The first Run call already recorded a baseline; we just need
	// the poll loop to observe the 500.
	startingHits := atomic.LoadInt32(&p.hits)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&p.hits) > startingHits
	}, 2*time.Second, 10*time.Millisecond)
	cancel2()
	<-done2

	snap, ok := svc.Cache().Get("alpha")
	require.True(t, ok)
	require.Len(t, snap.Workers, 1, "old snapshot should be retained on failure")
	require.NotEmpty(t, snap.LastError, "LastError should be populated after a failed poll")
}

// TestDockerQueryHandler_Proxies ensures the /docker/query endpoint
// rewrites the request to the allowlisted upstream path and streams the
// response back.
func TestDockerQueryHandler_Proxies(t *testing.T) {
	var seenPath, seenQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"abc"}]`))
	}))
	defer upstream.Close()

	reg := registryWith(t, nil)
	svc := kyle.New(reg, upstream.URL, 30*time.Second)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/docker/query?target=containers&labels=drem.scope%3Dglobal", nil)
	svc.Routes().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "/containers", seenPath)
	require.Contains(t, seenQuery, "labels=drem.scope%3Dglobal")
	require.Contains(t, rr.Body.String(), `"id":"abc"`)
}

// TestDockerQueryHandler_RejectsUnknownTarget confirms the allowlist is
// enforced. A caller trying to hit an unlisted target gets 403, not a
// silent upstream call.
func TestDockerQueryHandler_RejectsUnknownTarget(t *testing.T) {
	reg := registryWith(t, nil)
	svc := kyle.New(reg, "http://nowhere.invalid", 30*time.Second)

	rr := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/docker/query?target=exec", nil))
	require.Equal(t, http.StatusForbidden, rr.Code)
}

// TestDockerQueryHandler_UnconfiguredReturns503 ensures a Kyle started
// without a docker-query-proxy URL returns a specific error rather than
// a generic one.
func TestDockerQueryHandler_UnconfiguredReturns503(t *testing.T) {
	reg := registryWith(t, nil)
	svc := kyle.New(reg, "", 30*time.Second)
	rr := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/docker/query?target=containers", nil))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// TestEventsFilter verifies the merged event endpoint honours the ?since
// and ?project query parameters.
func TestEventsFilter(t *testing.T) {
	reg := registryWith(t, nil)
	svc := kyle.New(reg, "", 30*time.Second)
	t1 := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	svc.Cache().Set(kyle.ProjectSnapshot{
		Project:      "alpha",
		RecentEvents: []orchdto.EventDTO{{Timestamp: t1, Type: "commit"}, {Timestamp: t2, Type: "crash"}},
		FetchedAt:    t2,
	})
	svc.Cache().Set(kyle.ProjectSnapshot{
		Project:      "beta",
		RecentEvents: []orchdto.EventDTO{{Timestamp: t1, Type: "merge_success"}},
		FetchedAt:    t2,
	})

	// No filter: all three events, newest first.
	rr := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/events", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var got []orchdto.EventDTO
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got, 3)
	require.True(t, !got[0].Timestamp.Before(got[1].Timestamp), "events not newest-first")

	// project=alpha filter.
	rr = httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/events?project=alpha", nil))
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got, 2)

	// since filter drops the earlier event.
	rr = httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/events?since="+t2.Format(time.RFC3339), nil))
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got, 1)
	require.Equal(t, "crash", got[0].Type)
}

// TestStaleFlag: a snapshot older than 2*PollInterval reports Stale=true.
func TestStaleFlag(t *testing.T) {
	reg := registryWith(t, nil)
	interval := time.Second
	svc := kyle.New(reg, "", interval)
	now := time.Now()
	svc.SetNow(func() time.Time { return now.Add(3 * time.Second) })
	svc.Cache().Set(kyle.ProjectSnapshot{
		Project:   "alpha",
		FetchedAt: now,
	})
	snap, ok := svc.Cache().Get("alpha")
	require.True(t, ok)
	require.True(t, snap.Stale, "snapshot older than 2*interval should be stale")
}

// TestClientInjection exercises the SetClient escape hatch. It confirms
// that a client installed before polling is reused in place of one the
// registry OrchURL would have produced.
func TestClientInjection(t *testing.T) {
	p := &fakeOrch{workers: []orchdto.WorkerDTO{{ID: "injected"}}}
	srv := httptest.NewServer(p.handler())
	defer srv.Close()

	reg := registryWith(t, map[string]*httptest.Server{"alpha": srv})
	// Replace URL with a bogus one: if SetClient works, polling still
	// succeeds because the injected client points at srv.URL.
	reg.Projects[0].OrchURL = "http://nowhere.invalid"

	svc := kyle.New(reg, "", 30*time.Second)
	svc.SetClient("alpha", orchclient.New(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		snap, ok := svc.Cache().Get("alpha")
		return ok && len(snap.Workers) == 1 && snap.Workers[0].ID == "injected"
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

// TestHealthz provides a trivial liveness endpoint regression.
func TestHealthz(t *testing.T) {
	reg := registryWith(t, nil)
	svc := kyle.New(reg, "", 30*time.Second)
	rr := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	body, _ := io.ReadAll(rr.Body)
	require.Equal(t, "ok\n", string(body))
}

// Helper: prevent the unused-context warning when a test needs a ctx only
// inside an eventually loop. (Kept for readability — a nolint wouldn't be
// clearer than this.)
var _ = context.Background
