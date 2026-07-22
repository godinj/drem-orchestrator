package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/merger"
)

func TestParseFlagsRequiresFullAuthorizedSHAs(t *testing.T) {
	base := []string{
		"--feature-branch", "feature/x", "--project", "p", "--task-id", "t",
		"--test-cmd", "true", "--orch-url", "http://orch", "--agentmon-token", "tok",
	}
	_, err := parseFlags(base)
	if err == nil || !strings.Contains(err.Error(), "--expected-feature-sha") {
		t.Fatalf("missing SHA flags error = %v", err)
	}

	valid := append(append([]string{}, base...),
		"--expected-feature-sha", strings.Repeat("a", 40),
		"--expected-base-sha", strings.Repeat("b", 40),
	)
	if _, err := parseFlags(valid); err != nil {
		t.Fatalf("valid exact SHA flags rejected: %v", err)
	}
	withoutTelemetry := []string{
		"--feature-branch", "feature/x", "--project", "p", "--task-id", "t", "--test-cmd", "true",
		"--expected-feature-sha", strings.Repeat("a", 40), "--expected-base-sha", strings.Repeat("b", 40),
	}
	if _, err := parseFlags(withoutTelemetry); err != nil {
		t.Fatalf("telemetry-free merge flags rejected: %v", err)
	}
	if _, err := parseFlags(append(withoutTelemetry, "--orch-url", "http://orch")); err == nil {
		t.Fatal("one-sided telemetry configuration unexpectedly accepted")
	}

	invalid := append(append([]string{}, base...),
		"--expected-feature-sha", "short",
		"--expected-base-sha", strings.Repeat("b", 40),
	)
	if _, err := parseFlags(invalid); err == nil {
		t.Fatal("abbreviated SHA unexpectedly accepted")
	}
}

func TestHTTPReporterUsesAgentmonTokenHeader(t *testing.T) {
	var gotHeader string
	var gotBody map[string][]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Drem-Agentmon-Token")
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Fatalf("unexpected Authorization header: %q", auth)
		}
		if r.URL.Path != "/internal/logs" {
			t.Fatalf("path = %q, want /internal/logs", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("DREM_WORKER_ID", "merger-worker-1")
	reporter := newHTTPReporter(ts.URL, "token-123", slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := reporter.Report(context.Background(), "project-1", "task-1", merger.MergeResult{Success: true})
	if err != nil {
		t.Fatalf("Report returned error: %v", err)
	}
	if gotHeader != "token-123" {
		t.Fatalf("X-Drem-Agentmon-Token = %q, want token-123", gotHeader)
	}
	if len(gotBody["records"]) != 1 {
		t.Fatalf("records len = %d, want 1", len(gotBody["records"]))
	}
	var rec map[string]any
	if err := json.Unmarshal(gotBody["records"][0], &rec); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if rec["type"] != "merge_result" {
		t.Fatalf("record type = %v, want merge_result", rec["type"])
	}
	if rec["worker_id"] != "merger-worker-1" {
		t.Fatalf("worker_id = %v, want merger-worker-1", rec["worker_id"])
	}
	if rec["container_id"] == "" {
		t.Fatal("container_id was empty")
	}
}
