package orchhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

var _ orchhttp.LogStreamer = (*container.DockerRuntime)(nil)

// fakeLogStreamer satisfies orchhttp.LogStreamer. It counts how many
// times StreamLogs is called and returns a deterministic reader built
// from the supplied payload, so tests can assert that the handler
// invoked it exactly once and piped its bytes through unchanged.
type fakeLogStreamer struct {
	payload string
	lastID  atomic.Value // string
	lastOpt atomic.Value // container.LogOptions
	calls   atomic.Int32
}

func (f *fakeLogStreamer) StreamLogs(_ context.Context, id string, opts container.LogOptions) (io.ReadCloser, error) {
	f.calls.Add(1)
	f.lastID.Store(id)
	f.lastOpt.Store(opts)
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

// projectName is reused by every seed helper so fixtures line up with
// the ProjectInfo handed to the Server.
const projectName = "test-project"

// setupHTTPTest seeds a fresh in-memory DB, creates a project row
// matching the Server's ProjectInfo, and returns the constructed
// server plus the seeded project ID so tests can attach more fixtures.
func setupHTTPTest(t *testing.T, logs orchhttp.LogStreamer) (*orchhttp.Server, *httptest.Server, model.Project) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t)
	project := testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	srv := orchhttp.New(db, "secret-token", logs, orchhttp.ProjectInfo{
		Name:     projectName,
		Language: "go",
		OrchURL:  "http://localhost:8080",
	})
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return srv, ts, project
}

func TestListProjectsReturnsProjectInfo(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)

	resp, err := http.Get(ts.URL + "/projects")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.ProjectDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 1)
	require.Equal(t, projectName, got[0].Name)
	require.Equal(t, "go", got[0].Language)
	require.Equal(t, "http://localhost:8080", got[0].OrchURL)
}

func TestListTasksFiltersByStatus(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	testutil.CreateTask(t, srv.DB, project.ID, "backlog task", model.StatusBacklog)
	testutil.CreateTask(t, srv.DB, project.ID, "planning task", model.StatusPlanning)
	testutil.CreateTask(t, srv.DB, project.ID, "other backlog", model.StatusBacklog)

	resp, err := http.Get(ts.URL + "/projects/" + projectName + "/tasks?status=backlog")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 2)
	for _, d := range got {
		require.Equal(t, string(model.StatusBacklog), d.Status)
	}
}

func TestListTasksHidesCancelledByDefault(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	testutil.CreateTask(t, srv.DB, project.ID, "active", model.StatusBacklog)
	testutil.CreateTask(t, srv.DB, project.ID, "archived", model.StatusCancelled)

	resp, err := http.Get(ts.URL + "/projects/" + projectName + "/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 1)
	require.Equal(t, "active", got[0].Title)

	resp2, err := http.Get(ts.URL + "/projects/" + projectName + "/tasks?status=cancelled")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	got = nil
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&got))
	require.Len(t, got, 1)
	require.Equal(t, "archived", got[0].Title)
}

func TestListTasksIncludesDiagnosticsFromContextAndEvents(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	contextTask := testutil.CreateTask(t, srv.DB, project.ID, "context failure", model.StatusFailed)
	require.NoError(t, srv.DB.Model(&contextTask).Updates(map[string]any{
		"category": model.CategoryQuickFix,
		"context": model.JSONField{
			"current_health":    "needs_attention",
			"failure_diagnosis": "merge conflict while applying worker branch",
			"failure_category":  "merge_conflict",
		},
	}).Error)
	eventTask := testutil.CreateTask(t, srv.DB, project.ID, "event failure", model.StatusInProgress)
	failureAt := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    eventTask.ID,
		EventType: "test_result",
		NewValue:  "integration tests failed",
		Details: model.JSONField{
			"success": false,
			"summary": "go test ./... failed in package x",
		},
		Actor:     "worker-1",
		CreatedAt: failureAt,
	}).Error)

	resp, err := http.Get(ts.URL + "/projects/" + projectName + "/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	byTitle := map[string]orchdto.TaskDTO{}
	for _, d := range got {
		byTitle[d.Title] = d
	}

	fromContext := byTitle["context failure"]
	require.Equal(t, "quickfix", fromContext.Category)
	require.Equal(t, "needs_attention", fromContext.CurrentHealth)
	require.Equal(t, "merge conflict while applying worker branch", fromContext.LatestFailureSummary)
	require.Equal(t, "merge_conflict", fromContext.LatestFailureType)
	require.NotNil(t, fromContext.LatestFailureCurrent)
	require.True(t, *fromContext.LatestFailureCurrent)

	fromEvent := byTitle["event failure"]
	require.Equal(t, "standard", fromEvent.Category)
	require.Equal(t, "go test ./... failed in package x", fromEvent.LatestFailureSummary)
	require.Equal(t, "test_failure", fromEvent.LatestFailureType)
	require.NotNil(t, fromEvent.LatestFailureAt)
	require.True(t, failureAt.Equal(*fromEvent.LatestFailureAt))
	require.NotNil(t, fromEvent.LatestFailureCurrent)
	require.False(t, *fromEvent.LatestFailureCurrent)
}

