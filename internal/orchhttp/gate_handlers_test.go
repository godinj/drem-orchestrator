package orchhttp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/state"
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
	ErrRevisePlan         error
	ErrRetryTask          error
	ErrResumeTask         error
	ErrAdoptFailedChild   error
	ErrResumeCheckpoint   error
	ErrVerifyDelivery     error
	ErrIntegrateDelivery  error
	LastVerify            *orchestrator.VerifyDeliveryRequest
	LastRework            *orchestrator.RequestDeliveryReworkRequest

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

func (f *fakeGateOrch) HandlePlanApprovedBy(taskID uuid.UUID, actor string) error {
	return f.HandlePlanApproved(taskID)
}

func (f *fakeGateOrch) HandlePlanRejected(taskID uuid.UUID) error {
	f.logCall("HandlePlanRejected", taskID, "")
	if f.ErrPlanRejected != nil {
		return f.ErrPlanRejected
	}
	return f.transition(taskID, model.StatusRejected)
}

func (f *fakeGateOrch) HandlePlanRejectedBy(taskID uuid.UUID, actor string, feedback ...string) error {
	body := ""
	if len(feedback) > 0 {
		body = feedback[0]
	}
	f.logCall("HandlePlanRejected", taskID, body)
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

func (f *fakeGateOrch) HandleTestReviewApprovedBy(taskID uuid.UUID, actor string) error {
	return f.HandleTestReviewApproved(taskID)
}

func (f *fakeGateOrch) HandleTestReviewRejected(taskID uuid.UUID, feedback string) error {
	f.logCall("HandleTestReviewRejected", taskID, feedback)
	if f.ErrTestReviewRejected != nil {
		return f.ErrTestReviewRejected
	}
	return f.transition(taskID, model.StatusTestWriting)
}

func (f *fakeGateOrch) HandleTestReviewRejectedBy(taskID uuid.UUID, feedback, actor string) error {
	return f.HandleTestReviewRejected(taskID, feedback)
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
	return f.transition(taskID, model.StatusInProgress)
}

func (f *fakeGateOrch) HandleClarificationAnswer(taskID uuid.UUID, answer string) error {
	f.logCall("HandleClarificationAnswer", taskID, answer)
	if f.ErrClarification != nil {
		return f.ErrClarification
	}
	return f.transition(taskID, model.StatusPlanning)
}

func (f *fakeGateOrch) HandleClarificationAnswerBy(taskID uuid.UUID, answer, actor string) error {
	return f.HandleClarificationAnswer(taskID, answer)
}

func (f *fakeGateOrch) RevisePlan(taskID uuid.UUID, observedVersion uint64, plan model.JSONField, actor, reason string) error {
	f.logCall("RevisePlan", taskID, reason)
	if f.ErrRevisePlan != nil {
		return f.ErrRevisePlan
	}
	return f.db.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", taskID, model.StatusPlanReview, observedVersion).
		Updates(map[string]any{
			"plan": plan, "state_version": observedVersion + 1, "assigned_agent_id": nil,
		}).Error
}

func (f *fakeGateOrch) RetryTask(taskID uuid.UUID) error {
	f.logCall("RetryTask", taskID, "")
	if f.ErrRetryTask != nil {
		return f.ErrRetryTask
	}
	var task model.Task
	if err := f.db.First(&task, "id = ?", taskID).Error; err != nil {
		return err
	}
	if task.Status == model.StatusPaused {
		return f.transition(taskID, model.StatusTestingReady)
	}
	return f.transition(taskID, model.StatusBacklog)
}

func (f *fakeGateOrch) ResumeTask(taskID uuid.UUID) error {
	f.logCall("ResumeTask", taskID, "")
	if f.ErrResumeTask != nil {
		return f.ErrResumeTask
	}
	return f.transition(taskID, model.StatusTestWriting)
}

func (f *fakeGateOrch) AdoptFailedChild(taskID uuid.UUID, commitSHA, actor string) error {
	f.logCall("AdoptFailedChild", taskID, commitSHA+"|"+actor)
	if f.ErrAdoptFailedChild != nil {
		return f.ErrAdoptFailedChild
	}
	return f.transition(taskID, model.StatusDone)
}

func (f *fakeGateOrch) ResumeFailedCheckpoint(taskID uuid.UUID, commitSHA, actor string) error {
	f.logCall("ResumeFailedCheckpoint", taskID, commitSHA+"|"+actor)
	if f.ErrResumeCheckpoint != nil {
		return f.ErrResumeCheckpoint
	}
	return f.transition(taskID, model.StatusInProgress)
}

func (f *fakeGateOrch) VerifyDelivery(req orchestrator.VerifyDeliveryRequest) (*model.VerificationRecord, error) {
	f.LastVerify = &req
	f.logCall("VerifyDelivery", req.TaskID, req.Actor)
	if f.ErrVerifyDelivery != nil {
		return nil, f.ErrVerifyDelivery
	}
	target := model.StatusIntegrationReady
	if req.Result == model.VerificationFailed {
		target = model.StatusInProgress
	}
	if err := f.transition(req.TaskID, target); err != nil {
		return nil, err
	}
	return &model.VerificationRecord{
		ID: uuid.New(), TaskID: req.TaskID, DeliveryArtifactID: uuid.New(),
		ArtifactVersion: req.ArtifactVersion, CommitSHA: req.CommitSHA,
		VerifierActor: req.Actor, EnvironmentFingerprint: req.EnvironmentFingerprint,
		Result: req.Result, Notes: req.Notes, CreatedAt: time.Now(),
	}, nil
}

func (f *fakeGateOrch) AuthorizeIntegration(req orchestrator.IntegrateDeliveryRequest) (*model.IntegrationAuthorization, error) {
	f.logCall("AuthorizeIntegration", req.TaskID, req.Actor)
	if f.ErrIntegrateDelivery != nil {
		return nil, f.ErrIntegrateDelivery
	}
	if err := f.transition(req.TaskID, model.StatusMerging); err != nil {
		return nil, err
	}
	return &model.IntegrationAuthorization{
		ID: uuid.New(), TaskID: req.TaskID, ArtifactVersion: req.ArtifactVersion,
		CommitSHA: req.CommitSHA, VerificationRecordID: req.VerificationRecordID,
	}, nil
}

func (f *fakeGateOrch) RequestDeliveryRework(req orchestrator.RequestDeliveryReworkRequest) (*model.DeliveryReworkRecord, error) {
	f.LastRework = &req
	f.logCall("RequestDeliveryRework", req.TaskID, req.Reason)
	if err := f.transition(req.TaskID, model.StatusInProgress); err != nil {
		return nil, err
	}
	return &model.DeliveryReworkRecord{
		ID: uuid.New(), TaskID: req.TaskID, ArtifactVersion: req.ArtifactVersion,
		CommitSHA: req.CommitSHA, Actor: req.Actor, Reason: req.Reason, Mode: req.Mode,
	}, nil
}

func (f *fakeGateOrch) SubmitHostRework(req orchestrator.SubmitHostReworkRequest) (*model.HostReworkSubmission, error) {
	f.logCall("SubmitHostRework", req.TaskID, req.Actor)
	if err := f.transition(req.TaskID, model.StatusTestingReady); err != nil {
		return nil, err
	}
	return &model.HostReworkSubmission{
		ID: uuid.New(), SessionID: req.SessionID, TaskID: req.TaskID,
		PriorCommitSHA: strings.Repeat("a", 40), ReplacementCommitSHA: req.CommitSHA,
		Actor: req.Actor, IdempotencyKey: req.IdempotencyKey, ChangedPaths: model.JSONArray{"src/ui.cpp"},
	}, nil
}

func (f *fakeGateOrch) AbandonHostRework(req orchestrator.AbandonHostReworkRequest) (*model.HostReworkSession, error) {
	f.logCall("AbandonHostRework", req.TaskID, req.Actor)
	if err := f.transition(req.TaskID, model.StatusInProgress); err != nil {
		return nil, err
	}
	now := time.Now()
	return &model.HostReworkSession{
		ID: req.SessionID, TaskID: req.TaskID, OwnerActor: req.Actor,
		Disposition: model.HostReworkOrchestrated, PriorCommitSHA: strings.Repeat("a", 40), FinishedAt: &now,
	}, nil
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
	r.Header.Set("Authorization", "Bearer secret-token")
	actor := "codex:test-thread"
	var identity struct {
		Actor string `json:"actor"`
	}
	actorBodyRoute := strings.Contains(url, "/archive") || strings.Contains(url, "/audit-events") || strings.Contains(url, "/recover/")
	if actorBodyRoute && json.Unmarshal([]byte(body), &identity) == nil && strings.TrimSpace(identity.Actor) != "" {
		actor = strings.TrimSpace(identity.Actor)
	}
	r.Header.Set("X-Drem-Actor", actor)
	if method == http.MethodPost {
		observed := uint64(1)
		var envelope struct {
			Observed uint64 `json:"observed_state_version"`
		}
		if json.Unmarshal([]byte(body), &envelope) == nil && envelope.Observed != 0 {
			observed = envelope.Observed
		} else if taskURL := taskResourceURL(url); taskURL != "" {
			getReq, getErr := http.NewRequest(http.MethodGet, taskURL, nil)
			if getErr == nil {
				if getResp, getErr := http.DefaultClient.Do(getReq); getErr == nil {
					var task orchdto.TaskDTO
					if json.NewDecoder(getResp.Body).Decode(&task) == nil && task.StateVersion != 0 {
						observed = task.StateVersion
					}
					_ = getResp.Body.Close()
				}
			}
		}
		r.Header.Set("X-Drem-Observed-State-Version", fmt.Sprint(observed))
		r.Header.Set("Idempotency-Key", uuid.NewString())
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func taskResourceURL(rawURL string) string {
	marker := "/tasks/"
	idx := strings.Index(rawURL, marker)
	if idx < 0 {
		return ""
	}
	rest := rawURL[idx+len(marker):]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	if rest == "" {
		return ""
	}
	return rawURL[:idx+len(marker)] + rest
}

func doGuardedJSON(t *testing.T, method, rawURL, body, actor, key string, observed uint64) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Drem-Actor", actor)
	req.Header.Set("X-Drem-Observed-State-Version", fmt.Sprint(observed))
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, raw
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
func retryURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/retry", base, projectName, task)
}
func resumeURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/resume", base, projectName, task)
}
func adoptURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/adopt", base, projectName, task)
}

func continueCheckpointURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/continue-checkpoint", base, projectName, task)
}
func archiveURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/archive", base, projectName, task)
}
func commentURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/comments", base, projectName, task)
}
func auditURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/audit-events", base, projectName, task)
}
func artifactURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/artifact", base, projectName, task)
}
func verifyURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/verify", base, projectName, task)
}
func integrateURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/integrate", base, projectName, task)
}
func reworkURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/request-rework", base, projectName, task)
}
func submitReworkURL(base, task string) string {
	return fmt.Sprintf("%s/projects/%s/tasks/%s/submit-rework", base, projectName, task)
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

func TestMutationRequiresProjectBearerToken(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "protected", model.StatusPlanReview)
	req, err := http.NewRequest(http.MethodPost, approveURL(base, task.ID.String()), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Empty(t, fake.Calls)
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

func TestApproveTestingReadyFailsClosed(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "verify me", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "testing_ready")
	require.Empty(t, fake.Calls)
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
// 6. Reject plan_review with planner feedback → 200.
// ------------------------------------------------------------------
func TestRejectPlanReviewHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "bad plan", model.StatusPlanReview)

	resp, body := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), `{"reason":"use existing test seams"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusRejected), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "HandlePlanRejected", fake.Calls[0].Method)
	require.Equal(t, "use existing test seams", fake.Calls[0].Body)
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

func TestRejectTestingReadyFailsClosed(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "needs rework", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, rejectURL(base, task.ID.String()), `{"reason":"native verification failed"}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "request-rework")
	require.Empty(t, fake.Calls)
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

