package orchclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchclient"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// gateHandler simulates the orchestrator's POST /projects/.../tasks/.../{verb}
// endpoints using the spec from plans/orch-api-gate-mutations.md. It
// records the method, path, headers, and request body so tests can
// assert wire-level correctness, and returns a caller-supplied status
// code and body. The handler is intentionally dumb: it does not know
// which verb was called, so tests configure it per-case.
type gateHandler struct {
	method     string
	path       string
	ctype      string
	rawBody    []byte
	calls      int32
	status     int
	respBody   string
	respCtype  string
	sleepFor   time.Duration
	stopBefore chan struct{} // if non-nil, closed to indicate server saw the request
}

func (g *gateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&g.calls, 1)
	g.method = r.Method
	g.path = r.URL.Path
	g.ctype = r.Header.Get("Content-Type")
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		g.rawBody = b
	}
	if g.stopBefore != nil {
		select {
		case <-g.stopBefore:
		default:
			close(g.stopBefore)
		}
	}
	if g.sleepFor > 0 {
		select {
		case <-time.After(g.sleepFor):
		case <-r.Context().Done():
			return
		}
	}
	ct := g.respCtype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	if g.status == 0 {
		g.status = http.StatusOK
	}
	w.WriteHeader(g.status)
	_, _ = io.WriteString(w, g.respBody)
}

// sampleDTO is the canonical happy-path TaskDTO the stub returns. Tests
// assert on a couple of fields to confirm decoding actually ran.
func sampleDTO(id uuid.UUID, status string) orchdto.TaskDTO {
	return orchdto.TaskDTO{
		ID:        id.String(),
		Title:     "example task",
		Status:    status,
		CreatedAt: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC),
	}
}

func sampleDTOJSON(t *testing.T, id uuid.UUID, status string) string {
	t.Helper()
	b, err := json.Marshal(sampleDTO(id, status))
	require.NoError(t, err)
	return string(b)
}

func newGateClient(t *testing.T, h http.Handler) (*orchclient.Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return orchclient.New(ts.URL), ts
}

// -- Approve -------------------------------------------------------------

func TestApproveHappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "planning_complete")}
	c, _ := newGateClient(t, h)

	got, err := c.Approve(context.Background(), "canvas", id)
	require.NoError(t, err)
	require.Equal(t, id.String(), got.ID)
	require.Equal(t, "planning_complete", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/approve", h.path)
	require.Equal(t, http.MethodPost, h.method)
}

