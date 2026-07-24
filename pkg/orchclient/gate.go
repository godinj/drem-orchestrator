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
	"crypto/sha256"
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

// RevisePlan replaces an adapter-authored execution plan after review feedback
// without routing the task through the planner again. The server validates the
// replacement against the immutable task specification.
func (c *Client) RevisePlan(ctx context.Context, project string, taskID uuid.UUID, req orchdto.ReviseTaskPlanRequest) (orchdto.TaskDTO, error) {
	return c.RevisePlanWithIdempotencyKey(ctx, project, taskID, req, "")
}

func (c *Client) RevisePlanWithIdempotencyKey(ctx context.Context, project string, taskID uuid.UUID, req orchdto.ReviseTaskPlanRequest, idempotencyKey string) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "revise-plan")
	if err := c.postGateWithIdempotencyKey(ctx, path, req, &out, idempotencyKey); err != nil {
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

func (c *Client) SubmitHostRework(ctx context.Context, project string, taskID uuid.UUID, req orchdto.SubmitHostReworkRequest) (orchdto.HostReworkSubmissionDTO, error) {
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.CommitSHA) == "" || strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.IdempotencyKey) == "" || req.ObservedStateVersion == 0 {
		return orchdto.HostReworkSubmissionDTO{}, &ErrBadRequest{Message: "submit rework requires session, commit, actor, observed state version, and idempotency key"}
	}
	var out orchdto.HostReworkSubmissionDTO
	if err := c.postGate(ctx, gatePath(project, taskID, "submit-rework"), req, &out); err != nil {
		return orchdto.HostReworkSubmissionDTO{}, err
	}
	return out, nil
}

func (c *Client) AbandonHostRework(ctx context.Context, project string, taskID uuid.UUID, req orchdto.AbandonHostReworkRequest) (orchdto.HostReworkSessionDTO, error) {
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Reason) == "" || strings.TrimSpace(req.IdempotencyKey) == "" || req.ObservedStateVersion == 0 {
		return orchdto.HostReworkSessionDTO{}, &ErrBadRequest{Message: "abandon rework requires session, actor, reason, observed state version, and idempotency key"}
	}
	var out orchdto.HostReworkSessionDTO
	if err := c.postGate(ctx, gatePath(project, taskID, "abandon-rework"), req, &out); err != nil {
		return orchdto.HostReworkSessionDTO{}, err
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

// Retry transitions a failed task back to backlog or retries a paused
// preliminary gate. Returns *ErrWrongStatus when neither route is current.
func (c *Client) Retry(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	return c.retry(ctx, project, taskID, "")
}

// RetryWithIdempotencyKey starts a new retry intention even when a prior
// identical request has a durable failed replay record.
func (c *Client) RetryWithIdempotencyKey(ctx context.Context, project string, taskID uuid.UUID, idempotencyKey string) (orchdto.TaskDTO, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "retry idempotency key is required and must be at most 200 characters"}
	}
	return c.retry(ctx, project, taskID, idempotencyKey)
}

func (c *Client) retry(ctx context.Context, project string, taskID uuid.UUID, idempotencyKey string) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "retry")
	if err := c.postGateWithIdempotencyKey(ctx, path, nil, &out, idempotencyKey); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// Resume restores a paused task to its recorded pre-pause pipeline status.
// It is deliberately distinct from Retry, which applies failure recovery
// semantics and may route a task through backlog or a preliminary gate.
func (c *Client) Resume(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskDTO, error) {
	var out orchdto.TaskDTO
	path := gatePath(project, taskID, "resume")
	if err := c.postGate(ctx, path, nil, &out); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// AdoptFailedChild asks the orchestrator to admit an exact host-repaired head
// for a failed child task and resume its parent without another inference run.
func (c *Client) AdoptFailedChild(ctx context.Context, project string, taskID uuid.UUID, commitSHA string) (orchdto.TaskDTO, error) {
	return c.AdoptFailedChildWithIdempotencyKey(ctx, project, taskID, commitSHA, "")
}

func (c *Client) AdoptFailedChildWithIdempotencyKey(ctx context.Context, project string, taskID uuid.UUID, commitSHA, idempotencyKey string) (orchdto.TaskDTO, error) {
	commitSHA = strings.TrimSpace(commitSHA)
	if commitSHA == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "adopt commit SHA is required"}
	}
	var out orchdto.TaskDTO
	if err := c.postGateWithIdempotencyKey(ctx, gatePath(project, taskID, "adopt"), orchdto.AdoptFailedChildRequest{CommitSHA: commitSHA}, &out, idempotencyKey); err != nil {
		return orchdto.TaskDTO{}, err
	}
	return out, nil
}

// ResumeFailedCheckpoint continues a partial worker checkpoint without
// declaring the child complete.
func (c *Client) ResumeFailedCheckpoint(ctx context.Context, project string, taskID uuid.UUID, commitSHA string) (orchdto.TaskDTO, error) {
	return c.ResumeFailedCheckpointWithIdempotencyKey(ctx, project, taskID, commitSHA, "")
}