func TestAnswerRequiresAttributedActor(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "q", model.StatusNeedsClarification)
	req, err := http.NewRequest(http.MethodPost, answerURL(base, task.ID.String()), strings.NewReader(`{"body":"answer"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
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
// 11. Legacy pass fails closed because it carries no artifact evidence.
// ------------------------------------------------------------------
func TestPassHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "ready", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, passURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "deprecated")
	require.Empty(t, fake.Calls)
}

// ------------------------------------------------------------------
// 12. Legacy fail also fails closed.
// ------------------------------------------------------------------
func TestFailHappy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "ready", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, failURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "deprecated")
	require.Empty(t, fake.Calls)
}

func createDeliveryArtifact(t *testing.T, srv *orchhttp.Server, project model.Project, status model.TaskStatus) (model.Task, model.DeliveryArtifact) {
	t.Helper()
	task := testutil.CreateTask(t, srv.DB, project.ID, "native verification", status)
	artifact := model.DeliveryArtifact{
		ID: uuid.New(), TaskID: task.ID, ArtifactVersion: 1,
		Branch: "feature/native", CommitSHA: strings.Repeat("a", 40),
		BaseBranch: "main", BaseSHA: strings.Repeat("b", 40),
		PreliminaryEvidence: model.JSONField{"commands": []any{map[string]any{"command": "go test ./...", "passed": true}}},
		CreatorActor:        "orchestrator", CreatorSource: "testing_ready",
	}
	require.NoError(t, srv.DB.Create(&artifact).Error)
	require.NoError(t, srv.DB.First(&task, "id = ?", task.ID).Error)
	return task, artifact
}

func TestDeliveryArtifactReadContract(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusVerificationReady)
	resp, body := doJSON(t, http.MethodGet, artifactURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var envelope orchdto.DeliveryEnvelopeDTO
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, task.StateVersion, envelope.Task.StateVersion)
	require.Equal(t, artifact.CommitSHA, envelope.Artifact.CommitSHA)
	require.Equal(t, artifact.BaseSHA, envelope.Artifact.BaseSHA)
}

func TestVerifyDeliveryRoutesExactObservedEvidence(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusVerificationReady)
	body := fmt.Sprintf(`{"observed_state_version":%d,"artifact_version":1,"commit_sha":%q,"actor":"codex:test-thread","environment_fingerprint":"macos-arm64","commands":[{"command":"scripts/dev verify","passed":true,"exit_code":0,"started_at":"2026-07-22T00:00:00Z","finished_at":"2026-07-22T00:01:00Z"}],"result":"pass","idempotency_key":"verify-1"}`,
		task.StateVersion, artifact.CommitSHA)
	resp, responseBody := doJSON(t, http.MethodPost, verifyURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(responseBody))
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "VerifyDelivery", fake.Calls[0].Method)
	require.Equal(t, "codex:test-thread", fake.Calls[0].Body)
}

func TestVerifyDeliveryRoutesComputerUseEvidenceAndHostDirectDecision(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusVerificationReady)
	body := fmt.Sprintf(`{"observed_state_version":%d,"artifact_version":1,"commit_sha":%q,"actor":"codex:test-thread","environment_fingerprint":"macos-arm64","commands":[{"command":"scripts/dev build","passed":false,"exit_code":1,"started_at":"2026-07-22T00:00:00Z","finished_at":"2026-07-22T00:01:00Z"}],"binary_sha256":%q,"result":"fail","failure_mode":"host_direct","failure_reason":"bounded mismatch","allowed_scope":["src/ui"],"host_direct_attestation":{"acceptance_criteria_unchanged":true,"dependency_shape_unchanged":true,"no_persistence_or_schema":true,"no_security_or_auth":true,"no_cross_process_ownership":true,"no_build_or_release_policy":true},"interactions":[{"acceptance_criterion_id":"criterion-1","scenario_name":"drag","steps":[{"action":"drag","observed":"wrong range"}],"observed_result":"wrong range","evidence_refs":[{"artifact_id":"capture-1","sha256":%q,"media_type":"image/png"}],"application_version":"0.1","host_environment":"macos-arm64","run_pid":42,"result":"fail","discrepancy":"wrong range"}],"idempotency_key":"verify-cu-1"}`,
		task.StateVersion, artifact.CommitSHA, strings.Repeat("b", 64), strings.Repeat("c", 64))
	resp, responseBody := doJSON(t, http.MethodPost, verifyURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(responseBody))
	require.NotNil(t, fake.LastVerify)
	require.Len(t, fake.LastVerify.Interactions, 1)
	require.Equal(t, model.DeliveryReworkHostDirect, fake.LastVerify.FailureMode)
	require.Equal(t, []string{"src/ui"}, fake.LastVerify.AllowedScope)
	require.True(t, fake.LastVerify.HostDirectAttestation.NoSecurityOrAuth)
}

func TestVerifyDeliveryStaleArtifactReturnsConflict(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusVerificationReady)
	fake.ErrVerifyDelivery = fmt.Errorf("%w: changed", orchestrator.ErrStaleArtifact)
	body := fmt.Sprintf(`{"observed_state_version":%d,"artifact_version":1,"commit_sha":%q,"actor":"codex:test-thread","environment_fingerprint":"macos-arm64","commands":[{"command":"verify","passed":true}],"result":"pass","idempotency_key":"verify-stale"}`,
		task.StateVersion, artifact.CommitSHA)
	resp, responseBody := doJSON(t, http.MethodPost, verifyURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(responseBody))
}

func TestVerifyDeliveryRejectsActorSpoofing(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusVerificationReady)
	body := fmt.Sprintf(`{"observed_state_version":%d,"artifact_version":1,"commit_sha":%q,"actor":"different-actor","environment_fingerprint":"macos-arm64","commands":[{"command":"verify","passed":true}],"result":"pass","idempotency_key":"verify-spoof"}`,
		task.StateVersion, artifact.CommitSHA)
	resp, responseBody := doJSON(t, http.MethodPost, verifyURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(responseBody))
	require.Empty(t, fake.Calls)
}

func TestIntegrateDeliveryRoutesAcceptedVerification(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusIntegrationReady)
	verificationID := uuid.New()
	body := fmt.Sprintf(`{"observed_state_version":%d,"artifact_version":1,"commit_sha":%q,"verification_record_id":%q,"actor":"codex:test-thread","idempotency_key":"integrate-1"}`,
		task.StateVersion, artifact.CommitSHA, verificationID.String())
	resp, responseBody := doJSON(t, http.MethodPost, integrateURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(responseBody))
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "AuthorizeIntegration", fake.Calls[0].Method)
}

func TestRequestDeliveryReworkRoutesExactArtifactAndReason(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task, artifact := createDeliveryArtifact(t, srv, project, model.StatusIntegrationReady)
	body := fmt.Sprintf(`{"observed_state_version":%d,"artifact_version":1,"commit_sha":%q,"actor":"codex:test-thread","reason":"native regression","mode":"orchestrated","idempotency_key":"rework-1"}`,
		task.StateVersion, artifact.CommitSHA)
	resp, responseBody := doJSON(t, http.MethodPost, reworkURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(responseBody))
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RequestDeliveryRework", fake.Calls[0].Method)
	require.Equal(t, "native regression", fake.Calls[0].Body)
	require.Equal(t, model.DeliveryReworkOrchestrated, fake.LastRework.Mode)
}

func TestSubmitHostReworkRoutesActorOwnedExactSHA(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "host correction", model.StatusHostRework)
	sessionID := uuid.New()
	commitSHA := strings.Repeat("d", 40)
	body := fmt.Sprintf(`{"observed_state_version":%d,"session_id":%q,"commit_sha":%q,"actor":"codex:test-thread","idempotency_key":"submit-1"}`,
		task.StateVersion, sessionID.String(), commitSHA)
	resp, responseBody := doJSON(t, http.MethodPost, submitReworkURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(responseBody))
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "SubmitHostRework", fake.Calls[0].Method)
	var dto orchdto.HostReworkSubmissionDTO
	require.NoError(t, json.Unmarshal(responseBody, &dto))
	require.Equal(t, commitSHA, dto.ReplacementCommitSHA)
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

func TestApproveStaleTransitionReturns409(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	fake.ErrPlanApproved = fmt.Errorf("%w: already approved", state.ErrStaleTransition)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusPlanReview)

	resp, body := doJSON(t, http.MethodPost, approveURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "already approved")
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

// ------------------------------------------------------------------
// Retry — failed → backlog.
// ------------------------------------------------------------------
func TestServer_RetryTaskEndpoint_Happy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "stuck", model.StatusFailed)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, task.ID.String(), dto.ID)
	require.Equal(t, string(model.StatusBacklog), dto.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RetryTask", fake.Calls[0].Method)
	require.Equal(t, task.ID, fake.Calls[0].TaskID)
}

func TestServer_ResumeTaskEndpoint_Happy(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "diagnostic pause", model.StatusPaused)

	resp, body := doJSON(t, http.MethodPost, resumeURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusTestWriting), dto.Status)
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "ResumeTask", fake.Calls[0].Method)
}

func TestServer_ResumeTaskEndpoint_WrongStatus(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "running", model.StatusInProgress)

	resp, body := doJSON(t, http.MethodPost, resumeURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "paused")
}

func TestServer_ResumeTaskEndpoint_OrchError(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	fake.ErrResumeTask = errors.New("resume blew up")
	task := testutil.CreateTask(t, srv.DB, project.ID, "diagnostic pause", model.StatusPaused)

	resp, body := doJSON(t, http.MethodPost, resumeURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "resume blew up")
}

func TestServer_AdoptFailedChildEndpoint(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	parent := testutil.CreateTask(t, srv.DB, project.ID, "parent", model.StatusFailed)
	child := testutil.CreateTask(t, srv.DB, project.ID, "child", model.StatusFailed)
	child.ParentTaskID = &parent.ID
	require.NoError(t, srv.DB.Save(child).Error)
	commit := strings.Repeat("a", 40)

	resp, body := doJSON(t, http.MethodPost, adoptURL(base, child.ID.String()), `{"commit_sha":"`+commit+`"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusDone), dto.Status)
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "AdoptFailedChild", fake.Calls[0].Method)
	require.Contains(t, fake.Calls[0].Body, commit)
}

