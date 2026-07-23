package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

const testTaskID = "12345678-1234-1234-1234-123456789abc"

type recordedRequest struct {
	Method         string
	Path           string
	Query          string
	Body           string
	Authorization  string
	Actor          string
	IdempotencyKey string
}

func TestRequestReworkUsesObservedArtifactAndAuthenticatedActor(t *testing.T) {
	var post recordedRequest
	sha := strings.Repeat("a", 40)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordRequest(t, r)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifact"):
			writeJSONResponse(t, w, orchdto.DeliveryEnvelopeDTO{
				Task:     orchdto.TaskDTO{ID: testTaskID, Status: "integration_ready", StateVersion: 7},
				Artifact: orchdto.DeliveryArtifactDTO{ID: "artifact-1", TaskID: testTaskID, ArtifactVersion: 3, CommitSHA: sha},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/request-rework"):
			post = rec
			writeJSONResponse(t, w, orchdto.DeliveryReworkRecordDTO{
				ID: "87654321-1234-1234-1234-123456789abc", TaskID: testTaskID,
				ArtifactVersion: 3, CommitSHA: sha, Reason: "native regression",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"request-rework", testTaskID, "--mode", "orchestrated", "--reason", "native regression"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL, "DREM_PROJECT": "canvas", "DREM_ORCH_TOKEN": "token-1", "DREM_ACTOR": "codex:thread-1",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if post.Path != "/projects/canvas/tasks/"+testTaskID+"/request-rework" {
		t.Fatalf("POST path = %s", post.Path)
	}
	if post.Authorization != "Bearer token-1" || post.Actor != "codex:thread-1" {
		t.Fatalf("auth headers = %q / %q", post.Authorization, post.Actor)
	}
	var body orchdto.RequestDeliveryReworkRequest
	if err := json.Unmarshal([]byte(post.Body), &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if body.ObservedStateVersion != 7 || body.ArtifactVersion != 3 || body.CommitSHA != sha || body.Actor != "codex:thread-1" {
		t.Fatalf("unexpected guarded request: %#v", body)
	}
	if body.Mode != "orchestrated" {
		t.Fatalf("rework mode = %q", body.Mode)
	}
	if !strings.Contains(out.String(), "native regression") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDeliveryCommandsAcceptDocumentedTaskFirstSyntax(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, verb string
		args       []string
	}{
		{name: "verify", verb: "verify", args: []string{"verify", testTaskID, "--result", "pass", "--environment", "macos-arm64", "--command", "scripts/dev verify"}},
		{name: "integrate", verb: "integrate", args: []string{"integrate", testTaskID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var post recordedRequest
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifact"):
					writeJSONResponse(t, w, orchdto.DeliveryEnvelopeDTO{
						Task:               orchdto.TaskDTO{ID: testTaskID, Status: "verification_ready", StateVersion: 7},
						Artifact:           orchdto.DeliveryArtifactDTO{ID: "artifact-1", TaskID: testTaskID, ArtifactVersion: 3, CommitSHA: sha},
						LatestVerification: &orchdto.VerificationRecordDTO{ID: "87654321-1234-1234-1234-123456789abc", Result: "pass"},
					})
				case r.Method == http.MethodPost:
					post = recordRequest(t, r)
					if tc.verb == "verify" {
						writeJSONResponse(t, w, orchdto.VerificationRecordDTO{ID: "87654321-1234-1234-1234-123456789abc", ArtifactVersion: 3, CommitSHA: sha, Result: "pass"})
					} else {
						writeJSONResponse(t, w, orchdto.IntegrationAuthorizationDTO{ID: "87654321-1234-1234-1234-123456789abc", ArtifactVersion: 3, CommitSHA: sha})
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			var out, errOut bytes.Buffer
			err := run(t.Context(), tc.args, mapEnv(map[string]string{
				"DREM_ORCH_URL": ts.URL, "DREM_PROJECT": "canvas", "DREM_ORCH_TOKEN": "token-1", "DREM_ACTOR": "codex:thread-1",
			}), &out, &errOut)
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if !strings.HasSuffix(post.Path, "/"+tc.verb) {
				t.Fatalf("POST path = %s", post.Path)
			}
			if post.Authorization != "Bearer token-1" || post.Actor != "codex:thread-1" {
				t.Fatalf("auth headers = %q / %q", post.Authorization, post.Actor)
			}
		})
	}
}

func TestEnvDefaultsAndFlagOverride(t *testing.T) {
	var got recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = recordRequest(t, r)
		writeJSONResponse(t, w, []map[string]any{})
	}))
	defer ts.Close()

	env := mapEnv(map[string]string{
		"DREM_ORCH_URL": "http://wrong.invalid",
		"DREM_PROJECT":  "wrong-project",
	})
	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"--orch-url", ts.URL, "--project", "right-project", "tasks", "--limit", "2"}, env, &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got.Path != "/projects/right-project/tasks" {
		t.Fatalf("path used env instead of flag override: %s", got.Path)
	}
	if got.Query != "limit=2" {
		t.Fatalf("unexpected query: %q", got.Query)
	}
}

func TestMissingURLError(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"projects"}, mapEnv(nil), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "DREM_ORCH_URL") {
		t.Fatalf("expected missing URL error, got %v", err)
	}
}