func TestListTasksUnknownProjectReturns404(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)

	resp, err := http.Get(ts.URL + "/projects/nope/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCreateTaskCreatesClassifyingTask(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	resp, err := http.Post(ts.URL+"/projects/"+projectName+"/tasks", "application/json",
		strings.NewReader(`{"title":"File supported task","description":"Create through orchestrator HTTP","actor":"kyle"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var dto orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&dto))
	require.Equal(t, "File supported task", dto.Title)
	require.Equal(t, string(model.StatusClassifying), dto.Status)

	var task model.Task
	require.NoError(t, srv.DB.First(&task, "id = ?", dto.ID).Error)
	require.Equal(t, project.ID, task.ProjectID)
	require.Equal(t, "Create through orchestrator HTTP", task.Description)
	require.Equal(t, model.StatusClassifying, task.Status)
	require.Equal(t, model.CategoryStandard, task.Category)

	var event model.TaskEvent
	require.NoError(t, srv.DB.First(&event, "task_id = ? AND event_type = ?", task.ID, "task_created").Error)
	require.Equal(t, "kyle", event.Actor)
	require.Equal(t, string(model.StatusClassifying), event.NewValue)
}

func TestCreateTaskDefaultsActorToCSuite(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	resp, err := http.Post(ts.URL+"/projects/"+projectName+"/tasks", "application/json",
		strings.NewReader(`{"title":"Default actor","description":"No actor supplied"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var dto orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&dto))

	var event model.TaskEvent
	require.NoError(t, srv.DB.First(&event, "task_id = ? AND event_type = ?", dto.ID, "task_created").Error)
	require.Equal(t, "csuite", event.Actor)
}