func TestApproveBadRequest(t *testing.T) {
	h := &gateHandler{status: http.StatusBadRequest, respBody: `{"error":"malformed uuid"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var bad *orchclient.ErrBadRequest
	require.True(t, errors.As(err, &bad), "want *ErrBadRequest, got %T: %v", err, err)
	require.Contains(t, bad.Message, "malformed uuid")
}

func TestApproveNotFound(t *testing.T) {
	h := &gateHandler{status: http.StatusNotFound, respBody: `{"error":"no such task"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var nf *orchclient.ErrNotFound
	require.True(t, errors.As(err, &nf), "want *ErrNotFound, got %T: %v", err, err)
	require.Contains(t, nf.Message, "no such task")
}

func TestApproveWrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task is in backlog, cannot approve"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
	require.Contains(t, ws.Message, "cannot approve")
}

func TestApproveServerError(t *testing.T) {
	h := &gateHandler{status: http.StatusInternalServerError, respBody: `{"error":"db exploded"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var se *orchclient.ErrServer
	require.True(t, errors.As(err, &se), "want *ErrServer, got %T: %v", err, err)
	require.Contains(t, se.Message, "db exploded")
}

func TestApproveNetworkError(t *testing.T) {
	// Start a server, get the URL, then close it so the client hits a
	// dead address. The client should surface a non-nil error.
	h := &gateHandler{status: http.StatusOK, respBody: "{}"}
	ts := httptest.NewServer(h)
	c := orchclient.New(ts.URL)
	ts.Close()

	_, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	// We don't assert on the error type — network errors come from the
	// net/http stack and their shape varies across Go versions.
}

func TestApproveNoContentSuccessReturnsZeroDTO(t *testing.T) {
	h := &gateHandler{status: http.StatusNoContent}
	c, _ := newGateClient(t, h)

	got, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.NoError(t, err)
	require.Equal(t, orchdto.TaskDTO{}, got)
}

func TestApproveEmptyOKSuccessReturnsZeroDTO(t *testing.T) {
	h := &gateHandler{status: http.StatusOK, respBody: "   \n"}
	c, _ := newGateClient(t, h)

	got, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.NoError(t, err)
	require.Equal(t, orchdto.TaskDTO{}, got)
}

// -- Reject --------------------------------------------------------------

func TestRejectHappyPathWithReason(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "rejected")}
	c, _ := newGateClient(t, h)

	got, err := c.Reject(context.Background(), "canvas", id, "plan too vague")
	require.NoError(t, err)
	require.Equal(t, "rejected", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/reject", h.path)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "application/json", h.ctype)

	var payload struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "plan too vague", payload.Reason)
}

func TestRejectHappyPathEmptyReason(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "rejected")}
	c, _ := newGateClient(t, h)

	_, err := c.Reject(context.Background(), "canvas", id, "")
	require.NoError(t, err)
	// Empty reason still sends {"reason":""} — the server decides whether
	// that's acceptable. We assert the payload round-trips.
	var payload struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "", payload.Reason)
	require.Equal(t, "application/json", h.ctype)
}

func TestRejectWrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task is merging, cannot reject"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Reject(context.Background(), "canvas", uuid.New(), "nope")
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
}

func TestArchiveHappyPathWithReasonAndActor(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "cancelled")}
	c, _ := newGateClient(t, h)

	got, err := c.Archive(context.Background(), "canvas", id, "superseded", "kyle")
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/archive", h.path)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "application/json", h.ctype)

	var payload struct {
		Actor  string `json:"actor"`
		Reason string `json:"reason"`
		Mode   string `json:"mode"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "kyle", payload.Actor)
	require.Equal(t, "superseded", payload.Reason)
	require.Equal(t, "obsolete", payload.Mode)
}

func TestArchiveEmptyReasonReturnsBadRequestWithoutNetwork(t *testing.T) {
	h := &gateHandler{status: http.StatusOK, respBody: `{}`}
	c, _ := newGateClient(t, h)

	_, err := c.Archive(context.Background(), "canvas", uuid.New(), "  ", "kyle")
	require.Error(t, err)
	var bad *orchclient.ErrBadRequest
	require.True(t, errors.As(err, &bad), "want *ErrBadRequest, got %T: %v", err, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls))
}

// -- Pass ----------------------------------------------------------------

func TestPassHappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "merging")}
	c, _ := newGateClient(t, h)

	got, err := c.Pass(context.Background(), "canvas", id)
	require.NoError(t, err)
	require.Equal(t, "merging", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/pass", h.path)
	require.Equal(t, http.MethodPost, h.method)
}

func TestPassWrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"not testing_ready"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Pass(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws))
}

// -- Fail ----------------------------------------------------------------

func TestFailHappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "failed")}
	c, _ := newGateClient(t, h)

	got, err := c.Fail(context.Background(), "canvas", id)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/fail", h.path)
	require.Equal(t, http.MethodPost, h.method)
}

func TestFailWrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"not testing_ready"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Fail(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws))
}

// -- Answer --------------------------------------------------------------

func TestAnswerHappyPath(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "planning")}
	c, _ := newGateClient(t, h)

	got, err := c.Answer(context.Background(), "canvas", id, "use port 9090")
	require.NoError(t, err)
	require.Equal(t, "planning", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/answer", h.path)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "application/json", h.ctype)

	var payload struct {
		Body string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "use port 9090", payload.Body)
}