func TestServer_AdoptFailedChildMapsAdmissionConflict(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	parent := testutil.CreateTask(t, srv.DB, project.ID, "parent", model.StatusFailed)
	child := testutil.CreateTask(t, srv.DB, project.ID, "child", model.StatusFailed)
	child.ParentTaskID = &parent.ID
	require.NoError(t, srv.DB.Save(child).Error)
	fake.ErrAdoptFailedChild = fmt.Errorf("%w: stale head", orchestrator.ErrCodexAdoptionConflict)

	resp, body := doJSON(t, http.MethodPost, adoptURL(base, child.ID.String()), `{"commit_sha":"deadbeef"}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "stale head")
}

func TestServer_ResumeFailedCheckpointEndpoint(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	parent := testutil.CreateTask(t, srv.DB, project.ID, "parent", model.StatusFailed)
	child := testutil.CreateTask(t, srv.DB, project.ID, "child", model.StatusFailed)
	child.ParentTaskID = &parent.ID
	require.NoError(t, srv.DB.Save(child).Error)
	commit := strings.Repeat("b", 40)

	resp, body := doJSON(t, http.MethodPost, continueCheckpointURL(base, child.ID.String()), `{"commit_sha":"`+commit+`"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusInProgress), dto.Status)
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "ResumeFailedCheckpoint", fake.Calls[0].Method)
	require.Contains(t, fake.Calls[0].Body, commit)
}

