package tui_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/tui"
	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// httpDataSourceWithHandler wires an HTTPDataSource to a caller-supplied
// httptest.Server handler. Tests use it to assert that a particular
// DataSource call hits the expected URL and decodes the canned response.
func httpDataSourceWithHandler(t *testing.T, project string, handler http.Handler) (*tui.HTTPDataSource, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return tui.NewHTTPDataSource(ts.URL, project), ts
}

// writeJSON marshals v and writes it to w, failing the test on error.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func TestHTTPDataSource_ListTasks_URLAndDecode(t *testing.T) {
	var gotPath, gotQuery string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		writeJSON(t, w, []orchdto.TaskDTO{
			{ID: "11111111-2222-3333-4444-555555555555", Title: "Do the thing", Status: "in_progress"},
			{ID: "22222222-2222-3333-4444-555555555555", Title: "Second", Status: "backlog"},
		})
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	got, err := ds.ListTasks(context.Background(), orchclient.TaskFilter{
		Status: "in_progress",
		Limit:  20,
		Offset: 5,
	})
	require.NoError(t, err)
	require.Equal(t, "/projects/myproj/tasks", gotPath)
	require.Contains(t, gotQuery, "status=in_progress")
	require.Contains(t, gotQuery, "limit=20")
	require.Contains(t, gotQuery, "offset=5")
	require.Len(t, got, 2)
	require.Equal(t, "Do the thing", got[0].Title)
	require.Equal(t, "in_progress", got[0].Status)
}

func TestHTTPDataSource_ListWorkers_URLAndDecode(t *testing.T) {
	var gotPath string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(t, w, []orchdto.WorkerDTO{
			{ID: "w-1", ContainerID: "c-1", Project: "myproj", AgentType: "coder", Status: "working"},
		})
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	got, err := ds.ListWorkers(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/projects/myproj/workers", gotPath)
	require.Len(t, got, 1)
	require.Equal(t, "coder", got[0].AgentType)
	require.Equal(t, "working", got[0].Status)
}

func TestHTTPDataSource_Events_PassesSince(t *testing.T) {
	var gotPath, gotQuery string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		writeJSON(t, w, []orchdto.EventDTO{
			{Timestamp: time.Now().UTC(), Type: "task_updated", Payload: json.RawMessage(`{}`)},
		})
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	since := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	got, err := ds.Events(context.Background(), since)
	require.NoError(t, err)
	require.Equal(t, "/events", gotPath)
	require.Contains(t, gotQuery, "since=")
	require.Len(t, got, 1)
	require.Equal(t, "task_updated", got[0].Type)
}

func TestHTTPDataSource_WorkerHistory_URL(t *testing.T) {
	var gotPath string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(t, w, orchdto.WorkerHistoryDTO{
			WorkerID: "w-1",
			Events: []orchdto.WorkerHistoryEntry{
				{Timestamp: time.Now().UTC(), Kind: "start", Detail: "spawned"},
			},
		})
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	got, err := ds.WorkerHistory(context.Background(), "w-1")
	require.NoError(t, err)
	require.Equal(t, "/workers/w-1/history", gotPath)
	require.Equal(t, "w-1", got.WorkerID)
	require.Len(t, got.Events, 1)
}

func TestHTTPDataSource_ListTasks_ServerErrorWrapped(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	_, err := ds.ListTasks(context.Background(), orchclient.TaskFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
	require.Contains(t, err.Error(), "kaboom")
}

func TestHTTPDataSource_ListWorkers_ServerErrorWrapped(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	_, err := ds.ListWorkers(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

// TestHTTPDataSource_StreamLogs_ContextCancelClosesCleanly asserts that
// cancelling the caller context interrupts the stream without leaking the
// connection or panicking on close. The server writes one chunk, flushes,
// then blocks until the client disconnects.
func TestHTTPDataSource_StreamLogs_ContextCancelClosesCleanly(t *testing.T) {
	opened := make(chan struct{}, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			_, _ = io.WriteString(w, "line-1\n")
			f.Flush()
		}
		opened <- struct{}{}
		<-r.Context().Done()
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	ctx, cancel := context.WithCancel(context.Background())
	rc, err := ds.StreamLogs(ctx, "container-xyz")
	require.NoError(t, err)

	// Wait for the server to send the first chunk so we know the stream
	// is live before cancelling.
	select {
	case <-opened:
	case <-time.After(2 * time.Second):
		cancel()
		_ = rc.Close()
		t.Fatal("server never reported stream open")
	}

	cancel()
	// A read after cancel should fail quickly; we don't care about the
	// exact error, only that it unblocks.
	buf := make([]byte, 16)
	done := make(chan struct{})
	go func() {
		_, _ = rc.Read(buf)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = rc.Close()
		t.Fatal("Read did not unblock after ctx cancel")
	}

	// Double-Close must be safe.
	require.NoError(t, rc.Close())
}

func TestHTTPDataSource_StreamLogs_ServerErrorWrapped(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such container", http.StatusNotFound)
	})
	ds, _ := httpDataSourceWithHandler(t, "myproj", h)

	rc, err := ds.StreamLogs(context.Background(), "c-1")
	require.Error(t, err)
	if rc != nil {
		_ = rc.Close()
	}
	require.Contains(t, err.Error(), "status 404")
}

func TestHTTPDataSource_StreamLogs_RejectsEmptyContainer(t *testing.T) {
	ds, _ := httpDataSourceWithHandler(t, "myproj",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	_, err := ds.StreamLogs(context.Background(), "")
	require.Error(t, err)
}

func TestResolveOrchURL_DefaultWhenEmpty(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:8080", tui.ResolveOrchURL("", ""))
	require.Equal(t, "http://127.0.0.1:9090", tui.ResolveOrchURL("", "9090"))
}

func TestResolveOrchURL_AddsHTTPScheme(t *testing.T) {
	got := tui.ResolveOrchURL("drem-orch-canvas:8080", "")
	require.Equal(t, "http://drem-orch-canvas:8080", got)
}

func TestResolveOrchURL_TrimsTrailingSlash(t *testing.T) {
	got := tui.ResolveOrchURL("http://orch.local:8080/", "")
	require.False(t, strings.HasSuffix(got, "/"), "got %q", got)
}

func TestResolveOrchURL_PreservesExplicitHTTPS(t *testing.T) {
	got := tui.ResolveOrchURL("https://orch.example.com", "")
	require.Equal(t, "https://orch.example.com", got)
}