func (c *Client) ResumeFailedCheckpointWithIdempotencyKey(ctx context.Context, project string, taskID uuid.UUID, commitSHA, idempotencyKey string) (orchdto.TaskDTO, error) {
	commitSHA = strings.TrimSpace(commitSHA)
	if commitSHA == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "checkpoint commit SHA is required"}
	}
	var out orchdto.TaskDTO
	if err := c.postGateWithIdempotencyKey(ctx, gatePath(project, taskID, "continue-checkpoint"), orchdto.ResumeFailedCheckpointRequest{CommitSHA: commitSHA}, &out, idempotencyKey); err != nil {
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

// CreateTaskSpec files a task backed by a typed reference observation. The
// server is authoritative for validation, idempotency, and active-task
// deduplication; this guard only prevents obviously incomplete local calls.
func (c *Client) CreateTaskSpec(ctx context.Context, project string, spec orchdto.TaskSpecDTO) (orchdto.TaskDTO, error) {
	if strings.TrimSpace(spec.Title) == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "task title is required"}
	}
	if strings.TrimSpace(spec.IdempotencyKey) == "" {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "task idempotency_key is required"}
	}
	if spec.Observation == nil {
		return orchdto.TaskDTO{}, &ErrBadRequest{Message: "task observation is required"}
	}
	var out orchdto.TaskDTO
	path := "/projects/" + url.PathEscape(project) + "/tasks"
	if err := c.postGate(ctx, path, spec, &out); err != nil {
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
	return c.postGateWithIdempotencyKey(ctx, path, body, out, "")
}

func (c *Client) postGateWithIdempotencyKey(ctx context.Context, path string, body any, out any, explicitKey string) error {
	var (
		rawBody     []byte
		contentType string
	)
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("orchclient: marshal request body: %w", err)
		}
		contentType = "application/json"
	}

	observed, idempotencyKey, guarded, err := c.legacyMutationMetadata(ctx, path, rawBody)
	if err != nil {
		return err
	}
	if explicitKey != "" {
		if !guarded {
			return &ErrBadRequest{Message: "explicit idempotency key requires a guarded task mutation"}
		}
		idempotencyKey = explicitKey
	}
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		var reader io.Reader
		if len(rawBody) != 0 {
			reader = bytes.NewReader(rawBody)
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path, nil), reader)
		if requestErr != nil {
			return requestErr
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
		if guarded {
			req.Header.Set("X-Drem-Observed-State-Version", fmt.Sprint(observed))
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		req.Header.Set("Accept", "application/json")

		resp, err = c.http.Do(req)
		if err == nil || !guarded || ctx.Err() != nil || attempt == 1 {
			break
		}
	}
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

// legacyMutationMetadata upgrades convenience APIs to the guarded wire
// contract. A deterministic key plus one same-request transport retry covers
// the common lost-response case without duplicating a write.
func (c *Client) legacyMutationMetadata(ctx context.Context, path string, body []byte) (uint64, string, bool, error) {
	if c.actor == "" {
		return 0, "", false, nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 4 && parts[0] == "projects" && parts[2] == "tasks" && parts[3] == "" {
		return 0, "create-" + uuid.NewString(), true, nil
	}
	if len(parts) == 3 && parts[0] == "projects" && parts[2] == "tasks" {
		return 0, "create-" + uuid.NewString(), true, nil
	}
	if len(parts) < 5 || parts[0] != "projects" || parts[2] != "tasks" {
		return 0, "", false, nil
	}
	verb := strings.Join(parts[4:], "/")
	guarded := map[string]bool{
		"approve": true, "reject": true, "revise-plan": true, "answer": true, "retry": true, "resume": true, "adopt": true, "continue-checkpoint": true,
		"archive": true, "comments": true, "audit-events": true,
	}
	if strings.HasPrefix(verb, "recover/") {
		var mode struct {
			Apply bool `json:"apply"`
		}
		guarded[verb] = json.Unmarshal(body, &mode) == nil && mode.Apply
	}
	if !guarded[verb] {
		return 0, "", false, nil
	}
	project, err := url.PathUnescape(parts[1])
	if err != nil {
		return 0, "", false, err
	}
	taskID, err := uuid.Parse(parts[3])
	if err != nil {
		return 0, "", false, nil
	}
	task, err := c.Task(ctx, project, taskID)
	if err != nil {
		return 0, "", false, err
	}
	if task.StateVersion == 0 {
		return 0, "", false, &ErrBadRequest{Message: "task response omitted state_version"}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{c.actor, path, fmt.Sprint(task.StateVersion), string(body)}, "\x00")))
	return task.StateVersion, fmt.Sprintf("legacy-%x", sum[:]), true, nil
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