func TestServer_ResumeFailedCheckpointMapsConflict(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	parent := testutil.CreateTask(t, srv.DB, project.ID, "parent", model.StatusFailed)
	child := testutil.CreateTask(t, srv.DB, project.ID, "child", model.StatusFailed)
	child.ParentTaskID = &parent.ID
	require.NoError(t, srv.DB.Save(child).Error)
	fake.ErrResumeCheckpoint = fmt.Errorf("%w: stale head", orchestrator.ErrCheckpointResumeConflict)

	resp, body := doJSON(t, http.MethodPost, continueCheckpointURL(base, child.ID.String()), `{"commit_sha":"deadbeef"}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "stale head")
}

func TestServer_RetryTaskEndpoint_PausedGate(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "runner unavailable", model.StatusPaused)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusTestingReady), dto.Status)
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RetryTask", fake.Calls[0].Method)
}

func TestServer_RetryTaskEndpoint_PausedWithoutGateReturnsConflict(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "operator paused", model.StatusPaused)
	fake.ErrRetryTask = orchestrator.ErrRetryPausedGateMissing

	resp, body := doJSON(t, http.MethodPost, retryURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "no retryable preliminary gate")
}

func TestServer_RetryTaskEndpoint_UnknownTask(t *testing.T) {
	_, _, _, base := setupGateHTTPTest(t)
	resp, body := doJSON(t, http.MethodPost, retryURL(base, uuid.NewString()), "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "task not found", decodeErr(t, body))
}

func TestServer_RetryTaskEndpoint_WrongStatus(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "running", model.StatusInProgress)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "in_progress")
	require.Contains(t, decodeErr(t, body), "failed")
}

func TestServer_RetryTaskEndpoint_MalformedUUID(t *testing.T) {
	_, _, _, base := setupGateHTTPTest(t)
	resp, body := doJSON(t, http.MethodPost, retryURL(base, "not-a-uuid"), "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "invalid task id")
}

func TestServer_RetryTaskEndpoint_OrchError(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	fake.ErrRetryTask = errors.New("retry blew up")
	task := testutil.CreateTask(t, srv.DB, project.ID, "broken", model.StatusFailed)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "retry blew up")
}

func TestServer_RetryTaskEndpoint_ParentWithChildrenReturnsConflict(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	fake.ErrRetryTask = orchestrator.ErrRetryParentHasChildren
	task := testutil.CreateTask(t, srv.DB, project.ID, "parent", model.StatusFailed)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, task.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), orchestrator.ErrRetryParentHasChildren.Error())
}

func TestServer_RetryTaskEndpoint_NoOrchReturns503(t *testing.T) {
	db := testutil.NewTestDBWithModels(t)
	project := testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "x", model.StatusFailed)

	srv := orchhttp.New(db, "secret-token", nil, orchhttp.ProjectInfo{
		Name:     projectName,
		Language: "go",
		OrchURL:  "http://localhost:8080",
	})
	// Deliberately do NOT set srv.Orch.
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	resp, _ := doJSON(t, http.MethodPost, retryURL(ts.URL, task.ID.String()), "")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestRetrySubtask_FailedParentReturnsConflict(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	fake.ErrRetryTask = orchestrator.ErrRetryParentHasChildren

	parent := testutil.CreateTask(t, srv.DB, project.ID, "parent", model.StatusFailed)
	child := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		Title:        "child",
		Description:  "child",
		Status:       model.StatusFailed,
		Category:     model.CategoryStandard,
		ParentTaskID: &parent.ID,
	}
	require.NoError(t, srv.DB.Create(&child).Error)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, child.ID.String()), "")
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), orchestrator.ErrRetryParentHasChildren.Error())

	var reloadedParent, reloadedChild model.Task
	require.NoError(t, srv.DB.First(&reloadedParent, "id = ?", parent.ID).Error)
	require.NoError(t, srv.DB.First(&reloadedChild, "id = ?", child.ID).Error)
	require.Equal(t, model.StatusFailed, reloadedParent.Status)
	require.Equal(t, model.StatusFailed, reloadedChild.Status)

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RetryTask", fake.Calls[0].Method)
	require.Equal(t, parent.ID, fake.Calls[0].TaskID)
}

// TestRetrySubtask_DoneParentLeftAlone proves the cascade is scoped to
// FAILED parents only. A subtask under a DONE parent still retries as a
// single call — we do not re-animate completed work.
func TestRetrySubtask_DoneParentLeftAlone(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)

	parent := testutil.CreateTask(t, srv.DB, project.ID, "done-parent", model.StatusDone)
	child := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		Title:        "child",
		Description:  "child",
		Status:       model.StatusFailed,
		Category:     model.CategoryStandard,
		ParentTaskID: &parent.ID,
	}
	require.NoError(t, srv.DB.Create(&child).Error)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, child.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// Parent untouched.
	var reloadedParent model.Task
	require.NoError(t, srv.DB.First(&reloadedParent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusDone, reloadedParent.Status,
		"DONE parent must not be re-animated by a child retry")

	// Only the child's retry was called.
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RetryTask", fake.Calls[0].Method)
	require.Equal(t, child.ID, fake.Calls[0].TaskID)
}

// TestRetrySubtask_MissingParentFallsThrough guards against a dangling
// ParentTaskID (e.g. parent row deleted out-of-band). The handler must
// still retry the child rather than returning 500 or 404.
func TestRetrySubtask_MissingParentFallsThrough(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)

	ghostParent := uuid.New()
	child := model.Task{
		ID:           uuid.New(),
		ProjectID:    project.ID,
		Title:        "orphan",
		Description:  "orphan",
		Status:       model.StatusFailed,
		Category:     model.CategoryStandard,
		ParentTaskID: &ghostParent,
	}
	require.NoError(t, srv.DB.Create(&child).Error)

	resp, body := doJSON(t, http.MethodPost, retryURL(base, child.ID.String()), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RetryTask", fake.Calls[0].Method)
	require.Equal(t, child.ID, fake.Calls[0].TaskID)
}

func TestCommentTaskEndpoint_Happy(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "needs context", model.StatusMerging)

	resp, body := doJSON(t, http.MethodPost, commentURL(base, task.ID.String()), `{"body":"supersede candidate from current base"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskCommentDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, task.ID.String(), dto.TaskID)
	require.Equal(t, "codex:test-thread", dto.Author)
	require.Equal(t, "supersede candidate from current base", dto.Body)

	var comments []model.TaskComment
	require.NoError(t, srv.DB.Where("task_id = ?", task.ID).Find(&comments).Error)
	require.Len(t, comments, 1)
	require.Equal(t, dto.ID, comments[0].ID.String())
	var updated model.Task
	require.NoError(t, srv.DB.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, task.StateVersion+1, updated.StateVersion)
}

func TestCommentTaskEndpoint_EmptyBodyReturns400(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "x", model.StatusBacklog)

	resp, body := doJSON(t, http.MethodPost, commentURL(base, task.ID.String()), `{"body":"   "}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "body is required")
}

func TestCommentTaskEndpoint_UnknownTaskReturns404(t *testing.T) {
	_, _, _, base := setupGateHTTPTest(t)
	resp, body := doJSON(t, http.MethodPost, commentURL(base, uuid.NewString()), `{"body":"x"}`)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "task not found", decodeErr(t, body))
}

func TestRecoveryAuditEndpoint_RecordsStructuredKyleEvent(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "blocked", model.StatusTestingReady)
	body := `{"actor":"kyle","policy_rule":"testing_ready.infra_tooling.one_retry_max","evidence":"tooling timeout","surface":"POST /fail","action":"retry testing_ready via fail endpoint","result":"task transitioned to in_progress","next_follow_up":"escalate if blocker repeats","supported_path":true}`

	resp, raw := doJSON(t, http.MethodPost, auditURL(base, task.ID.String()), body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	var dto orchdto.EventDTO
	require.NoError(t, json.Unmarshal(raw, &dto))
	require.Equal(t, "kyle_recovery_audit", dto.Type)

	var event model.TaskEvent
	require.NoError(t, srv.DB.Where("task_id = ? AND event_type = ?", task.ID, "kyle_recovery_audit").First(&event).Error)
	require.Equal(t, "kyle", event.Actor)
	require.Equal(t, "retry testing_ready via fail endpoint", event.NewValue)
	require.Equal(t, "testing_ready.infra_tooling.one_retry_max", event.Details["policy_rule"])
	require.Equal(t, "tooling timeout", event.Details["observed_evidence"])
	require.Equal(t, true, event.Details["supported_path"])
}

func TestRecoveryAuditEndpoint_RequiresCoreFields(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "blocked", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, auditURL(base, task.ID.String()), `{"actor":"kyle"}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "actor, policy_rule, action, and result are required")
}

