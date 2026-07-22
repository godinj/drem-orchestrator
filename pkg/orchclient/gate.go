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

// Reject transitions a task away from its plan_review or test_review gate.
// Delivery failures use Verify with result=fail. reason is optional — pass "" to reject without feedback; the
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

// Pass is a deprecated compatibility call. Evidence-bearing delivery flows
// should use Verify; servers fail this call closed for testing_ready tasks.
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

// Fail returns a testing_ready task to implementation. It is retained as a
// compatibility alias; new operator and Codex workflows should use Reject.
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

// DeliveryArtifact returns the exact immutable handoff plus the current task
// state version needed for guarded verification and integration mutations.
func (c *Client) DeliveryArtifact(ctx context.Context, project string, taskID uuid.UUID) (orchdto.DeliveryEnvelopeDTO, error) {
	var out orchdto.DeliveryEnvelopeDTO
	path := gatePath(project, taskID, "artifact")
	if err := c.get(ctx, path, nil, &out); err != nil {
		return orchdto.DeliveryEnvelopeDTO{}, err
	}
	return out, nil
}

func (c *Client) VerifyDelivery(ctx context.Context, project string, taskID uuid.UUID, req orchdto.VerifyDeliveryRequest) (orchdto.VerificationRecordDTO, error) {
	var out orchdto.VerificationRecordDTO
	if err := c.postGate(ctx, gatePath(project, taskID, "verify"), req, &out); err != nil {
		return orchdto.VerificationRecordDTO{}, err
	}
	return out, nil
}

func (c *Client) IntegrateDelivery(ctx context.Context, project string, taskID uuid.UUID, req orchdto.IntegrateDeliveryRequest) (orchdto.IntegrationAuthorizationDTO, error) {
	var out orchdto.IntegrationAuthorizationDTO
	if err := c.postGate(ctx, gatePath(project, taskID, "integrate"), req, &out); err != nil {
		return orchdto.IntegrationAuthorizationDTO{}, err
	}
	return out, nil
}

