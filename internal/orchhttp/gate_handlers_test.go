package orchhttp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// fakeGateOrch is a fully-scripted stand-in for the orchestrator used by the
// gate mutation handlers. It records every method invocation (for assertion
// counting and arg inspection) and applies deterministic state transitions to
// the shared GORM database so the handler's re-fetch at the end returns the
// updated status. Tests that want an error path override the matching Err*
// field.
type fakeGateOrch struct {
	db *gorm.DB

	// Override any of these to force a specific error from the matching call.
	ErrPlanApproved       error
	ErrPlanRejected       error
	ErrTestReviewApproved error
	ErrTestReviewRejected error
	ErrTestPassed         error
	ErrTestFailed         error
	ErrClarification      error

	// Call log for assertion.
	Calls []fakeCall
}

type fakeCall struct {
	Method string
	TaskID uuid.UUID
	Body   string
}

func (f *fakeGateOrch) logCall(method string, id uuid.UUID, body string) {
	f.Calls = append(f.Calls, fakeCall{Method: method, TaskID: id, Body: body})
}

func (f *fakeGateOrch) HandlePlanApproved(taskID uuid.UUID) error {
	f.logCall("HandlePlanApproved", taskID, "")
	if f.ErrPlanApproved != nil {
		return f.ErrPlanApproved
	}
	return f.transition(taskID, model.StatusInProgress)
}

func (f *fakeGateOrch) HandlePlanRejected(taskID uuid.UUID) error {
	f.logCall("HandlePlanRejected", taskID, "")
	if f.ErrPlanRejected != nil {
		return f.ErrPlanRejected
	}
	return f.transition(taskID, model.StatusRejected)
}

func (f *fakeGateOrch) HandleTestReviewApproved(taskID uuid.UUID) error {
	f.logCall("HandleTestReviewApproved", taskID, "")
	if f.ErrTestReviewApproved != nil {
		return f.ErrTestReviewApproved
	}
	return f.transition(taskID, model.StatusInProgress)
}

func (f *fakeGateOrch) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	f.logCall("HandleTestReviewRejected", taskID, feedback)
	if f.ErrTestReviewRejected != nil {
		return f.ErrTestReviewRejected
	}
	return f.transition(taskID, model.StatusTestWriting)
}

func (f *fakeGateOrch) HandleTestPassed(taskID uuid.UUID) error {
	f.logCall("HandleTestPassed", taskID, "")
	if f.ErrTestPassed != nil {
		return f.ErrTestPassed
	}
	return f.transition(taskID, model.StatusMerging)
}

func (f *fakeGateOrch) HandleTestFailed(taskID uuid.UUID) error {
	f.logCall("HandleTestFailed", taskID, "")
	if f.ErrTestFailed != nil {
		return f.ErrTestFailed
	}
	return f.transition(taskID, model.StatusFailed)
}

func (f *fakeGateOrch) HandleClarificationAnswer(taskID uuid.UUID, answer string) error {
	f.logCall("HandleClarificationAnswer", taskID, answer)
	if f.ErrClarification != nil {
		return f.ErrClarification
	}
	return f.transition(taskID, model.StatusPlanning)
}

// transition mutates the task's Status directly on the underlying DB. Real
// orchestrator methods do additional work (events, worktrees, etc); the fake
// only needs the row to reflect the new status so the handler's re-fetch
// returns the updated DTO.
func (f *fakeGateOrch) transition(taskID uuid.UUID, to model.TaskStatus) error {
	return f.db.Model(&model.Task{}).Where("id = ?", taskID).Update("status", to).Error
}

// setupGateHTTPTest wires a Server with a fakeGateOrch and returns the DB,
// project, fake, and the httptest.Server base URL.
func setupGateHTTPTest(t *testing.T) (fake *fakeGateOrch, project model.Project, srv *orchhttp.Server, baseURL string) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t)
	project = testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	fake = &fakeGateOrch{db: db}
	srv = orchhttp.New(db, "secret-token", nil, orchhttp.ProjectInfo{
		Name:     projectName,
		Language: "go",
		OrchURL:  "http://localhost:8080",
	})
	srv.Orch = fake
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return fake, project, srv, ts.URL
}

func doJSON(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequest(method, url, nil)
	} else {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func decodeErr(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &e))
	return e.Error
}

func approveURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/approve", base, projectName, task)
}
func rejectURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/reject", base, projectName, task)
}
func passURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/pass", base, projectName, task)
}
func failURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/fail", base, projectName, task)
}
func answerURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/answer", base, projectName, task)
}

// ------------------------------------------------------------------
// 1. Approve happy path, plan_review → in_progress.
// ------------------------------------------------------------------
func TestApprovePlanReviewHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "plan me", model.StatusPlanReview)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, task.ID.String(), dto.ID)
	require.Equal(t, string(model.StatusInProgress), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandlePlanApproved", fake.Calls[0].Method)
	require.Equal(t, task.ID, fake.Calls[0].TaskID)
}

// ------------------------------------------------------------------
// 2. Approve happy path, test_review → in_progress.
// ------------------------------------------------------------------
func TestApproveTestReviewHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "test me", model.StatusTestReview)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusInProgress), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandleTestReviewApproved", fake.Calls[0].Method)
}

// ------------------------------------------------------------------
// 3. Approve wrong status → 409.
// ------------------------------------------------------------------
func TestApproveWrongStatusReturns409(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "wrong", model.StatusBacklog)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "backlog")
}