func TestMissingProjectError(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"tasks"}, mapEnv(map[string]string{"DREM_ORCH_URL": ts.URL}), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "DREM_PROJECT") {
		t.Fatalf("expected missing project error, got %v", err)
	}
}

func TestReadCommandsHitExpectedPaths(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		response  any
		wantPath  string
		wantQuery []string
		wantOut   string
	}{
		{
			name:     "projects",
			args:     []string{"projects"},
			response: []map[string]any{{"name": "canvas", "language": "go", "orch_url": "http://orch:8080", "worker_count": 2}},
			wantPath: "/projects",
			wantOut:  "canvas",
		},
		{
			name:      "tasks",
			args:      []string{"tasks", "--status", "failed", "--limit", "5", "--offset", "10", "--include-archived"},
			response:  []map[string]any{{"id": testTaskID, "title": "Fix it", "status": "failed", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z"), "assigned_worker": "w1", "active_attempt_count": 1, "active_attempts": []map[string]any{{"attempt_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "role": "coder", "branch": "feature/x", "lease_state": "running"}}}},
			wantPath:  "/projects/canvas/tasks",
			wantQuery: []string{"status=failed", "limit=5", "offset=10", "include_archived=true"},
			wantOut:   "running/coder/feature/x/aaaaaaaa",
		},
		{
			name:     "workers",
			args:     []string{"workers"},
			response: []map[string]any{{"id": "worker-1", "project": "canvas", "agent_type": "coder", "status": "running", "branch": "b", "started_at": rfc3339("2026-04-24T10:00:00Z"), "last_heartbeat": rfc3339("2026-04-24T10:02:00Z"), "current_task": testTaskID}},
			wantPath: "/projects/canvas/workers",
			wantOut:  "running",
		},
		{
			name:     "worker",
			args:     []string{"worker", "worker-1"},
			response: map[string]any{"id": "worker-1", "project": "canvas", "agent_type": "coder", "status": "running", "started_at": rfc3339("2026-04-24T10:00:00Z"), "last_heartbeat": rfc3339("2026-04-24T10:02:00Z")},
			wantPath: "/workers/worker-1",
			wantOut:  "worker-1",
		},
		{
			name:     "history",
			args:     []string{"history", "worker-1"},
			response: map[string]any{"worker_id": "worker-1", "events": []map[string]any{{"timestamp": rfc3339("2026-04-24T10:00:00Z"), "kind": "exit", "detail": "done", "exit_code": 0}}},
			wantPath: "/workers/worker-1/history",
			wantOut:  "done",
		},
		{
			name:      "events",
			args:      []string{"events", "--since", "2026-04-24T10:00:00Z", "--limit", "3"},
			response:  []map[string]any{{"timestamp": rfc3339("2026-04-24T10:01:00Z"), "type": "worker.exit", "payload": map[string]any{"id": "w1"}}},
			wantPath:  "/events",
			wantQuery: []string{"since=2026-04-24T10%3A00%3A00Z", "limit=3"},
			wantOut:   "worker.exit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got recordedRequest
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = recordRequest(t, r)
				writeJSONResponse(t, w, tt.response)
			}))
			defer ts.Close()

			var out, errOut bytes.Buffer
			err := run(t.Context(), tt.args, mapEnv(map[string]string{
				"DREM_ORCH_URL": ts.URL,
				"DREM_PROJECT":  "canvas",
				"DREM_ACTOR":    "codex:test-thread",
			}), &out, &errOut)
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if got.Path != tt.wantPath {
				t.Fatalf("path = %s, want %s", got.Path, tt.wantPath)
			}
			for _, fragment := range tt.wantQuery {
				if !strings.Contains(got.Query, fragment) {
					t.Fatalf("query %q missing %q", got.Query, fragment)
				}
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Fatalf("output %q missing %q", out.String(), tt.wantOut)
			}
		})
	}
}

func TestLogsStreamsRawBody(t *testing.T) {
	var got recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = recordRequest(t, r)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "line one\nline two\n")
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"logs", "--container", "worker-a", "--follow", "--since", "2026-04-24T10:00:00Z"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got.Path != "/logs" {
		t.Fatalf("path = %s, want /logs", got.Path)
	}
	for _, fragment := range []string{"container=worker-a", "follow=true", "since=2026-04-24T10%3A00%3A00Z"} {
		if !strings.Contains(got.Query, fragment) {
			t.Fatalf("query %q missing %q", got.Query, fragment)
		}
	}
	if out.String() != "line one\nline two\n" {
		t.Fatalf("unexpected log output: %q", out.String())
	}
}