func TestCreateTaskMissingTitleOrDescriptionReturns400(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)

	resp, err := http.Post(ts.URL+"/projects/"+projectName+"/tasks", "application/json",
		strings.NewReader(`{"description":"missing title"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, err = http.Post(ts.URL+"/projects/"+projectName+"/tasks", "application/json",
		strings.NewReader(`{"title":"missing description"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCreateTaskWrongProjectReturns404(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)

	resp, err := http.Post(ts.URL+"/projects/nope/tasks", "application/json",
		strings.NewReader(`{"title":"x","description":"y"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGetWorkerKnownAndUnknown(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	ag := testutil.CreateAgent(t, srv.DB, uuid.Nil, model.AgentCoder, model.AgentWorking)
	completedAt := time.Date(2026, 4, 28, 12, 30, 0, 0, time.UTC)
	// Attach the agent to the project so list endpoints would find it;
	// not strictly required for GetWorker but makes the fixture realistic.
	require.NoError(t, srv.DB.Model(&ag).Updates(map[string]any{
		"project_id":            project.ID,
		"provider":              "codex",
		"model_id":              "gpt-5.5",
		"effort":                "high",
		"completed_at":          completedAt,
		"exit_reason":           "completed",
		"total_cost_usd":        1.23,
		"final_context_pct":     72,
		"tokens_in":             1000,
		"tokens_out":            250,
		"constraint_violations": 2,
	}).Error)

	// Known.
	resp, err := http.Get(ts.URL + "/workers/" + ag.ID.String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	for _, key := range []string{
		"provider", "model_id", "effort", "completed_at", "exit_reason", "total_cost_usd",
		"final_context_pct", "tokens_in", "tokens_out", "constraint_violations",
	} {
		require.Contains(t, raw, key)
	}

	var got orchdto.WorkerDTO
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, ag.ID.String(), got.ID)
	require.Equal(t, string(model.AgentCoder), got.AgentType)
	require.Equal(t, "codex", got.Provider)
	require.Equal(t, "gpt-5.5", got.ModelID)
	require.Equal(t, "high", got.Effort)
	require.NotNil(t, got.CompletedAt)
	require.Equal(t, completedAt, got.CompletedAt.UTC())
	require.Equal(t, "completed", got.ExitReason)
	require.Equal(t, 1.23, got.TotalCostUSD)
	require.Equal(t, 72, got.FinalContextPct)
	require.Equal(t, 1000, got.TokensIn)
	require.Equal(t, 250, got.TokensOut)
	require.Equal(t, 2, got.ConstraintViolations)

	// Unknown.
	resp2, err := http.Get(ts.URL + "/workers/" + uuid.NewString())
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusNotFound, resp2.StatusCode)
}

func TestListWorkersForProject(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	ag1 := testutil.CreateAgent(t, srv.DB, uuid.Nil, model.AgentCoder, model.AgentWorking)
	ag2 := testutil.CreateAgent(t, srv.DB, uuid.Nil, model.AgentPlanner, model.AgentIdle)
	require.NoError(t, srv.DB.Model(&ag1).Updates(map[string]any{
		"project_id":        project.ID,
		"provider":          "sglang-direct",
		"model_id":          "qwen-coder",
		"tokens_in":         321,
		"tokens_out":        123,
		"total_cost_usd":    0.45,
		"final_context_pct": 64,
	}).Error)
	require.NoError(t, srv.DB.Model(&ag2).Update("project_id", project.ID).Error)

	resp, err := http.Get(ts.URL + "/projects/" + projectName + "/workers")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.WorkerDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 2)
	workersByID := map[string]orchdto.WorkerDTO{}
	for _, worker := range got {
		workersByID[worker.ID] = worker
	}
	require.Equal(t, "sglang-direct", workersByID[ag1.ID.String()].Provider)
	require.Equal(t, "qwen-coder", workersByID[ag1.ID.String()].ModelID)
	require.Equal(t, 321, workersByID[ag1.ID.String()].TokensIn)
	require.Equal(t, 123, workersByID[ag1.ID.String()].TokensOut)
	require.Equal(t, 0.45, workersByID[ag1.ID.String()].TotalCostUSD)
	require.Equal(t, 64, workersByID[ag1.ID.String()].FinalContextPct)
}

func TestTaskAttemptsReturnsOnlyAttemptsForTask(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	task := testutil.CreateTask(t, srv.DB, project.ID, "target", model.StatusInProgress)
	otherTask := testutil.CreateTask(t, srv.DB, project.ID, "other", model.StatusInProgress)

	started := time.Now().UTC().Add(-10 * time.Minute)
	heartbeat := started.Add(5 * time.Minute)
	completed := started.Add(9 * time.Minute)
	ag := testutil.CreateAgent(t, srv.DB, task.ID, model.AgentCoder, model.AgentDead)
	require.NoError(t, srv.DB.Model(&ag).Updates(map[string]any{
		"name":                  "coder-worker",
		"project_id":            project.ID,
		"tmux_session":          "container-target",
		"worktree_branch":       "feature/target",
		"provider":              "codex",
		"model_id":              "gpt-5.5",
		"effort":                "high",
		"heartbeat_at":          heartbeat,
		"completed_at":          completed,
		"exit_reason":           "success",
		"tokens_in":             100,
		"tokens_out":            20,
		"total_cost_usd":        0.12,
		"final_context_pct":     61,
		"constraint_violations": 1,
		"created_at":            started,
	}).Error)

	assigned := testutil.CreateAgent(t, srv.DB, uuid.Nil, model.AgentReviewer, model.AgentWorking)
	require.NoError(t, srv.DB.Model(&assigned).Updates(map[string]any{
		"project_id":      project.ID,
		"tmux_session":    "assigned-container",
		"worktree_branch": "feature/review",
	}).Error)
	require.NoError(t, srv.DB.Model(&task).Update("assigned_agent_id", assigned.ID).Error)

	stale := testutil.CreateAgent(t, srv.DB, otherTask.ID, model.AgentCoder, model.AgentDead)
	require.NoError(t, srv.DB.Model(&stale).Updates(map[string]any{
		"project_id":   project.ID,
		"tmux_session": "stale-container",
	}).Error)

	attemptID := uuid.New()
	agentID := ag.ID
	require.NoError(t, srv.DB.Create(&model.WorkerAttempt{
		ID:          attemptID,
		TaskID:      task.ID,
		AgentID:     &agentID,
		WorkerID:    "worker-label-1",
		ContainerID: "container-target",
		AgentType:   string(model.AgentCoder),
		Image:       "worker-image",
		CreatedAt:   started,
	}).Error)
	spawnID := uuid.New()
	retrySpawnID := uuid.New()
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        spawnID,
		TaskID:    task.ID,
		EventType: "worker_spawned",
		NewValue:  string(model.AgentCoder),
		Actor:     "orchestrator",
		Details: model.JSONField{
			"agent_id":     ag.ID.String(),
			"attempt_id":   attemptID.String(),
			"worker_id":    "worker-label-1",
			"container_id": "container-target",
			"agent_type":   string(model.AgentCoder),
		},
		CreatedAt: started,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        retrySpawnID,
		TaskID:    task.ID,
		EventType: "worker_spawned",
		NewValue:  string(model.AgentCoder),
		Actor:     "orchestrator",
		Details: model.JSONField{
			"worker_id":    "worker-label-retry",
			"container_id": "container-retry",
			"agent_type":   string(model.AgentCoder),
		},
		CreatedAt: started.Add(time.Minute),
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    otherTask.ID,
		EventType: "worker_spawned",
		NewValue:  string(model.AgentCoder),
		Actor:     "orchestrator",
		Details: model.JSONField{
			"agent_id":     stale.ID.String(),
			"worker_id":    "stale-worker",
			"container_id": "stale-container",
		},
	}).Error)

	resp, err := http.Get(ts.URL + "/tasks/" + task.ID.String() + "/attempts")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.WorkerAttemptDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 3)

	byContainer := map[string]orchdto.WorkerAttemptDTO{}
	for _, attempt := range got {
		require.Equal(t, task.ID.String(), attempt.TaskID)
		require.NotEqual(t, stale.ID.String(), attempt.AgentID)
		byContainer[attempt.ContainerID] = attempt
	}

	spawnAttempt := byContainer["container-target"]
	require.Equal(t, attemptID.String(), spawnAttempt.AttemptID)
	require.Equal(t, ag.ID.String(), spawnAttempt.AgentID)
	require.Equal(t, "worker-label-1", spawnAttempt.WorkerID)
	require.Equal(t, "feature/target", spawnAttempt.Branch)
	require.Equal(t, "codex", spawnAttempt.Provider)
	require.Equal(t, "gpt-5.5", spawnAttempt.ModelID)
	require.Equal(t, "high", spawnAttempt.Effort)
	require.Equal(t, string(model.AgentDead), spawnAttempt.Status)
	require.Equal(t, heartbeat, spawnAttempt.LastHeartbeat.UTC())
	require.NotNil(t, spawnAttempt.CompletedAt)
	require.Equal(t, completed, spawnAttempt.CompletedAt.UTC())
	require.Equal(t, "success", spawnAttempt.ExitReason)
	require.Equal(t, 100, spawnAttempt.TokensIn)
	require.Equal(t, 20, spawnAttempt.TokensOut)
	require.Equal(t, 0.12, spawnAttempt.TotalCostUSD)
	require.Equal(t, 61, spawnAttempt.FinalContextPct)
	require.Equal(t, 1, spawnAttempt.ConstraintViolations)

	require.Equal(t, retrySpawnID.String(), byContainer["container-retry"].AttemptID)
	require.Equal(t, assigned.ID.String(), byContainer["assigned-container"].AttemptID)
	require.NotContains(t, byContainer, "stale-container")
}

func TestTaskAttemptsIncludesBoundedFailureEvidence(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	task := testutil.CreateTask(t, srv.DB, project.ID, "attempt failure", model.StatusFailed)
	agent := testutil.CreateAgent(t, srv.DB, task.ID, model.AgentCoder, model.AgentDead)
	containerID := "container-failure"
	require.NoError(t, srv.DB.Model(&agent).Updates(map[string]any{
		"project_id":   project.ID,
		"tmux_session": containerID,
		"exit_reason":  "error",
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "worker_spawned",
		Actor:     "orchestrator",
		Details: model.JSONField{
			"agent_id":     agent.ID.String(),
			"worker_id":    "worker-failure",
			"container_id": containerID,
		},
	}).Error)
	longMessage := "build failed DREM_TOKEN=super-secret " + strings.Repeat("x", 700)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "build_error",
		Actor:     "worker-failure",
		OldValue:  containerID,
		NewValue:  longMessage,
		Details: model.JSONField{
			"container_id": containerID,
			"message":      longMessage,
		},
	}).Error)

	resp, err := http.Get(ts.URL + "/tasks/" + task.ID.String() + "/attempts")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.WorkerAttemptDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 1)
	require.Equal(t, "build_error", got[0].FailureClassification)
	require.LessOrEqual(t, len(got[0].FirstError), 512)
	require.Contains(t, got[0].FirstError, "DREM_TOKEN=[REDACTED]")
	require.NotContains(t, got[0].FirstError, "super-secret")
}

func TestGetLogsInvokesStreamer(t *testing.T) {
	streamer := &fakeLogStreamer{payload: "hello logs\n"}
	_, ts, _ := setupHTTPTest(t, streamer)
	since := time.Date(2026, 4, 19, 12, 30, 0, 123, time.UTC)

	resp, err := http.Get(ts.URL + "/logs?container=abc&follow=true&since=" + since.Format(time.RFC3339Nano))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "hello logs\n", string(body))
	require.EqualValues(t, 1, streamer.calls.Load())
	require.Equal(t, "abc", streamer.lastID.Load())
	opts := streamer.lastOpt.Load().(container.LogOptions)
	require.True(t, opts.Follow)
	require.True(t, since.Equal(opts.Since))
}

func TestGetLogsDefaultsToBoundedLogs(t *testing.T) {
	streamer := &fakeLogStreamer{payload: "hello logs\n"}
	_, ts, _ := setupHTTPTest(t, streamer)

	resp, err := http.Get(ts.URL + "/logs?container=abc")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	opts := streamer.lastOpt.Load().(container.LogOptions)
	require.False(t, opts.Follow)
	require.True(t, opts.Since.IsZero())
}

func TestGetLogsResolvesDurableAttemptToContainer(t *testing.T) {
	streamer := &fakeLogStreamer{payload: "attempt logs\n"}
	srv, ts, project := setupHTTPTest(t, streamer)
	task := testutil.CreateTask(t, srv.DB, project.ID, "logs", model.StatusInProgress)
	attemptID := uuid.New()
	require.NoError(t, srv.DB.Create(&model.WorkerAttempt{
		ID:          attemptID,
		TaskID:      task.ID,
		WorkerID:    "worker-label-1",
		ContainerID: "container-attempt",
		AgentType:   string(model.AgentCoder),
	}).Error)

	resp, err := http.Get(ts.URL + "/logs?attempt=" + attemptID.String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "attempt logs\n", string(body))
	require.Equal(t, "container-attempt", streamer.lastID.Load())
}

func TestGetLogsResolvesLegacySpawnAttemptToContainer(t *testing.T) {
	streamer := &fakeLogStreamer{payload: "legacy logs\n"}
	srv, ts, project := setupHTTPTest(t, streamer)
	task := testutil.CreateTask(t, srv.DB, project.ID, "logs", model.StatusInProgress)
	spawnID := uuid.New()
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        spawnID,
		TaskID:    task.ID,
		EventType: "worker_spawned",
		Actor:     "orchestrator",
		Details: model.JSONField{
			"container_id": "legacy-container",
		},
	}).Error)

	resp, err := http.Get(ts.URL + "/logs?attempt=" + spawnID.String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "legacy-container", streamer.lastID.Load())
}

func TestGetLogsRequiresContainer(t *testing.T) {
	streamer := &fakeLogStreamer{payload: ""}
	_, ts, _ := setupHTTPTest(t, streamer)

	resp, err := http.Get(ts.URL + "/logs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.EqualValues(t, 0, streamer.calls.Load())
}

func TestIngestRejectsMissingToken(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)

	body := `{"records":[{"type":"heartbeat","container_id":"c1","worker_id":"w1","timestamp":"2026-04-19T00:00:00Z","agent_id":"a1"}]}`
	resp, err := http.Post(ts.URL+"/internal/logs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIngestAcceptsKnownRecords(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	records := []map[string]any{
		{"type": "commit", "container_id": "c1", "worker_id": uuid.NewString(), "timestamp": time.Now().UTC(), "sha": "abc", "branch": "main", "message": "wip"},
		{"type": "push", "container_id": "c1", "worker_id": uuid.NewString(), "timestamp": time.Now().UTC(), "branch": "main", "remote": "origin"},
		{"type": "heartbeat", "container_id": "c1", "worker_id": uuid.NewString(), "timestamp": time.Now().UTC(), "agent_id": "a1"},
	}
	body, err := json.Marshal(map[string]any{"records": records})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Drem-Agentmon-Token", "secret-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var ir orchdto.IngestResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ir))
	require.Equal(t, 3, ir.Accepted)

	var count int64
	require.NoError(t, srv.DB.Model(&model.TaskEvent{}).Count(&count).Error)
	require.EqualValues(t, 3, count)
}

func TestIngestAttributesMissingTaskIDFromCurrentAgent(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	task := testutil.CreateTask(t, srv.DB, project.ID, "current task", model.StatusInProgress)
	agentID := uuid.New()
	workerID := "worker-label-1"
	containerID := "container-1"
	require.NoError(t, srv.DB.Create(&model.Agent{
		ID:            agentID,
		ProjectID:     project.ID,
		AgentType:     model.AgentCoder,
		Name:          "coder-1",
		Status:        model.AgentWorking,
		CurrentTaskID: &task.ID,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: "worker_spawned",
		Actor:     workerID,
		OldValue:  containerID,
		Details: model.JSONField{
			"agent_id":     agentID.String(),
			"worker_id":    workerID,
			"container_id": containerID,
		},
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}).Error)

	records := []map[string]any{
		{"type": "heartbeat", "agent_id": agentID.String(), "timestamp": time.Now().UTC()},
		{"type": "tool_call", "worker_id": workerID, "timestamp": time.Now().UTC(), "tool": "Read", "target": "main.go"},
		{"type": "crash", "container_id": containerID, "timestamp": time.Now().UTC(), "reason": "exited"},
	}
	body, err := json.Marshal(map[string]any{"records": records})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Drem-Agentmon-Token", "secret-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var events []model.TaskEvent
	require.NoError(t, srv.DB.Where("event_type IN ?", []string{"heartbeat", "tool_call", "crash"}).Order("event_type ASC").Find(&events).Error)
	require.Len(t, events, 3)
	for _, event := range events {
		require.Equal(t, task.ID, event.TaskID)
	}
}

func TestIngestLeavesUnmatchedMissingTaskIDUnattributed(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	record := map[string]any{
		"type":         "heartbeat",
		"container_id": "unknown-container",
		"worker_id":    "unknown-worker",
		"agent_id":     uuid.NewString(),
		"timestamp":    time.Now().UTC(),
	}
	body, err := json.Marshal(map[string]any{"records": []any{record}})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Drem-Agentmon-Token", "secret-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var event model.TaskEvent
	require.NoError(t, srv.DB.First(&event, "event_type = ?", "heartbeat").Error)
	require.Equal(t, uuid.Nil, event.TaskID)
}

func TestIngestAcceptsMergeResultAndUpdatesTaskContext(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	task := testutil.CreateTask(t, srv.DB, project.ID, "merge me", model.StatusMerging)

	record := map[string]any{
		"type":           "merge_result",
		"container_id":   "container-1",
		"worker_id":      "merger-worker-1",
		"task_id":        task.ID.String(),
		"success":        false,
		"failure_reason": "conflict",
		"conflicts":      []string{"README.md"},
		"test_output":    "merge failed",
	}
	body, err := json.Marshal(map[string]any{"records": []any{record}})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Drem-Agentmon-Token", "secret-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var saved model.Task
	require.NoError(t, srv.DB.First(&saved, "id = ?", task.ID).Error)
	require.Equal(t, "conflict", saved.Context["merge_failure_reason"])
	require.Equal(t, "merge failed", saved.Context["merge_test_output"])
	require.Equal(t, []any{"README.md"}, saved.Context["merge_conflicts"])

	var evt model.TaskEvent
	require.NoError(t, srv.DB.First(&evt, "event_type = ?", "merge_result").Error)
	require.Equal(t, task.ID, evt.TaskID)
	require.Equal(t, "merger-worker-1", evt.Actor)
	require.Equal(t, "container-1", evt.OldValue)
}

// TestIngestAcceptsAgentmonHTTPIngestorRoundTrip is the cross-package
// contract test for the agentmon↔orch authentication path. It drives
// the real agentmon.HTTPIngestor (the production client) against the
// real orchhttp.Server.requireAgentmonToken middleware (the production
// server) with a matching shared token, and asserts the round-trip
// succeeds.
//
// Motivation: the April 2026 41-hour ingest-401 outage slipped through
// two separately-passing test suites. internal/orchhttp's middleware
// tests hand-roll the X-Drem-Agentmon-Token header with the expected
// name; internal/agentmon's client tests stand up an httptest server
// and read whatever header the client chose to send. Neither caught
// the fact that a server-side config gap left SharedToken="". Nor
// would either catch a future rename of the header constant on one
// side but not the other. This test pins the two together: the actual
// header constant on the server must match the actual header constant
// on the client, and the actual URL path on the server must match the
// actual URL path on the client. If either side drifts, this test
// fails.
func TestIngestAcceptsAgentmonHTTPIngestorRoundTrip(t *testing.T) {
	const token = "round-trip-secret"

	db := testutil.NewTestDBWithModels(t)
	testutil.CreateProject(t, db, projectName, "/tmp/repo.git", "master")
	srv := orchhttp.New(db, token, nil, orchhttp.ProjectInfo{
		Name:     projectName,
		Language: "go",
		OrchURL:  "http://localhost:8080",
	})
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	// Drive the production agentmon client — not a hand-rolled
	// http.NewRequest — so a future rename of agentmonTokenHeader or
	// the /internal/logs path on either side fails this test.
	ing := &agentmon.HTTPIngestor{OrchURL: ts.URL, Token: token}
	err := ing.Ingest(context.Background(), []agentmon.IngestRecord{{
		Type:        "heartbeat",
		ContainerID: "c1",
		WorkerID:    "w1",
		Timestamp:   time.Now().UTC(),
		Payload:     map[string]any{"agent_id": "a1"},
	}})
	require.NoError(t, err, "agentmon HTTPIngestor and orchhttp.requireAgentmonToken must agree on header name and path; a 401 here means one side renamed its constant without updating the other")

	// Belt-and-suspenders: the event row should have landed.
	var count int64
	require.NoError(t, srv.DB.Model(&model.TaskEvent{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestIngestRejectsUnknownTypeAllOrNothing(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	records := []map[string]any{
		{"type": "commit", "container_id": "c1", "worker_id": uuid.NewString(), "timestamp": time.Now().UTC(), "sha": "abc", "message": "wip"},
		{"type": "never-heard-of-this-type", "container_id": "c1", "worker_id": uuid.NewString(), "timestamp": time.Now().UTC()},
	}
	body, err := json.Marshal(map[string]any{"records": records})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/logs", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Drem-Agentmon-Token", "secret-token")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// All-or-nothing: no rows should have been written even though the
	// first record was valid.
	var count int64
	require.NoError(t, srv.DB.Model(&model.TaskEvent{}).Count(&count).Error)
	require.EqualValues(t, 0, count)
}

func TestListEventsSinceFilter(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	old := time.Now().UTC().Add(-2 * time.Hour)
	newer := time.Now().UTC()
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "commit", Actor: "w1", CreatedAt: old,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "push", Actor: "w1", CreatedAt: newer,
	}).Error)

	since := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	resp, err := http.Get(ts.URL + "/events?since=" + since)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var events []orchdto.EventDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&events))
	require.Len(t, events, 1)
	require.Equal(t, "push", events[0].Type)
}

func TestListEventsWithoutSinceReturnsLatestFirst(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	old := time.Now().UTC().Add(-2 * time.Hour)
	newer := time.Now().UTC()
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "old", Actor: "w1", CreatedAt: old,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "newer", Actor: "w1", CreatedAt: newer,
	}).Error)

	resp, err := http.Get(ts.URL + "/events?limit=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var events []orchdto.EventDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&events))
	require.Len(t, events, 1)
	require.Equal(t, "newer", events[0].Type)
}

func TestWorkerHistoryReturnsEvents(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)

	workerID := uuid.New()
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "commit", Actor: workerID.String(), NewValue: "wip",
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "heartbeat", Actor: workerID.String(),
	}).Error)
	// Unrelated worker's event — must not appear.
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: uuid.New(), EventType: "commit", Actor: uuid.NewString(),
	}).Error)

	resp, err := http.Get(ts.URL + "/workers/" + workerID.String() + "/history")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var hist orchdto.WorkerHistoryDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&hist))
	require.Equal(t, workerID.String(), hist.WorkerID)
	require.Len(t, hist.Events, 2)
}

func TestWorkerHistoryResolvesContainerIDToMergeResult(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)
	taskID := uuid.New()
	containerID := "container-full-id"
	workerID := "merger-1234-abcd"
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "worker_spawned",
		NewValue:  "merger",
		Actor:     "orchestrator",
		Details: model.JSONField{
			"container_id": containerID,
			"worker_id":    workerID,
		},
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "merge_result",
		OldValue:  "short-container",
		NewValue:  "tests_failed",
		Actor:     workerID,
		Details: model.JSONField{
			"task_id":        taskID.String(),
			"worker_id":      workerID,
			"failure_reason": "tests_failed",
			"test_output":    "go test failed",
		},
		CreatedAt: time.Now().UTC(),
	}).Error)

	resp, err := http.Get(ts.URL + "/workers/" + containerID + "/history")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var hist orchdto.WorkerHistoryDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&hist))
	require.Equal(t, containerID, hist.WorkerID)
	require.Len(t, hist.Events, 2)
	require.Equal(t, "merge_result", hist.Events[1].Kind)
	require.Contains(t, string(hist.Events[1].Details), "go test failed")
}

func TestWorkerHistoryExcludesStaleFuzzyRowsAndIncludesCurrentAttributedRows(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)
	taskID := uuid.New()
	containerID := "container-current"
	workerID := "worker-current"
	staleTime := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    uuid.New(),
		EventType: "commit",
		Actor:     "unrelated-worker",
		NewValue:  "mentions " + containerID + " but is not this worker",
		Details: model.JSONField{
			"message": "old fuzzy mention of " + containerID,
		},
		CreatedAt: staleTime,
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "worker_spawned",
		Actor:     "orchestrator",
		Details: model.JSONField{
			"worker_id":    workerID,
			"container_id": containerID,
		},
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}).Error)
	require.NoError(t, srv.DB.Create(&model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    taskID,
		EventType: "test_result",
		Actor:     workerID,
		OldValue:  containerID,
		NewValue:  "tests failed",
		Details: model.JSONField{
			"worker_id":    workerID,
			"container_id": containerID,
			"summary":      "tests failed",
		},
		CreatedAt: time.Now().UTC(),
	}).Error)

	resp, err := http.Get(ts.URL + "/workers/" + containerID + "/history")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var hist orchdto.WorkerHistoryDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&hist))
	kinds := make([]string, 0, len(hist.Events))
	for _, event := range hist.Events {
		kinds = append(kinds, event.Kind)
		require.NotEqual(t, "commit", event.Kind)
	}
	require.ElementsMatch(t, []string{"worker_spawned", "test_result"}, kinds)
}