// ------------------------------------------------------------------
// 4. Approve unknown task → 404.
// ------------------------------------------------------------------
func TestApproveUnknownTaskReturns404(t *testing.T) {
	_, _, _, base := setupGateHTTPTest(t)
	resp, body := doJSON(t, http.MethodPost, approveURL(base, uuid.NewString()), "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "task not found", decodeErr(t, body))
}

// ------------------------------------------------------------------
// 5. Approve malformed UUID → 400.
// ------------------------------------------------------------------
func TestApproveMalformedUUIDReturns400(t *testing.T) {
	_, _, _, base := setupGateHTTPTest(t)
	resp, body := doJSON(t, http.MethodPost, approveURL(base, "not-a-uuid"), "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "invalid task id")
}

// ------------------------------------------------------------------
// 6. Reject plan_review (no reason) → 200.
// ------------------------------------------------------------------
func TestRejectPlanReviewHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "bad plan", model.StatusPlanReview)

	resp, body := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusRejected), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandlePlanRejected", fake.Calls[0].Method)
}

// ------------------------------------------------------------------
// 7. Reject test_review with reason → 200; reason forwarded.
// ------------------------------------------------------------------
func TestRejectTestReviewWithReason(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "bad tests", model.StatusTestReview)

	resp, _ := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), `{"reason":"tests lack edge cases"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandleTestReviewRejected", fake.Calls[0].Method)
	require.Equal(t, "tests lack edge cases", fake.Calls[0].Body)
}

// ------------------------------------------------------------------
// 8. Reject with missing body (reason optional) → 200, empty reason.
// ------------------------------------------------------------------
func TestRejectTestReviewMissingBody(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "bad tests", model.StatusTestReview)

	resp, _ := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandleTestReviewRejected", fake.Calls[0].Method)
	require.Equal(t, "", fake.Calls[0].Body)
}

// ------------------------------------------------------------------
// 9. Answer with body → 200.
// ------------------------------------------------------------------
func TestAnswerHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "q", model.StatusNeedsClarification)

	resp, body := doJSON(t, http.MethodPost, answerURL(base, task.ID.String()), `{"body":"my clarifying response"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandleClarificationAnswer", fake.Calls[0].Method)
	require.Equal(t, "my clarifying response", fake.Calls[0].Body)
}

// ------------------------------------------------------------------
// 10. Answer without body → 400.
// ------------------------------------------------------------------
func TestAnswerMissingBodyReturns400(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "q", model.StatusNeedsClarification)

	// No JSON body at all.
	resp, body := doJSON(t, http.MethodPost, answerURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "body is required")

	// Empty-string body in JSON.
	resp2, body2 := doJSON(t, http.MethodPost, answerURL(base, task.ID.String()), `{"body":""}`)
	require.Equal(t, http.StatusBadRequest, resp2.StatusCode)
	require.Contains(t, decodeErr(t, body2), "body is required")
}

// ------------------------------------------------------------------
// 11. Pass testing_ready → 200.
// ------------------------------------------------------------------
func TestPassHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "ready", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, passURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusMerging), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandleTestPassed", fake.Calls[0].Method)
}

// ------------------------------------------------------------------
// 12. Fail testing_ready → 200.
// ------------------------------------------------------------------
func TestFailHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "ready", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, failURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusFailed), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandleTestFailed", fake.Calls[0].Method)
}

// ------------------------------------------------------------------
// 13. Wrong project name → 404.
// ------------------------------------------------------------------
func TestWrongProjectNameReturns404(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusPlanReview)

	url := fmt.Sprintf("%s/projects/%s/tasks/%s/approve", base, "does-not-exist", task.ID.String())
	resp, body := doJSON(t, http.MethodPost, url, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "project")
}

// ------------------------------------------------------------------
// 14. Orchestrator error → 500.
// ------------------------------------------------------------------
func TestOrchestratorErrorReturns500(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	fake.ErrPlanApproved = errors.New("boom from orch")
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusPlanReview)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "boom from orch")
}

// ------------------------------------------------------------------
// 15. End-to-end: POST /approve then GET /tasks — read reflects write.
// ------------------------------------------------------------------
func TestApproveThenListTasksReadAfterWrite(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "e2e", model.StatusPlanReview)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// Now list tasks and confirm the status transitioned.
	r, err := http.Get(base + "/projects/" + projectName + "/tasks")
	require.NoError(t, err)
	defer r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	var out []orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(r.Body).Decode(&out))

	var found *orchdto.TaskDTO
	for i := range out {
		if out[i].ID == task.ID.String() {
			found = &out[i]
			break
		}
	}
	require.NotNil(t, found, "task should appear in list")
	require.Equal(t, string(model.StatusInProgress), found.Status,
		"read endpoint must see the write made via POST /approve")
}

// Extra: reject wrong status.
func TestRejectWrongStatusReturns409(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusBacklog)

	resp, body := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "backlog")
}

// Extra: pass wrong status.
func TestPassWrongStatusReturns409(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusBacklog)

	resp, _ := doJSON(t, http.MethodPost, passURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

// Extra: answer wrong status.
func TestAnswerWrongStatusReturns409(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusBacklog)

	resp, _ := doJSON(t, http.MethodPost, answerURL(base, task.ID.String()), `{"body":"anything"}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

// Extra: malformed JSON on reject/answer → 400.
func TestRejectMalformedJSONReturns400(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusTestReview)

	resp, body := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), `{not json`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "invalid")
}

// Extra: orch nil should degrade to 503 (safety: don't crash).
func TestGateEndpointNoOrchReturns503(t *testing.T) {
	db := testutil.NewTestDBWithModels(t)
	project := testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "x", model.StatusPlanReview)

	srv := orchhttp.New(db, "secret-token", nil, orchhttp.ProjectInfo{
		Name:     projectName,
		Language: "go",
		OrchURL:  "http://localhost:8080",
	})
	// Deliberately do NOT set srv.Orch.
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	resp, _ := doJSON(t, http.MethodPost, approveURL(ts.URL, task.ID.String()), "")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
