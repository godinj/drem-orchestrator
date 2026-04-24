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
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// fakeLogStreamer satisfies orchhttp.LogStreamer. It counts how many
// times StreamLogs is called and returns a deterministic reader built
// from the supplied payload, so tests can assert that the handler
// invoked it exactly once and piped its bytes through unchanged.
type fakeLogStreamer struct {
	payload string
	lastID  atomic.Value // string
	calls   atomic.Int32
}

func (f *fakeLogStreamer) StreamLogs(_ context.Context, id string) (io.ReadCloser, error) {
	f.calls.Add(1)
	f.lastID.Store(id)
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

func TestListTasksUnknownProjectReturns404(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)

	resp, err := http.Get(ts.URL + "/projects/nope/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGetWorkerKnownAndUnknown(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)

	ag := testutil.CreateAgent(t, srv.DB, uuid.Nil, model.AgentCoder, model.AgentWorking)
	// Attach the agent to the project so list endpoints would find it;
	// not strictly required for GetWorker but makes the fixture realistic.
	require.NoError(t, srv.DB.Model(&ag).Update("project_id", project.ID).Error)

	// Known.
	resp, err := http.Get(ts.URL + "/workers/" + ag.ID.String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got orchdto.WorkerDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, ag.ID.String(), got.ID)
	require.Equal(t, string(model.AgentCoder), got.AgentType)

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
	require.NoError(t, srv.DB.Model(&ag1).Update("project_id", project.ID).Error)
	require.NoError(t, srv.DB.Model(&ag2).Update("project_id", project.ID).Error)

	resp, err := http.Get(ts.URL + "/projects/" + projectName + "/workers")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []orchdto.WorkerDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 2)
}

func TestGetLogsInvokesStreamer(t *testing.T) {
	streamer := &fakeLogStreamer{payload: "hello logs\n"}
	_, ts, _ := setupHTTPTest(t, streamer)

	resp, err := http.Get(ts.URL + "/logs?container=abc")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "hello logs\n", string(body))
	require.EqualValues(t, 1, streamer.calls.Load())
	require.Equal(t, "abc", streamer.lastID.Load())
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
