package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/merger"
)

func TestHTTPReporterUsesAgentmonTokenHeader(t *testing.T) {
	var gotHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Drem-Agentmon-Token")
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Fatalf("unexpected Authorization header: %q", auth)
		}
		if r.URL.Path != "/internal/logs" {
			t.Fatalf("path = %q, want /internal/logs", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	reporter := newHTTPReporter(ts.URL, "token-123", slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := reporter.Report(context.Background(), "project-1", "task-1", merger.MergeResult{Success: true})
	if err != nil {
		t.Fatalf("Report returned error: %v", err)
	}
	if gotHeader != "token-123" {
		t.Fatalf("X-Drem-Agentmon-Token = %q, want token-123", gotHeader)
	}
}