func TestHealthIssuesCommandHitsExpectedPath(t *testing.T) {
	var got recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = recordRequest(t, r)
		writeJSONResponse(t, w, []map[string]any{{
			"type":        "duplicate_active_attempts",
			"severity":    "warning",
			"task_id":     testTaskID,
			"role":        "coder",
			"branch":      "feature/x",
			"attempt_ids": []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
			"status":      "in_progress",
			"detected_at": rfc3339("2026-04-24T10:00:00Z"),
			"message":     "2 active attempts share task, role, and branch",
		}})
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"health", "issues"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got.Path != "/projects/canvas/health/issues" {
		t.Fatalf("path = %s, want /projects/canvas/health/issues", got.Path)
	}
	if !strings.Contains(out.String(), "duplicate_active_attempts") || !strings.Contains(out.String(), "coder") || !strings.Contains(out.String(), "feature/x") || !strings.Contains(out.String(), "aaaaaaaa,bbbbbbbb") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRenderHealthIssuesIncludesBlockedDependencyAndGateFailure(t *testing.T) {
	var out bytes.Buffer
	err := renderHealthIssues(&out, false, []orchdto.HealthIssueDTO{
		{
			Type:     "parent_readiness_blocked",
			Severity: "warning",
			TaskID:   testTaskID,
			Message:  "parent blocked",
			BlockedDependencies: []orchdto.BlockedDependencyDTO{{
				TaskID:       testTaskID,
				DependencyID: "abcdef12-1234-1234-1234-123456789abc",
				Status:       "in_progress",
			}},
		},
		{
			Type:     "branch_hygiene_gate_failure",
			Severity: "warning",
			TaskID:   testTaskID,
			GateFailure: &orchdto.GateFailureDTO{
				Gate:    "branch_hygiene",
				Reason:  "branch_contamination",
				Message: "trace file committed",
			},
		},
	})
	if err != nil {
		t.Fatalf("renderHealthIssues returned error: %v", err)
	}
	for _, want := range []string{"dep=abcdef12", "status=in_progress", "gate_reason=branch_contamination"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q missing %q", out.String(), want)
		}
	}
}