// TestAnswerEmptyBodyGuardsNetwork asserts that Answer with an empty
// body fails client-side (before any network call) so callers don't
// waste a roundtrip on input the server is guaranteed to reject.
func TestAnswerEmptyBodyGuardsNetwork(t *testing.T) {
	h := &gateHandler{status: http.StatusOK, respBody: "{}"}
	c, _ := newGateClient(t, h)

	_, err := c.Answer(context.Background(), "canvas", uuid.New(), "")
	require.Error(t, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls), "expected no network call for empty body")
}

func TestAnswerBadRequest(t *testing.T) {
	h := &gateHandler{status: http.StatusBadRequest, respBody: `{"error":"body must be non-empty"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Answer(context.Background(), "canvas", uuid.New(), "   ")
	require.Error(t, err)
	var bad *orchclient.ErrBadRequest
	require.True(t, errors.As(err, &bad), "want *ErrBadRequest, got %T: %v", err, err)
}

func TestAnswerWrongStatus(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task not in needs_clarification"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Answer(context.Background(), "canvas", uuid.New(), "sure")
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws))
}

// -- Retry ---------------------------------------------------------------

func TestClient_Retry_Success(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "backlog")}
	c, _ := newGateClient(t, h)

	got, err := c.Retry(context.Background(), "canvas", id)
	require.NoError(t, err)
	require.Equal(t, id.String(), got.ID)
	require.Equal(t, "backlog", got.Status)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/retry", h.path)
	require.Equal(t, http.MethodPost, h.method)
}

func TestClient_Retry_NotFound(t *testing.T) {
	h := &gateHandler{status: http.StatusNotFound, respBody: `{"error":"task not found"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Retry(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var nf *orchclient.ErrNotFound
	require.True(t, errors.As(err, &nf), "want *ErrNotFound, got %T: %v", err, err)
	require.Contains(t, nf.Message, "task not found")
}

func TestClient_Retry_Conflict(t *testing.T) {
	h := &gateHandler{status: http.StatusConflict, respBody: `{"error":"task in status \"in_progress\", expected one of [failed]"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Retry(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var ws *orchclient.ErrWrongStatus
	require.True(t, errors.As(err, &ws), "want *ErrWrongStatus, got %T: %v", err, err)
	require.Contains(t, ws.Message, "failed")
}

func TestClient_Retry_BadRequest(t *testing.T) {
	h := &gateHandler{status: http.StatusBadRequest, respBody: `{"error":"invalid task id"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Retry(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
	var bad *orchclient.ErrBadRequest
	require.True(t, errors.As(err, &bad), "want *ErrBadRequest, got %T: %v", err, err)
}

// -- CreateTask ----------------------------------------------------------

func TestClient_CreateTask_Success(t *testing.T) {
	id := uuid.New()
	h := &gateHandler{status: http.StatusCreated, respBody: sampleDTOJSON(t, id, "backlog")}
	c, _ := newGateClient(t, h)

	got, err := c.CreateTask(context.Background(), "my-project", "Add thing", "Build the thing")
	require.NoError(t, err)
	require.Equal(t, id.String(), got.ID)
	require.Equal(t, "backlog", got.Status)
	require.Equal(t, "/projects/my-project/tasks", h.path)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "application/json", h.ctype)

	var payload struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "Add thing", payload.Title)
	require.Equal(t, "Build the thing", payload.Description)
}

func TestClient_CreateTask_EmptyTitleGuardsNetwork(t *testing.T) {
	h := &gateHandler{status: http.StatusOK, respBody: `{}`}
	c, _ := newGateClient(t, h)

	_, err := c.CreateTask(context.Background(), "canvas", " ", "description")
	require.Error(t, err)
	var bad *orchclient.ErrBadRequest
	require.True(t, errors.As(err, &bad), "want *ErrBadRequest, got %T: %v", err, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls), "expected no network call for empty title")
}

func TestClient_CreateTask_EmptyDescriptionGuardsNetwork(t *testing.T) {
	h := &gateHandler{status: http.StatusOK, respBody: `{}`}
	c, _ := newGateClient(t, h)

	_, err := c.CreateTask(context.Background(), "canvas", "title", "\t")
	require.Error(t, err)
	var bad *orchclient.ErrBadRequest
	require.True(t, errors.As(err, &bad), "want *ErrBadRequest, got %T: %v", err, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls), "expected no network call for empty description")
}

// -- Comment -------------------------------------------------------------

func TestClient_Comment_Success(t *testing.T) {
	id := uuid.New()
	commentID := uuid.New()
	body, err := json.Marshal(orchdto.TaskCommentDTO{ID: commentID.String(), TaskID: id.String(), Author: "csuite", Body: "new context"})
	require.NoError(t, err)
	h := &gateHandler{status: http.StatusOK, respBody: string(body)}
	c, _ := newGateClient(t, h)

	got, err := c.Comment(context.Background(), "canvas", id, "new context")
	require.NoError(t, err)
	require.Equal(t, commentID.String(), got.ID)
	require.Equal(t, id.String(), got.TaskID)
	require.Equal(t, "/projects/canvas/tasks/"+id.String()+"/comments", h.path)
	require.Equal(t, http.MethodPost, h.method)
	require.Equal(t, "application/json", h.ctype)

	var payload struct {
		Body string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(h.rawBody, &payload))
	require.Equal(t, "new context", payload.Body)
}

func TestClient_Comment_EmptyBodyGuardsNetwork(t *testing.T) {
	h := &gateHandler{status: http.StatusOK, respBody: `{}`}
	c, _ := newGateClient(t, h)

	_, err := c.Comment(context.Background(), "canvas", uuid.New(), " ")
	require.Error(t, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&h.calls), "expected no network call for empty body")
}

// -- Header + path + method correctness ----------------------------------

// TestAllMethodsSendJSONContentType drives each of the five methods
// through a capturing stub and asserts Content-Type is set when the
// method sends a body. Approve / Pass / Fail send no body, so we
// exempt them; Reject and Answer do.
func TestGateMethodsSendJSONContentTypeWhenBody(t *testing.T) {
	type tc struct {
		name    string
		call    func(c *orchclient.Client, id uuid.UUID) error
		wantCT  bool
		wantVal string
	}
	cases := []tc{
		{"approve", func(c *orchclient.Client, id uuid.UUID) error {
			_, err := c.Approve(context.Background(), "p", id)
			return err
		}, false, ""},
		{"reject", func(c *orchclient.Client, id uuid.UUID) error {
			_, err := c.Reject(context.Background(), "p", id, "why")
			return err
		}, true, "application/json"},
		{"pass", func(c *orchclient.Client, id uuid.UUID) error {
			_, err := c.Pass(context.Background(), "p", id)
			return err
		}, false, ""},
		{"fail", func(c *orchclient.Client, id uuid.UUID) error {
			_, err := c.Fail(context.Background(), "p", id)
			return err
		}, false, ""},
		{"answer", func(c *orchclient.Client, id uuid.UUID) error {
			_, err := c.Answer(context.Background(), "p", id, "ok")
			return err
		}, true, "application/json"},
		{"archive", func(c *orchclient.Client, id uuid.UUID) error {
			_, err := c.Archive(context.Background(), "p", id, "obsolete", "kyle")
			return err
		}, true, "application/json"},
	}
	for _, tcase := range cases {
		t.Run(tcase.name, func(t *testing.T) {
			id := uuid.New()
			h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "x")}
			c, _ := newGateClient(t, h)
			require.NoError(t, tcase.call(c, id))
			if tcase.wantCT {
				require.Equal(t, tcase.wantVal, h.ctype)
			}
		})
	}
}