func TestArchiveTaskEndpoint_CancelsFailedTaskAndAudits(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "obsolete", model.StatusFailed)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"actor":"kyle","reason":"superseded","mode":"obsolete"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, task.ID.String(), dto.ID)
	require.Equal(t, string(model.StatusCancelled), dto.Status)
	require.Equal(t, task.StateVersion+1, dto.StateVersion)

	var events []model.TaskEvent
	require.NoError(t, srv.DB.Where("task_id = ? AND event_type = ?", task.ID, "task_archived").Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, "kyle", events[0].Actor)
	require.Equal(t, string(model.StatusFailed), events[0].OldValue)
	require.Equal(t, string(model.StatusCancelled), events[0].NewValue)
	require.Equal(t, "superseded", events[0].Details["reason"])
	var transition model.TaskEvent
	require.NoError(t, srv.DB.Where("task_id = ? AND event_type = ? AND new_value = ?",
		task.ID, "status_change", model.StatusCancelled).First(&transition).Error)
	evidence, _ := transition.Details["evidence"].(map[string]any)
	require.Equal(t, "archive_api", evidence["source"])
}

func TestArchiveTaskEndpoint_CancelsUnassignedTestingReadyTask(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "obsolete delivery gate", model.StatusTestingReady)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"actor":"codex:canary","reason":"canary complete","mode":"obsolete"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusCancelled), dto.Status)
}

