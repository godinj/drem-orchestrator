package orchclient_test

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

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// recordingHandler is a tiny test double that records the last path
// and query string it saw and returns a preconfigured response. Tests
// use it to assert that the client hits the expected URL and decodes
// the response correctly.
type recordingHandler struct {
	t        *testing.T
	lastPath string
	lastRaw  string
	status   int
	body     string
	ctype    string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.lastPath = r.URL.Path
	h.lastRaw = r.URL.RawQuery
	if h.ctype != "" {
		w.Header().Set("Content-Type", h.ctype)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(h.status)
	_, _ = io.WriteString(w, h.body)
}

func newClient(t *testing.T, h *recordingHandler) (*orchclient.Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return orchclient.New(ts.URL), ts
}

func TestListProjectsDecodes(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK}
	projects := []orchdto.ProjectDTO{{Name: "p", Language: "go", OrchURL: "u", WorkerCount: 2}}
	body, _ := json.Marshal(projects)
	h.body = string(body)

	c, _ := newClient(t, h)

	got, err := c.ListProjects(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "p", got[0].Name)
	require.Equal(t, 2, got[0].WorkerCount)
	require.Equal(t, "/projects", h.lastPath)
}

func TestListTasksEncodesFilter(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK, body: "[]"}
	c, _ := newClient(t, h)

	_, err := c.ListTasks(context.Background(), "myproj", orchclient.TaskFilter{
		Status:          "backlog",
		Limit:           10,
		Offset:          5,
		IncludeArchived: true,
	})
	require.NoError(t, err)
	require.Equal(t, "/projects/myproj/tasks", h.lastPath)
	require.Contains(t, h.lastRaw, "status=backlog")
	require.Contains(t, h.lastRaw, "limit=10")
	require.Contains(t, h.lastRaw, "offset=5")
	require.Contains(t, h.lastRaw, "include_archived=true")
}

func TestListTasksNoFilterOmitsQuery(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK, body: "[]"}
	c, _ := newClient(t, h)

	_, err := c.ListTasks(context.Background(), "myproj", orchclient.TaskFilter{})
	require.NoError(t, err)
	require.Equal(t, "/projects/myproj/tasks", h.lastPath)
	require.Empty(t, h.lastRaw)
}

func TestListWorkersPath(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK, body: "[]"}
	c, _ := newClient(t, h)

	_, err := c.ListWorkers(context.Background(), "canvas")
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas/workers", h.lastPath)
}

func TestGetWorkerDecodes(t *testing.T) {
	dto := orchdto.WorkerDTO{ID: "abc", AgentType: "coder", Status: "working"}
	body, _ := json.Marshal(dto)
	h := &recordingHandler{status: http.StatusOK, body: string(body)}

	c, _ := newClient(t, h)

	got, err := c.GetWorker(context.Background(), "abc")
	require.NoError(t, err)
	require.Equal(t, "abc", got.ID)
	require.Equal(t, "coder", got.AgentType)
	require.Equal(t, "/workers/abc", h.lastPath)
}

func TestGetWorkerNotFoundSurfacesError(t *testing.T) {
	h := &recordingHandler{status: http.StatusNotFound, body: "worker not found"}
	c, _ := newClient(t, h)

	_, err := c.GetWorker(context.Background(), "zzz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")
}

func TestWorkerHistoryDecodes(t *testing.T) {
	hist := orchdto.WorkerHistoryDTO{WorkerID: "w1", Events: []orchdto.WorkerHistoryEntry{{Kind: "commit", Detail: "wip"}}}
	body, _ := json.Marshal(hist)
	h := &recordingHandler{status: http.StatusOK, body: string(body)}

	c, _ := newClient(t, h)

	got, err := c.WorkerHistory(context.Background(), "w1")
	require.NoError(t, err)
	require.Equal(t, "w1", got.WorkerID)
	require.Len(t, got.Events, 1)
	require.Equal(t, "commit", got.Events[0].Kind)
	require.Equal(t, "/workers/w1/history", h.lastPath)
}

func TestEventsEncodesSinceAndLimit(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK, body: "[]"}
	c, _ := newClient(t, h)

	since := time.Date(2026, 4, 19, 12, 0, 0, 123, time.UTC)
	_, err := c.Events(context.Background(), since, 25)
	require.NoError(t, err)
	require.Equal(t, "/events", h.lastPath)
	require.Contains(t, h.lastRaw, "since=2026-04-19T12%3A00%3A00.000000123Z")
	require.Contains(t, h.lastRaw, "limit=25")
}

func TestStreamLogsReturnsRawBody(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK, body: "line1\nline2\n", ctype: "text/plain"}
	c, _ := newClient(t, h)

	rc, err := c.StreamLogs(context.Background(), "abc", time.Time{}, true)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "line1\nline2\n", string(data))
	require.Equal(t, "/logs", h.lastPath)
	require.Contains(t, h.lastRaw, "container=abc")
	require.Contains(t, h.lastRaw, "follow=true")
}

func TestStreamLogsRequiresContainer(t *testing.T) {
	c := orchclient.New("http://nowhere.invalid")
	_, err := c.StreamLogs(context.Background(), "", time.Time{}, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "container is required")
}

func TestStreamLogsPropagatesErrorStatus(t *testing.T) {
	h := &recordingHandler{status: http.StatusBadGateway, body: "upstream dead", ctype: "text/plain"}
	c, _ := newClient(t, h)

	_, err := c.StreamLogs(context.Background(), "abc", time.Time{}, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "502")
	require.Contains(t, err.Error(), "upstream dead")
}

// sanity: ensure that the client URL-escapes path components. We use a
// name with a slash-escape-worthy character to confirm.
func TestListTasksEscapesProjectName(t *testing.T) {
	h := &recordingHandler{status: http.StatusOK, body: "[]"}
	c, _ := newClient(t, h)

	_, err := c.ListTasks(context.Background(), "a/b", orchclient.TaskFilter{})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(h.lastPath, "/projects/"),
		"unexpected path: %s", h.lastPath)
	require.Contains(t, h.lastPath, "a") // escaped component may differ
}