func TestRecoverStaleAssignmentDryRunPostsExpectedBody(t *testing.T) {
	var posts []recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := recordRequest(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{"id": testTaskID, "title": "Fix it", "status": "in_progress", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z"), "assigned_worker": "worker-1"}})
		case r.Method == http.MethodPost:
			posts = append(posts, req)
			writeJSONResponse(t, w, map[string]any{"task_id": testTaskID, "status": "in_progress", "assigned_worker": "worker-1", "worker_status": "dead", "classification": "dead_worker", "safe": true, "applied": false, "message": "assigned worker is dead"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"recover", "stale-assignment", "12345678", "--dry-run"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("POST count = %d, want 1", len(posts))
	}
	if posts[0].Path != "/projects/canvas/tasks/"+testTaskID+"/recover/stale-assignment" {
		t.Fatalf("path = %s", posts[0].Path)
	}
	for _, want := range []string{`"dry_run":true`, `"apply":false`} {
		if !strings.Contains(posts[0].Body, want) {
			t.Fatalf("body %q missing %q", posts[0].Body, want)
		}
	}
	if !strings.Contains(out.String(), "dry-run") || !strings.Contains(out.String(), "dead_worker") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestHistoryRendersMergeResultDetails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workers/container-1/history" {
			http.NotFound(w, r)
			return
		}
		writeJSONResponse(t, w, map[string]any{
			"worker_id": "container-1",
			"events": []map[string]any{{
				"timestamp": rfc3339("2026-04-24T10:00:00Z"),
				"kind":      "merge_result",
				"detail":    "tests_failed",
				"details": map[string]any{
					"success":        false,
					"failure_reason": "tests_failed",
					"test_output":    "go test ./...\nFAIL pkg",
				},
			}},
		})
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"history", "container-1"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{"merge_result", "failure_reason=tests_failed", "test_output=go test ./... FAIL pkg"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("history output %q missing %q", out.String(), want)
		}
	}
}

func TestStatusHitsCompositeEndpoints(t *testing.T) {
	var got []recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, recordRequest(t, r))
		switch r.URL.Path {
		case "/projects":
			writeJSONResponse(t, w, []map[string]any{{"name": "canvas", "language": "go", "orch_url": "http://orch:8080", "worker_count": 1}})
		case "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{
				{"id": testTaskID, "title": "A", "status": "failed", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")},
				{"id": "abcdef12-1234-1234-1234-123456789abc", "title": "B", "status": "backlog", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")},
			})
		case "/projects/canvas/workers":
			writeJSONResponse(t, w, []map[string]any{{"id": "worker-1", "status": "running", "started_at": rfc3339("2026-04-24T10:00:00Z"), "last_heartbeat": rfc3339("2026-04-24T10:02:00Z")}})
		case "/events":
			writeJSONResponse(t, w, []map[string]any{{"timestamp": rfc3339("2026-04-24T10:03:00Z"), "type": "worker.heartbeat", "payload": map[string]any{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"status"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	wantPaths := []string{"/projects", "/projects/canvas/tasks", "/projects/canvas/workers", "/events"}
	if len(got) != len(wantPaths) {
		t.Fatalf("got %d requests, want %d", len(got), len(wantPaths))
	}
	for i, want := range wantPaths {
		if got[i].Path != want {
			t.Fatalf("request %d path = %s, want %s", i, got[i].Path, want)
		}
	}
	if !strings.Contains(out.String(), "tasks: 2") || !strings.Contains(out.String(), "failed=1") || !strings.Contains(out.String(), "workers: 1") || !strings.Contains(out.String(), "live workers: 1 {running=1}") {
		t.Fatalf("unexpected status output: %q", out.String())
	}
	if got[3].Query != "limit=10" {
		t.Fatalf("events query = %q, want limit=10", got[3].Query)
	}
}

func TestStatusJSONDistinguishesHistoricalAndLiveWorkers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			writeJSONResponse(t, w, []map[string]any{{"name": "canvas", "language": "go", "orch_url": "http://orch:8080", "worker_count": 99}})
		case "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{})
		case "/projects/canvas/workers":
			workers := make([]map[string]any, 0, 40)
			for i := 0; i < 40; i++ {
				workers = append(workers, map[string]any{"id": fmt.Sprintf("worker-%d", i), "status": "dead"})
			}
			writeJSONResponse(t, w, workers)
		case "/events":
			writeJSONResponse(t, w, []map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"--json", "status"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output was not JSON: %v\n%s", err, out.String())
	}
	if decoded["worker_count"] != float64(40) || decoded["historical_worker_count"] != float64(40) || decoded["live_worker_count"] != float64(0) {
		t.Fatalf("unexpected worker counts: %#v", decoded)
	}
	liveByStatus, ok := decoded["live_workers_by_status"].(map[string]any)
	if !ok || len(liveByStatus) != 0 {
		t.Fatalf("live_workers_by_status = %#v, want empty object", decoded["live_workers_by_status"])
	}
}

func TestStatusPaginatesTasksForCompleteCounts(t *testing.T) {
	var taskQueries []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			writeJSONResponse(t, w, []map[string]any{{"name": "canvas", "language": "go", "orch_url": "http://orch:8080", "worker_count": 1}})
		case "/projects/canvas/tasks":
			taskQueries = append(taskQueries, r.URL.RawQuery)
			if r.URL.Query().Get("offset") == "500" {
				writeJSONResponse(t, w, []map[string]any{taskResponse("abcdef12-1234-1234-1234-123456789abc", "failed")})
				return
			}
			tasks := make([]map[string]any, 0, 500)
			for i := 0; i < 500; i++ {
				tasks = append(tasks, taskResponse(fmt.Sprintf("%08x-1234-1234-1234-123456789abc", i), "backlog"))
			}
			writeJSONResponse(t, w, tasks)
		case "/projects/canvas/workers":
			writeJSONResponse(t, w, []map[string]any{})
		case "/events":
			writeJSONResponse(t, w, []map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"status"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if len(taskQueries) != 2 {
		t.Fatalf("got %d task page queries, want 2: %#v", len(taskQueries), taskQueries)
	}
	if !strings.Contains(taskQueries[0], "limit=500") || strings.Contains(taskQueries[0], "offset=") {
		t.Fatalf("unexpected first page query: %q", taskQueries[0])
	}
	if !strings.Contains(taskQueries[1], "limit=500") || !strings.Contains(taskQueries[1], "offset=500") {
		t.Fatalf("unexpected second page query: %q", taskQueries[1])
	}
	if !strings.Contains(out.String(), "tasks: 501") || !strings.Contains(out.String(), "backlog=500") || !strings.Contains(out.String(), "failed=1") {
		t.Fatalf("status did not count all task pages: %q", out.String())
	}
}

func TestJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, []map[string]any{{"name": "canvas", "language": "go", "orch_url": "http://orch:8080", "worker_count": 2}})
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"projects", "--json"}, mapEnv(map[string]string{"DREM_ORCH_URL": ts.URL}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output was not JSON: %v\n%s", err, out.String())
	}
	if decoded[0]["name"] != "canvas" {
		t.Fatalf("unexpected decoded output: %#v", decoded)
	}
}

func TestMutationsResolvePrefixesAndPostExpectedBodies(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantBody string
		wantOut  string
	}{
		{name: "approve", args: []string{"approve", "12345678"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/approve", wantOut: "in_progress"},
		{name: "reject", args: []string{"reject", "12345678", "--reason", "too vague"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/reject", wantBody: `{"reason":"too vague"}`, wantOut: "in_progress"},
		{name: "pass", args: []string{"pass", "12345678"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/pass", wantOut: "in_progress"},
		{name: "fail", args: []string{"fail", "12345678"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/fail", wantOut: "in_progress"},
		{name: "answer", args: []string{"answer", "12345678", "--body", "use port 9090"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/answer", wantBody: `{"body":"use port 9090"}`, wantOut: "in_progress"},
		{name: "retry", args: []string{"retry", "12345678"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/retry", wantOut: "in_progress"},
		{name: "retry explicit key", args: []string{"retry", "12345678", "--idempotency-key", "retry-after-upgrade"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/retry", wantOut: "in_progress"},
		{name: "archive", args: []string{"archive", "12345678", "--reason", "superseded", "--actor", "codex:kyle:test"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/archive", wantBody: `{"actor":"codex:kyle:test","reason":"superseded","mode":"obsolete"}`, wantOut: "in_progress"},
		{name: "comment", args: []string{"comment", "12345678", "--body", "supersede from current base"}, wantPath: "/projects/canvas/tasks/" + testTaskID + "/comments", wantBody: `{"body":"supersede from current base"}`, wantOut: "comment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var posts []recordedRequest
			var gets []recordedRequest
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec := recordRequest(t, r)
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
					gets = append(gets, rec)
					writeJSONResponse(t, w, []map[string]any{{"id": testTaskID, "title": "A", "status": "plan_review", "state_version": 1, "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")}})
				case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks/"+testTaskID:
					gets = append(gets, rec)
					writeJSONResponse(t, w, map[string]any{"id": testTaskID, "title": "A", "status": "plan_review", "state_version": 1, "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")})
				case r.Method == http.MethodPost:
					posts = append(posts, rec)
					if strings.HasSuffix(r.URL.Path, "/comments") {
						writeJSONResponse(t, w, map[string]any{"id": "87654321-1234-1234-1234-123456789abc", "task_id": testTaskID, "author": "csuite", "body": "supersede from current base", "created_at": rfc3339("2026-04-24T10:01:00Z")})
						return
					}
					writeJSONResponse(t, w, map[string]any{"id": testTaskID, "title": "A", "status": "in_progress", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")})
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()

			var out, errOut bytes.Buffer
			err := run(t.Context(), tt.args, mapEnv(map[string]string{
				"DREM_ORCH_URL": ts.URL,
				"DREM_PROJECT":  "canvas",
				"DREM_ACTOR":    "codex:test-thread",
			}), &out, &errOut)
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			wantGets := 2
			if tt.name == "pass" || tt.name == "fail" {
				wantGets = 1
			}
			if len(gets) != wantGets {
				t.Fatalf("got %d task GETs, want %d", len(gets), wantGets)
			}
			if len(posts) != 1 {
				t.Fatalf("got %d POSTs, want 1", len(posts))
			}
			if posts[0].Path != tt.wantPath {
				t.Fatalf("POST path = %s, want %s", posts[0].Path, tt.wantPath)
			}
			if strings.TrimSpace(posts[0].Body) != tt.wantBody {
				t.Fatalf("POST body = %q, want %q", strings.TrimSpace(posts[0].Body), tt.wantBody)
			}
			if tt.name == "retry explicit key" && posts[0].IdempotencyKey != "retry-after-upgrade" {
				t.Fatalf("Idempotency-Key = %q, want retry-after-upgrade", posts[0].IdempotencyKey)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Fatalf("mutation output %q missing %q", out.String(), tt.wantOut)
			}
		})
	}
}

func TestFullUUIDMutationSkipsPrefixResolution(t *testing.T) {
	var got []recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, recordRequest(t, r))
		writeJSONResponse(t, w, map[string]any{"id": testTaskID, "title": "A", "status": "in_progress", "state_version": 1, "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")})
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"approve", testTaskID}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
		"DREM_ACTOR":    "codex:test-thread",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if len(got) != 2 || got[0].Method != http.MethodGet || got[0].Path != "/projects/canvas/tasks/"+testTaskID || got[1].Method != http.MethodPost {
		t.Fatalf("full UUID should skip list resolution but fetch guarded state, got %#v", got)
	}
}

func TestCreateTaskCommandsPostExpectedBodyAndRenderMutationOutput(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "create", args: []string{"create", "--title", "New task", "--description", "Do the work"}},
		{name: "create-task", args: []string{"create-task", "--title", "New task", "--description", "Do the work"}},
		{name: "file-task", args: []string{"file-task", "--title", "New task", "--description", "Do the work"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []recordedRequest
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = append(got, recordRequest(t, r))
				if r.Method != http.MethodPost || r.URL.Path != "/projects/canvas/tasks" {
					http.NotFound(w, r)
					return
				}
				writeJSONResponse(t, w, map[string]any{"id": testTaskID, "title": "New task", "status": "backlog", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")})
			}))
			defer ts.Close()

			var out, errOut bytes.Buffer
			err := run(t.Context(), tt.args, mapEnv(map[string]string{
				"DREM_ORCH_URL": ts.URL,
				"DREM_PROJECT":  "canvas",
				"DREM_ACTOR":    "codex:test-thread",
			}), &out, &errOut)
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d requests, want 1", len(got))
			}
			if got[0].Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", got[0].Method)
			}
			if got[0].Path != "/projects/canvas/tasks" {
				t.Fatalf("path = %s, want /projects/canvas/tasks", got[0].Path)
			}
			if strings.TrimSpace(got[0].Body) != `{"title":"New task","description":"Do the work"}` {
				t.Fatalf("body = %q", strings.TrimSpace(got[0].Body))
			}
			if !strings.Contains(out.String(), "task 12345678 -> backlog") {
				t.Fatalf("unexpected output: %q", out.String())
			}
		})
	}
}

func TestCreateTaskRequiresTitleAndDescriptionBeforeNetwork(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "title", args: []string{"create", "--description", "Do the work"}, wantErr: "--title is required"},
		{name: "title alias", args: []string{"create-task", "--description", "Do the work"}, wantErr: "--title is required"},
		{name: "description", args: []string{"file-task", "--title", "New task"}, wantErr: "--description is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				http.NotFound(w, r)
			}))
			defer ts.Close()

			var out, errOut bytes.Buffer
			err := run(t.Context(), tt.args, mapEnv(map[string]string{
				"DREM_ORCH_URL": ts.URL,
				"DREM_PROJECT":  "canvas",
			}), &out, &errOut)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected %q error, got %v", tt.wantErr, err)
			}
			if called {
				t.Fatal("server was called despite missing create-task input")
			}
		})
	}
}

func TestCreateTaskSpecReadsFileAndPostsTypedBody(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "cubase-task.json")
	spec := orchdto.TaskSpecDTO{
		Title:          "Observed Cubase task",
		Description:    "Match the reference behavior.",
		Actor:          "codex:test",
		IdempotencyKey: "cubase-observation-1",
		Observation: &orchdto.ReferenceObservationDTO{
			SessionID:          "session-1",
			Product:            "Cubase Pro",
			ProductVersion:     "15",
			OS:                 "Windows 11",
			DisplayEnvironment: "1920x1080",
			ObservedAt:         time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
			ObserverActor:      "codex:computer-use:test",
			Preconditions:      []string{"Project open"},
			Steps:              []orchdto.ReferenceWorkflowStepDTO{{Action: "Click", ExpectedVisibleResult: "Panel opens"}},
			ExpectedBehavior:   []string{"Panel opens"},
			NegativeBehavior:   []string{"Project remains unchanged"},
			Evidence: []orchdto.ObservationEvidenceDTO{{
				ArtifactID: "capture-1",
				SHA256:     strings.Repeat("a", 64),
				MediaType:  "image/png",
				Purpose:    "Visible result",
			}},
		},
		AcceptanceCriteria: []orchdto.TaskAcceptanceCriterionDTO{{
			ID:                "panel-opens",
			Description:       "The panel opens.",
			VerificationSteps: []string{"Click control"},
			ExpectedBehavior:  []string{"Panel visible"},
		}},
		ProposedScope: []string{"panel"},
		Exclusions:    []string{"persistence"},
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(specPath, raw, 0o600))

	var got recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = recordRequest(t, r)
		writeJSONResponse(t, w, map[string]any{"id": testTaskID, "title": spec.Title, "status": "classifying", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:01:00Z")})
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err = run(t.Context(), []string{"--actor", "codex:test", "create", "--spec", specPath}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	require.NoError(t, err)
	require.Equal(t, "codex:test", got.Actor)
	var posted orchdto.TaskSpecDTO
	require.NoError(t, json.Unmarshal([]byte(got.Body), &posted))
	require.Equal(t, spec.IdempotencyKey, posted.IdempotencyKey)
	require.Contains(t, out.String(), "task 12345678 -> classifying")
}

func TestCreateTaskSpecRejectsUnknownFieldBeforeNetwork(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()
	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"create", "--spec", `{"title":"x","unknown":true}`}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	require.ErrorContains(t, err, "unknown field")
	require.False(t, called)
}

func TestKyleRecoverDryRunClassifiesTestingReadyBlockers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{
				"id":                     testTaskID,
				"title":                  "A",
				"status":                 "testing_ready",
				"created_at":             rfc3339("2026-04-24T10:00:00Z"),
				"updated_at":             rfc3339("2026-04-24T10:01:00Z"),
				"latest_failure_type":    "build_error",
				"latest_failure_summary": "tooling timeout while running gate",
			}})
		case "/events":
			writeJSONResponse(t, w, []map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"kyle", "recover", "--dry-run"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{"TASK ID", "testing_ready", "tooling timeout", "infra_tooling_gate_failure", retryPolicyRule, recoveryRetryAction, "supported"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output %q missing %q", out.String(), want)
		}
	}
}