func TestArchiveTaskEndpoint_RejectsLiveAssignedWork(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "assigned", model.StatusFailed)
	ag := testutil.CreateAgent(t, srv.DB, task.ID, model.AgentCoder, model.AgentWorking)
	require.NoError(t, srv.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("assigned_agent_id", ag.ID).Error)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"reason":"obsolete"}`)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "live worker")

	var reloaded model.Task
	require.NoError(t, srv.DB.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
}

func TestArchiveTaskEndpoint_AllowsMissingAssignedAgent(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "stale missing", model.StatusRejected)
	missingAgentID := uuid.New()
	require.NoError(t, srv.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("assigned_agent_id", missingAgentID).Error)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"reason":"obsolete"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var reloaded model.Task
	require.NoError(t, srv.DB.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusCancelled, reloaded.Status)
	require.Nil(t, reloaded.AssignedAgentID)

	var event model.TaskEvent
	require.NoError(t, srv.DB.Where("task_id = ? AND event_type = ?", task.ID, "task_archived").First(&event).Error)
	require.Equal(t, missingAgentID.String(), event.Details["previous_worker_id"])
}

func TestArchiveTaskEndpoint_AllowsIdleAssignedAgent(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "stale idle", model.StatusFailed)
	ag := testutil.CreateAgent(t, srv.DB, task.ID, model.AgentCoder, model.AgentIdle)
	require.NoError(t, srv.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("assigned_agent_id", ag.ID).Error)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"reason":"obsolete"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusCancelled), dto.Status)
	require.Empty(t, dto.AssignedWorker)
}

func TestArchiveTaskEndpoint_InvalidatesParkedDeliveryArtifact(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "parked delivery", model.StatusIntegrationReady)
	artifact := model.DeliveryArtifact{
		ID: uuid.New(), TaskID: task.ID, ArtifactVersion: 1,
		Branch: "feature/canary", CommitSHA: strings.Repeat("a", 40),
		BaseBranch: "main", BaseSHA: strings.Repeat("b", 40),
		PreliminaryEvidence: model.JSONField{"commands": []any{map[string]any{"command": "git diff --check", "passed": true}}},
		CreatorActor:        "orchestrator", CreatorSource: "testing_ready",
	}
	require.NoError(t, srv.DB.Create(&artifact).Error)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()),
		`{"actor":"codex:canary","reason":"canary complete","mode":"obsolete"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var reloadedTask model.Task
	require.NoError(t, srv.DB.First(&reloadedTask, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusCancelled, reloadedTask.Status)
	require.Equal(t, task.StateVersion+1, reloadedTask.StateVersion)
	var reloadedArtifact model.DeliveryArtifact
	require.NoError(t, srv.DB.First(&reloadedArtifact, "id = ?", artifact.ID).Error)
	require.NotNil(t, reloadedArtifact.InvalidatedAt)
	require.Equal(t, "task_archived", reloadedArtifact.InvalidationReason)
}

func TestArchiveTaskEndpoint_IdempotentForCancelled(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "already cancelled", model.StatusCancelled)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"reason":"already obsolete"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(body, &dto))
	require.Equal(t, string(model.StatusCancelled), dto.Status)

	var count int64
	require.NoError(t, srv.DB.Model(&model.TaskEvent{}).Where("task_id = ? AND event_type = ?", task.ID, "task_archived").Count(&count).Error)
	require.Zero(t, count)
}

