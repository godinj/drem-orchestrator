package orchhttp_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/orchhttp"
)

// TestRequestLogIsSampledUnderFloodProof is the W4.1 apply-site
// regression. It points slog.Default at an in-memory buffer, fires 200
// requests through the orchhttp request-log wrapper, and asserts that
// the buffer contains far fewer than 200 "orchhttp request" lines. The
// 2026-04-21 incident emitted 1406 such lines per 200 KB of drem.log
// tail at 495 req/s; with the sampler applied the log volume should
// fall by at least 10x for a single site.
func TestRequestLogIsSampledUnderFloodProof(t *testing.T) {
	// Route slog output into a buffer we can inspect.
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))

	// A trivial 200-OK handler is enough — we care about request-log
	// volume, not handler logic.
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := orchhttp.LogRequestsForTest(ok)

	const fires = 200
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	for i := 0; i < fires; i++ {
		resp, err := http.Get(srv.URL + "/projects/test/tasks")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_ = resp.Body.Close()
	}

	got := strings.Count(buf.String(), "orchhttp request")
	// Sampler is EveryN(32) per the wiring — 200 / 32 = ~7 emissions.
	// Assert "far fewer than unsampled", not an exact count, so the
	// production knob can be tuned without test churn.
	if got >= fires/4 {
		t.Fatalf("request log was not sampled: got %d emissions for %d fires (want <= %d)\nbuffer sample:\n%s",
			got, fires, fires/4, firstLines(buf.String(), 3))
	}
	if got == 0 {
		t.Fatalf("request log was fully suppressed: expected at least one sampled emission out of %d fires", fires)
	}
}

// firstLines returns the first n lines of s for diagnostic output in
// failure messages, keeping the test log readable when the buffer is
// huge.
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