func TestGateMethodsAllUsePOST(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name string
		call func(c *orchclient.Client) error
	}{
		{"approve", func(c *orchclient.Client) error { _, err := c.Approve(context.Background(), "p", id); return err }},
		{"reject", func(c *orchclient.Client) error { _, err := c.Reject(context.Background(), "p", id, ""); return err }},
		{"pass", func(c *orchclient.Client) error { _, err := c.Pass(context.Background(), "p", id); return err }},
		{"fail", func(c *orchclient.Client) error { _, err := c.Fail(context.Background(), "p", id); return err }},
		{"answer", func(c *orchclient.Client) error { _, err := c.Answer(context.Background(), "p", id, "x"); return err }},
		{"archive", func(c *orchclient.Client) error {
			_, err := c.Archive(context.Background(), "p", id, "obsolete", "kyle")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "x")}
			c, _ := newGateClient(t, h)
			require.NoError(t, tc.call(c))
			require.Equal(t, http.MethodPost, h.method)
		})
	}
}

func TestGateMethodsURLPathShape(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name     string
		verb     string
		call     func(c *orchclient.Client) error
		wantPath string
	}{
		{"approve", "approve", func(c *orchclient.Client) error {
			_, err := c.Approve(context.Background(), "my-proj", id)
			return err
		}, "/projects/my-proj/tasks/" + id.String() + "/approve"},
		{"reject", "reject", func(c *orchclient.Client) error {
			_, err := c.Reject(context.Background(), "my-proj", id, "r")
			return err
		}, "/projects/my-proj/tasks/" + id.String() + "/reject"},
		{"pass", "pass", func(c *orchclient.Client) error {
			_, err := c.Pass(context.Background(), "my-proj", id)
			return err
		}, "/projects/my-proj/tasks/" + id.String() + "/pass"},
		{"fail", "fail", func(c *orchclient.Client) error {
			_, err := c.Fail(context.Background(), "my-proj", id)
			return err
		}, "/projects/my-proj/tasks/" + id.String() + "/fail"},
		{"answer", "answer", func(c *orchclient.Client) error {
			_, err := c.Answer(context.Background(), "my-proj", id, "b")
			return err
		}, "/projects/my-proj/tasks/" + id.String() + "/answer"},
		{"archive", "archive", func(c *orchclient.Client) error {
			_, err := c.Archive(context.Background(), "my-proj", id, "obsolete", "kyle")
			return err
		}, "/projects/my-proj/tasks/" + id.String() + "/archive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "x")}
			c, _ := newGateClient(t, h)
			require.NoError(t, tc.call(c))
			require.Equal(t, tc.wantPath, h.path)
		})
	}
}

