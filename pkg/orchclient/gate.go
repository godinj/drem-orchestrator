package orchclient

// Gate mutation methods — the client-side half of the HTTP API's
// POST /projects/{name}/tasks/{id}/{verb} endpoints. These exist so
// CLI (`drem cli approve/reject/pass/fail/answer`) and TUI flows
// mutate pipeline state by calling the long-lived container
// orchestrator instead of opening the SQLite file directly and
// spinning up a second in-process orchestrator, which silently
// contends with the real one over the same DB.
//
// See plans/orch-api-gate-mutations.md for the authoritative spec:
// status codes, request/response bodies, and which transitions each
// verb accepts.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// Approve transitions a task past its plan_review or test_review gate.
// The server picks the correct transition based on the task's current
// status. Returns *ErrWrongStatus if the task is in a status that
// cannot be approved (for example, still in backlog).
//
// See plans/orch-api-gate-mutations.md for the full spec.
func (c *Client) Approve(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "approve")
	if err := c.postGate(ctx, path, nil, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Reject transitions a task away from its plan_review or test_review
// gate. reason is optional — pass "" to reject without feedback; the
// request body is always {"reason": "..."} so the server can
// distinguish "no reason given" from "field missing".
//
// See plans/orch-api-gate-mutations.md for the full spec.
func (c *Client) Reject(ctx context.Context, project string, taskID uuid.UUID, reason string) (orchdto.TaskDTO, error) {
	body := struct {
		Reason string `json:"reason"`
	}{Reason: reason}
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "reject")
	if err := c.postGate(ctx, path, body, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Pass transitions a testing_ready task to merging.
//
// See plans/orch-api-gate-mutations.md for the full spec.
func (c *Client) Pass(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "pass")
	if err := c.postGate(ctx, path, nil, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Fail transitions a testing_ready task to failed.
//
// See plans/orch-api-gate-mutations.md for the full spec.
func (c *Client) Fail(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "fail")
	if err := c.postGate(ctx, path, nil, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Answer supplies a clarification answer for a task in the
// needs_clarification status. body must be non-empty; empty or
// whitespace-only bodies are rejected client-side (no roundtrip) so
// callers cannot accidentally waste a request the server would have
// rejected with 400 anyway.
//
// See plans/orch-api-gate-mutations.md for the full spec.
func (c *Client) Answer(ctx context.Context, project string, taskID uuid.UUID, body string) (orchdto.TaskDTO, error) {
	if strings.TrimSpace(body) == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "answer body is required"}
	}
	payload := struct {
		Body string `json:"body"`
	}{Body: body}
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "answer")
	if err := c.postGate(ctx, path, payload, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// gatePath returns the HTTP path for a gate mutation verb on the given
// task in the given project. The project name is URL-escaped; the UUID
// is emitted in its canonical 36-char form which is already URL-safe.
func gatePath(project string, taskID uuid.UUID, verb string) string {
	return "/projects/" + url.PathEscape(project) + "/tasks/" + taskID.String() + "/" + verb
}

// postGate is the shared POST helper for the five gate mutations. It
// marshals body (nil omits the body entirely), POSTs to path, and
// decodes either a 200 success body into out or a non-2xx error body
// into the appropriate typed error defined in errors.go.
//
// The helper centralises three concerns: Content-Type is set only when
// a body is present, the server's {"error": "..."} envelope is parsed
// into typed errors, and the response body is always drained and
// closed so the HTTP connection can be reused.
func (c *Client) postGate(ctx context.Context, path string, body any, out any) error {
	var (
		reader      io.Reader
		contentType string
	)
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("orchclient: marshal request body: %w", err)
		}
		reader = bytes.NewReader(buf)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path, nil), reader)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return json.NewDecoder(resp.Body).Decode(out)
	}

	// Non-2xx: try to extract {"error": "..."} for the typed-error
	// message. Fall back to the raw body (truncated) if parsing fails,
	// so we never end up with a silent "orchclient: wrong status: "
	// with nothing after the colon.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := parseErrorEnvelope(raw)
	switch resp.StatusCode {
	case http.StatusBadRequest:
		return &ErrBadRequest{Message: msg}
	case http.StatusNotFound:
		return &ErrNotFound{Message: msg}
	case http.StatusConflict:
		return &ErrWrongStatus{Message: msg}
	case http.StatusInternalServerError:
		return &ErrServer{Message: msg}
	default:
		return fmt.Errorf("orchclient: POST %s: unexpected status %d: %s", path, resp.StatusCode, msg)
	}
}

// parseErrorEnvelope extracts the "error" field from the server's
// JSON error envelope. If the body isn't JSON or lacks the field, the
// function returns the trimmed raw body so the caller still sees
// something useful.
func parseErrorEnvelope(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Error != "" {
		return env.Error
	}
	return trimmed
}

// Compile-time sanity: ensure the typed errors implement error. This
// keeps the errors.go declarations load-bearing even if a future
// refactor drops a method.
var (
	_ error = (*ErrBadRequest)(nil)
	_ error = (*ErrNotFound)(nil)
	_ error = (*ErrWrongStatus)(nil)
	_ error = (*ErrServer)(nil)
)