func TestArchiveTaskEndpoint_RequiresReason(t *testing.T) {
	_, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "obsolete", model.StatusFailed)

	resp, body := doJSON(t, http.MethodPost, archiveURL(base, task.ID.String()), `{"reason":"   "}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, decodeErr(t, body), "reason is required")
}

func TestMutationGuardReplaysExactResponseWithoutRedispatch(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "guarded", model.StatusPlanReview)
	url := approveURL(base, task.ID.String())

	first, firstBody := doGuardedJSON(t, http.MethodPost, url, `{}`, "codex:thread-42", "approve-replay-1", task.StateVersion)
	require.Equal(t, http.StatusOK, first.StatusCode, string(firstBody))
	second, secondBody := doGuardedJSON(t, http.MethodPost, url, `{}`, "codex:thread-42", "approve-replay-1", task.StateVersion)
	require.Equal(t, http.StatusOK, second.StatusCode, string(secondBody))
	require.Equal(t, "true", second.Header.Get("X-Drem-Idempotent-Replay"))
	require.Equal(t, firstBody, secondBody)
	require.Len(t, fake.Calls, 1)

	var records []model.TaskMutationRecord
	require.NoError(t, srv.DB.Where("task_id = ?", task.ID).Find(&records).Error)
	require.Len(t, records, 1)
	require.Equal(t, "succeeded", records[0].Outcome)
	require.Equal(t, "codex:thread-42", records[0].Actor)
	require.Equal(t, task.StateVersion, records[0].ObservedStateVersion)
}

func TestMutationGuardRejectsStaleGenericAndConflictingRequests(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "guarded", model.StatusPlanReview)
	url := approveURL(base, task.ID.String())

	resp, body := doGuardedJSON(t, http.MethodPost, url, `{}`, "codex:thread-42", "stale-1", task.StateVersion+1)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Empty(t, fake.Calls)

	resp, body = doGuardedJSON(t, http.MethodPost, url, `{}`, "user", "generic-1", task.StateVersion)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "specific mutation actor")

	resp, body = doGuardedJSON(t, http.MethodPost, url, `{}`, "codex:thread-42", "conflict-1", task.StateVersion)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	resp, body = doGuardedJSON(t, http.MethodPost, url, `{"different":true}`, "codex:thread-42", "conflict-1", task.StateVersion)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "different mutation")
	require.Len(t, fake.Calls, 1)
}

func TestMutationGuardFailsClosedForPendingClaimAndActorMismatch(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := testutil.CreateTask(t, srv.DB, project.ID, "guarded", model.StatusPlanReview)
	url := approveURL(base, task.ID.String())

	resp, body := doGuardedJSON(t, http.MethodPost, url, `{"actor":"kyle:loop-1"}`, "codex:thread-42", "actor-mismatch-1", task.StateVersion)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "does not match")

	resp, body = doGuardedJSON(t, http.MethodPost, url, `{}`, "codex:thread-42", "pending-1", task.StateVersion)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var record model.TaskMutationRecord
	require.NoError(t, srv.DB.Where("idempotency_key = ?", "pending-1").First(&record).Error)
	require.NoError(t, srv.DB.Model(&record).Updates(map[string]any{"outcome": "pending", "completed_at": nil}).Error)

	resp, body = doGuardedJSON(t, http.MethodPost, url, `{}`, "codex:thread-42", "pending-1", task.StateVersion)
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
	require.Contains(t, decodeErr(t, body), "uncertain")
	require.Len(t, fake.Calls, 1)
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