// -- Context cancellation ------------------------------------------------

func TestGateMethodsHonorContextCancel(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name string
		call func(c *orchclient.Client, ctx context.Context) error
	}{
		{"approve", func(c *orchclient.Client, ctx context.Context) error {
			_, err := c.Approve(ctx, "p", id)
			return err
		}},
		{"reject", func(c *orchclient.Client, ctx context.Context) error {
			_, err := c.Reject(ctx, "p", id, "r")
			return err
		}},
		{"pass", func(c *orchclient.Client, ctx context.Context) error {
			_, err := c.Pass(ctx, "p", id)
			return err
		}},
		{"fail", func(c *orchclient.Client, ctx context.Context) error {
			_, err := c.Fail(ctx, "p", id)
			return err
		}},
		{"answer", func(c *orchclient.Client, ctx context.Context) error {
			_, err := c.Answer(ctx, "p", id, "body")
			return err
		}},
		{"archive", func(c *orchclient.Client, ctx context.Context) error {
			_, err := c.Archive(ctx, "p", id, "obsolete", "kyle")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Handler sleeps longer than the context deadline so the
			// client must abort via ctx.Done() for the test to pass.
			h := &gateHandler{status: http.StatusOK, respBody: sampleDTOJSON(t, id, "x"), sleepFor: 2 * time.Second}
			c, _ := newGateClient(t, h)

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			start := time.Now()
			err := tc.call(c, ctx)
			elapsed := time.Since(start)
			require.Error(t, err)
			require.Less(t, elapsed, time.Second, "expected quick abort on ctx cancel, took %s", elapsed)
		})
	}
}

// -- Misc: unexpected status --------------------------------------------

func TestApproveUnexpectedStatus(t *testing.T) {
	// 418 isn't one of the four documented classes; the client should
	// still surface a non-nil error rather than silently decode garbage.
	h := &gateHandler{status: http.StatusTeapot, respBody: `{"error":"short and stout"}`}
	c, _ := newGateClient(t, h)

	_, err := c.Approve(context.Background(), "canvas", uuid.New())
	require.Error(t, err)
}