func TestKyleRecoverDryRunRepresentsBreakGlassSelfConfirmationWithoutExecution(t *testing.T) {
	missionFile := t.TempDir() + "/mission.md"
	if err := os.WriteFile(missionFile, []byte("keep the orchestrator pipeline unblocked"), 0o600); err != nil {
		t.Fatalf("write mission file: %v", err)
	}
	calledPost := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{
				"id":                     testTaskID,
				"title":                  "A",
				"status":                 "testing_ready",
				"created_at":             rfc3339("2026-04-24T10:00:00Z"),
				"updated_at":             rfc3339("2026-04-24T10:01:00Z"),
				"latest_failure_type":    "state_repair",
				"latest_failure_summary": "supported surface unavailable for task state repair; direct DB would unblock",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/events":
			writeJSONResponse(t, w, []map[string]any{})
		case r.Method == http.MethodPost:
			calledPost = true
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"kyle", "recover", "--mission-file", missionFile, "--dry-run"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if calledPost {
		t.Fatal("dry-run posted for break-glass decision")
	}
	for _, want := range []string{"keep the orchestrator pipeline unblocked", "supported_surface_unavailable", breakGlassPolicy, "break-glass", "self-confirmed by policy", "unsupported DB/Docker/host execution is not implemented"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output %q missing %q", out.String(), want)
		}
	}
}

