package orchhttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// TestListTasksDBQueryTimeoutReturns503 is W1.3's regression test.
// It swaps in a *gorm.DB whose every query blocks via a GORM callback
// until the request context is cancelled, and asserts that the
// /tasks handler returns 503 after (approximately) the configured
// timeout — not 30 s, not the full 5 minutes, but the configured
// ceiling. The clock budget in the test is deliberately tight
// (200 ms) so the bound is observable without stretching CI latency.
func TestListTasksDBQueryTimeoutReturns503(t *testing.T) {
	// 200ms timeout keeps the test fast. Production default stays 5s.
	t.Setenv("DREM_ORCH_TASKS_QUERY_TIMEOUT_MS", "200")
	// Disable load-shedding so it isn't what we're asserting on.
	t.Setenv("DREM_ORCH_TASKS_MAX_INFLIGHT", "1024")
	t.Setenv("DREM_ORCH_MAX_INFLIGHT", "1024")

	// Stand up a fresh file-backed DB so we can register a slow
	// callback without fighting the shared in-memory cache.
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_journal_mode=WAL"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Project{},
		&model.Task{},
		&model.Agent{},
		&model.TaskEvent{},
		&model.Memory{},
		&model.TaskComment{},
	))
	project := testutil.CreateProject(t, db, "test-project", "/tmp/repo.git", "master")
	_ = project

	// Install a Query callback that blocks until the request context
	// cancels (i.e. the context.WithTimeout fires), simulating a
	// hung DB query.
	err = db.Callback().Query().Before("gorm:query").Register("slow_query_sim",
		func(tx *gorm.DB) {
			// Only block queries that came through a request context —
			// lets initial setup (CreateProject) proceed unhindered.
			// WithContext sets tx.Statement.Context to the inbound one;
			// requests arrive with a context that has a Deadline.
			if _, ok := tx.Statement.Context.Deadline(); !ok {
				return
			}
			<-tx.Statement.Context.Done()
		},
	)
	require.NoError(t, err)

	srv := orchhttp.New(db, "secret-token", nil, orchhttp.ProjectInfo{
		Name:     "test-project",
		Language: "go",
		OrchURL:  "http://localhost:8080",
	})
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	tStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/projects/test-project/tasks", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(tStart)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"expected 503 when DB query exceeds configured timeout")
	require.Less(t, elapsed, 2*time.Second,
		"handler took %s — timeout did not fire within configured ceiling", elapsed)
}
