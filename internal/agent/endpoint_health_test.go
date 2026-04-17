package agent

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestChecker(serverURL string) *EndpointHealthChecker {
	h := NewEndpointHealthChecker(serverURL+"/v1/chat/completions", testLogger())
	h.httpClient = &http.Client{Timeout: 2 * time.Second}
	return h
}

func TestEndpointHealthChecker_HealthyEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newTestChecker(srv.URL)
	if !h.IsHealthy() {
		t.Error("healthy endpoint should return true")
	}
}

func TestEndpointHealthChecker_ClosedCircuitTrustsRecordSuccess(t *testing.T) {
	// A fresh (closed) checker returns true without probing — health is
	// discovered via actual API calls and RecordFailure/RecordSuccess.
	h := NewEndpointHealthChecker("http://localhost:1/v1/chat/completions", testLogger())
	if !h.IsHealthy() {
		t.Error("closed circuit should return true without probing")
	}
}

func TestEndpointHealthChecker_RecordFailureOpensCircuit(t *testing.T) {
	h := NewEndpointHealthChecker("http://localhost:1/v1/chat/completions", testLogger())
	h.RecordFailure()
	if h.IsHealthy() {
		t.Error("after RecordFailure, IsHealthy should return false")
	}
}

func TestEndpointHealthChecker_CircuitOpensOnRecordFailure(t *testing.T) {
	var probeCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&probeCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newTestChecker(srv.URL)
	h.RecordFailure()

	// Reset probe counter after RecordFailure.
	atomic.StoreInt64(&probeCount, 0)

	// IsHealthy should return false without probing (within cooldown).
	if h.IsHealthy() {
		t.Error("open circuit within cooldown should return false")
	}
	if atomic.LoadInt64(&probeCount) != 0 {
		t.Error("no HTTP probe should occur while circuit is open within cooldown")
	}
}

func TestEndpointHealthChecker_RecoverAfterCooldown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newTestChecker(srv.URL)
	h.baseCooldown = 10 * time.Millisecond
	h.maxCooldown = 100 * time.Millisecond

	h.RecordFailure()
	time.Sleep(50 * time.Millisecond)

	if !h.IsHealthy() {
		t.Error("after cooldown, healthy endpoint should return true (half-open → closed)")
	}
	if h.state != 0 {
		t.Error("circuit should be closed after successful recovery probe")
	}
}

func TestEndpointHealthChecker_ExponentialBackoff(t *testing.T) {
	h := NewEndpointHealthChecker("http://localhost:1/v1/chat/completions", testLogger())
	h.baseCooldown = 100 * time.Millisecond
	h.maxCooldown = 1 * time.Second

	h.RecordFailure()
	if h.cooldown != 100*time.Millisecond {
		t.Errorf("first failure cooldown: want 100ms, got %v", h.cooldown)
	}
	h.RecordFailure()
	if h.cooldown != 200*time.Millisecond {
		t.Errorf("second failure cooldown: want 200ms, got %v", h.cooldown)
	}
	h.RecordFailure()
	if h.cooldown != 400*time.Millisecond {
		t.Errorf("third failure cooldown: want 400ms, got %v", h.cooldown)
	}
	h.RecordFailure()
	if h.cooldown != 800*time.Millisecond {
		t.Errorf("fourth failure cooldown: want 800ms, got %v", h.cooldown)
	}
	h.RecordFailure()
	if h.cooldown != 1*time.Second {
		t.Errorf("fifth failure should cap at maxCooldown, got %v", h.cooldown)
	}
}

func TestEndpointHealthChecker_RecordSuccessResetsCircuit(t *testing.T) {
	h := NewEndpointHealthChecker("http://localhost:1/v1/chat/completions", testLogger())
	h.RecordFailure()
	h.RecordFailure()
	h.RecordFailure()

	if h.state != 1 {
		t.Fatal("circuit should be open after failures")
	}

	h.RecordSuccess()
	if h.state != 0 {
		t.Error("circuit should be closed after RecordSuccess")
	}
	if h.consecutives != 0 {
		t.Error("consecutive count should be reset after RecordSuccess")
	}
	if h.cooldown != h.baseCooldown {
		t.Error("cooldown should be reset to baseCooldown after RecordSuccess")
	}
}

func TestEndpointHealthChecker_ProbeURLDerived(t *testing.T) {
	h := NewEndpointHealthChecker("http://localhost:8081/v1/chat/completions", testLogger())
	if h.probeURL != "http://localhost:8081/v1/models" {
		t.Errorf("probe URL: want http://localhost:8081/v1/models, got %s", h.probeURL)
	}
}

func TestEndpointHealthChecker_Status(t *testing.T) {
	h := NewEndpointHealthChecker("http://localhost:1/v1/chat/completions", testLogger())

	if s := h.Status(); s != "healthy" {
		t.Errorf("fresh checker status: want 'healthy', got '%s'", s)
	}

	h.RecordFailure()
	s := h.Status()
	if s == "healthy" {
		t.Error("status should not be 'healthy' after failure")
	}
}