func TestKyleRecoverApplyDoesNotExecuteBreakGlassActions(t *testing.T) {
	missionFile := t.TempDir() + "/mission.md"
	if err := os.WriteFile(missionFile, []byte("keep the orchestrator pipeline unblocked"), 0o600); err != nil {
		t.Fatalf("write mission file: %v", err)
	}
	calledPost := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{
				"id":                     testTaskID,
				"title":                  "A",
				"status":                 "testing_ready",
				"created_at":             rfc3339("2026-04-24T10:00:00Z"),
				"updated_at":             rfc3339("2026-04-24T10:01:00Z"),
				"latest_failure_summary": "supported surface unavailable for docker --no-deps repair",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/events":
			writeJSONResponse(t, w, []map[string]any{})
		case r.Method == http.MethodPost:
			calledPost = true
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"kyle", "recover", "--mission-file", missionFile, "--apply"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if calledPost {
		t.Fatal("apply posted despite break-glass action being execution-disabled")
	}
	if !strings.Contains(out.String(), "no allowlisted supported recovery action") || !strings.Contains(out.String(), "break-glass") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestKyleRecoverApplyExecutesFirstSupportedRetryAndAudits(t *testing.T) {
	var posts []recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordRequest(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{
				"id":                     testTaskID,
				"title":                  "A",
				"status":                 "testing_ready",
				"created_at":             rfc3339("2026-04-24T10:00:00Z"),
				"updated_at":             rfc3339("2026-04-24T10:01:00Z"),
				"latest_failure_type":    "crash",
				"latest_failure_summary": "container runtime timeout",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/events":
			writeJSONResponse(t, w, []map[string]any{})
		case r.Method == http.MethodPost:
			posts = append(posts, rec)
			switch r.URL.Path {
			case "/projects/canvas/tasks/" + testTaskID + "/fail":
				writeJSONResponse(t, w, map[string]any{"id": testTaskID, "title": "A", "status": "in_progress", "created_at": rfc3339("2026-04-24T10:00:00Z"), "updated_at": rfc3339("2026-04-24T10:02:00Z")})
			case "/projects/canvas/tasks/" + testTaskID + "/comments":
				writeJSONResponse(t, w, map[string]any{"id": "87654321-1234-1234-1234-123456789abc", "task_id": testTaskID, "author": "csuite", "body": "audit", "created_at": rfc3339("2026-04-24T10:02:00Z")})
			case "/projects/canvas/tasks/" + testTaskID + "/audit-events":
				writeJSONResponse(t, w, map[string]any{"timestamp": rfc3339("2026-04-24T10:02:00Z"), "type": recoveryAuditType, "payload": map[string]any{"task_id": testTaskID}})
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"kyle", "recover", "--apply"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	wantPaths := []string{
		"/projects/canvas/tasks/" + testTaskID + "/fail",
		"/projects/canvas/tasks/" + testTaskID + "/comments",
		"/projects/canvas/tasks/" + testTaskID + "/audit-events",
	}
	if len(posts) != len(wantPaths) {
		t.Fatalf("got %d POSTs, want %d: %#v", len(posts), len(wantPaths), posts)
	}
	for i, want := range wantPaths {
		if posts[i].Path != want {
			t.Fatalf("POST %d path = %s, want %s", i, posts[i].Path, want)
		}
	}
	if !strings.Contains(posts[2].Body, `"actor":"kyle"`) || !strings.Contains(posts[2].Body, retryPolicyRule) || !strings.Contains(posts[2].Body, `"supported_path":true`) {
		t.Fatalf("audit body missing required fields: %s", posts[2].Body)
	}
	if !strings.Contains(out.String(), "apply: task transitioned to in_progress") {
		t.Fatalf("unexpected apply output: %q", out.String())
	}
}

func TestKyleRecoverApplyHonorsRetryBudget(t *testing.T) {
	calledPost := false
	eventsQuery := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{
				"id":                     testTaskID,
				"title":                  "A",
				"status":                 "testing_ready",
				"created_at":             rfc3339("2026-04-24T10:00:00Z"),
				"updated_at":             rfc3339("2026-04-24T10:01:00Z"),
				"latest_failure_type":    "crash",
				"latest_failure_summary": "tooling timeout",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/events":
			eventsQuery = r.URL.RawQuery
			writeJSONResponse(t, w, []map[string]any{{
				"timestamp": rfc3339("2026-04-24T10:02:00Z"),
				"type":      recoveryAuditType,
				"payload": map[string]any{
					"task_id": testTaskID,
					"details": map[string]any{"policy_rule": retryPolicyRule},
				},
			}})
		case r.Method == http.MethodPost:
			calledPost = true
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"kyle", "recover", "--apply"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if calledPost {
		t.Fatal("apply posted despite exhausted retry budget")
	}
	if eventsQuery != "limit=500" {
		t.Fatalf("retry budget should use the existing bounded /events window, got query %q", eventsQuery)
	}
	if !strings.Contains(out.String(), "retry budget exhausted") || !strings.Contains(out.String(), "no allowlisted supported recovery action") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestKyleRecoverApplyPreventsEscalationOnlyActions(t *testing.T) {
	calledPost := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/canvas/tasks":
			writeJSONResponse(t, w, []map[string]any{{
				"id":                     testTaskID,
				"title":                  "A",
				"status":                 "testing_ready",
				"created_at":             rfc3339("2026-04-24T10:00:00Z"),
				"updated_at":             rfc3339("2026-04-24T10:01:00Z"),
				"latest_failure_type":    "test_failure",
				"latest_failure_summary": "needs secret token to continue",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/events":
			writeJSONResponse(t, w, []map[string]any{})
		case r.Method == http.MethodPost:
			calledPost = true
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"kyle", "recover", "--apply"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if calledPost {
		t.Fatal("apply posted despite escalation-only classification")
	}
	if !strings.Contains(out.String(), "escalation_only") || !strings.Contains(out.String(), "escalation-only action class") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestAnswerRequiresBodyBeforeNetwork(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.NotFound(w, r)
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"answer", "12345678"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "--body is required") {
		t.Fatalf("expected body error, got %v", err)
	}
	if called {
		t.Fatal("server was called despite missing --body")
	}
}

func TestArchiveRequiresReasonBeforeNetwork(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.NotFound(w, r)
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"archive", "12345678"}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL,
		"DREM_PROJECT":  "canvas",
	}), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Fatalf("expected reason error, got %v", err)
	}
	if called {
		t.Fatal("server was called despite missing --reason")
	}
}

func TestHTTPErrorRenderingIncludesStatusBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "orch unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"projects"}, mapEnv(map[string]string{"DREM_ORCH_URL": ts.URL}), &out, &errOut)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "orch unavailable") {
		t.Fatalf("error did not include status/body: %v", err)
	}
}

func TestImportDiscipline(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{range .Imports}}{{.}}{{"\n"}}{{end}}`, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	forbidden := []string{
		"github.com/godinj/drem-orchestrator/internal/",
		"github.com/godinj/drem-orchestrator/cmd/drem",
		"gorm.io/",
		"github.com/mattn/go-sqlite3",
	}
	for _, imp := range strings.Fields(string(out)) {
		for _, prefix := range forbidden {
			if strings.HasPrefix(imp, prefix) {
				t.Fatalf("forbidden import %q", imp)
			}
		}
	}
}

func recordRequest(t *testing.T, r *http.Request) recordedRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return recordedRequest{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body),
		Authorization: r.Header.Get("Authorization"), Actor: r.Header.Get("X-Drem-Actor"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func mapEnv(values map[string]string) envLookup {
	return func(key string) string {
		if values == nil {
			return ""
		}
		return values[key]
	}
}

func rfc3339(raw string) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		panic(err)
	}
	return t
}

func taskResponse(id, status string) map[string]any {
	return map[string]any{
		"id":              id,
		"title":           "A",
		"status":          status,
		"created_at":      rfc3339("2026-04-24T10:00:00Z"),
		"updated_at":      rfc3339("2026-04-24T10:01:00Z"),
		"assigned_worker": "",
	}
}