func (c *Client) RequestDeliveryRework(ctx context.Context, project string, taskID uuid.UUID, req orchdto.RequestDeliveryReworkRequest) (orchdto.DeliveryReworkRecordDTO, error) {
	var out orchdto.DeliveryReworkRecordDTO
	if err := c.postGate(ctx, gatePath(project, taskID, "request-rework"), req, &out); err != nil {
		return orchdto.DeliveryReworkRecordDTO{}, err
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

// Retry transitions a failed task back to backlog so the orchestrator
// can redispatch it. Returns *ErrWrongStatus if the task is not in
// status=failed, *ErrNotFound if the task does not exist. See
// plans/v15-v16-failed-task-recovery.md for the recovery workflow.
func (c *Client) Retry(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "retry")
	if err := c.postGate(ctx, path, nil, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// CreateTask files a new task for the named project. title and description
// must both be non-empty; empty values are rejected client-side so callers do
// not spend a request on input the server is expected to reject.
func (c *Client) CreateTask(ctx context.Context, project, title, description string) (orchdto.TaskDTO, error) {
	if strings.TrimSpace(title) == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "task title is required"}
	}
	if strings.TrimSpace(description) == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "task description is required"}
	}
	payload := struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}{Title: title, Description: description}
	var out orchdto.TaskDTO
	path := "/projects/" + url.PathEscape(project) + "/tasks"
	if err := c.postGate(ctx, path, payload, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Archive marks obsolete non-running work as cancelled through the
// orchestrator's no-spawn archival endpoint. reason is required by the server;
// actor is optional and defaults server-side when empty.
func (c *Client) Archive(ctx context.Context, project string, taskID uuid.UUID, reason, actor string) (orchdto.TaskDTO, error) {
	if strings.TrimSpace(reason) == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "archive reason is required"}
	}
	payload := struct {
		Actor  string `json:"actor"`
		Reason string `json:"reason"`
		Mode   string `json:"mode"`
	}{Actor: actor, Reason: reason, Mode: "obsolete"}
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "archive")
	if err := c.postGate(ctx, path, payload, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Comment appends a C-Suite advisory comment to a task. Comments are accepted
// for tasks in any status and are included in future agent prompts.
func (c *Client) Comment(ctx context.Context, project string, taskID uuid.UUID, body string) (orchdto.TaskCommentDTO, error) {
	if strings.TrimSpace(body) == "" {
		return orchdto.TaskCommentDTO{}, &ErrBadRequest{Message: "comment body is required"}
	}
	payload := struct {
		Body string `json:"body"`
	}{Body: body}
	var out orchdto.TaskCommentDTO
	path := gatePath(project, taskID, "comments")
	if err := c.postGate(ctx, path, payload, &out); err != nil {
		return orchdto.TaskCommentDTO{}, err
	}
	return out, nil
}

// AuditRecovery records Kyle's structured autonomous-recovery audit event for
// a task. This is intentionally narrower than a generic event writer.
func (c *Client) AuditRecovery(ctx context.Context, project string, taskID uuid.UUID, req orchdto.RecoveryAuditRequest) (orchdto.EventDTO, error) {
	if strings.TrimSpace(req.Actor) == "" {
		return orchdto.EventDTO{}, &ErrBadRequest{Message: "audit actor is required"}
	}
	if strings.TrimSpace(req.PolicyRule) == "" {
		return orchdto.EventDTO{}, &ErrBadRequest{Message: "audit policy rule is required"}
	}
	if strings.TrimSpace(req.Action) == "" {
		return orchdto.EventDTO{}, &ErrBadRequest{Message: "audit action is required"}
	}
	var out orchdto.EventDTO
	path := gatePath(project, taskID, "audit-events")
	if err := c.postGate(ctx, path, req, &out); err != nil {
		return orchdto.EventDTO{}, err
	}
	return out, nil
}

// RecoverStaleAssignment classifies or repairs one task's stale assignment.
// The server refuses live working assignments with fresh heartbeats.
func (c *Client) RecoverStaleAssignment(ctx context.Context, project string, taskID uuid.UUID, req orchdto.StaleAssignmentRecoveryRequest) (orchdto.StaleAssignmentRecoveryDTO, error) {
	if req.DryRun == req.Apply {
		return orchdto.StaleAssignmentRecoveryDTO{}, &ErrBadRequest{Message: "exactly one of dry_run or apply is required"}
	}
	var out orchdto.StaleAssignmentRecoveryDTO
	path := gatePath(project, taskID, "recover/stale-assignment")
	if err := c.postGate(ctx, path, req, &out); err != nil {
		return orchdto.StaleAssignmentRecoveryDTO{}, err
	}
	return out, nil
}

// RecoverTask classifies or applies one named break-glass recovery action.
// Unsupported or unsafe actions return a refusal DTO rather than mutating.
func (c *Client) RecoverTask(ctx context.Context, project string, taskID uuid.UUID, action string, req orchdto.TaskRecoveryRequest) (orchdto.TaskRecoveryDTO, error) {
	if req.DryRun == req.Apply {
		return orchdto.TaskRecoveryDTO{}, &ErrBadRequest{Message: "exactly one of dry_run or apply is required"}
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return orchdto.TaskRecoveryDTO{}, &ErrBadRequest{Message: "recovery action is required"}
	}
	var out orchdto.TaskRecoveryDTO
	path := gatePath(project, taskID, "recover/"+url.PathEscape(action))
	if err := c.postGate(ctx, path, req, &out); err != nil {
		return orchdto.TaskRecoveryDTO{}, err
	}
	return out, nil
}

// gatePath returns the HTTP path for a gate mutation verb on the given
// task in the given project. The project name is URL-escaped; the UUID
// is emitted in its canonical 36-char form which is already URL-safe.
func gatePath(project string, taskID uuid.UUID, verb string) string {
	return "/projects/" + url.PathEscape(project) + "/tasks/" + taskID.String() + "/" + verb
}

// postGate is the shared POST helper for task mutations. It marshals body
// (nil omits the body entirely), POSTs to path, and decodes either a 2xx
// success body into out or a non-2xx error body
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.actor != "" {
		req.Header.Set("X-Drem-Actor", c.actor)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if resp.StatusCode == http.StatusNoContent || out == nil {
			return nil
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) == "" {
			return nil
		}
		return json.Unmarshal(raw, out)
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
	case http.StatusUnauthorized:
		return &ErrUnauthorized{Message: msg}
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
