package agent

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndpointHealthChecker_IsHealthy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("Initially healthy", func(t *testing.T) {
		h := NewEndpointHealthChecker("http://localhost/v1/chat/completions", logger)
		if !h.IsHealthy() {
			t.Error("Expected initially healthy status")
		}
	})

	t.Run("Circuit opens on failure and respects cooldown", func(t *testing.T) {
		h := NewEndpointHealthChecker("http://localhost/v1/chat/completions", logger)
		h.baseCooldown = 100 * time.Millisecond
		h.maxCooldown = 1 * time.Second

		h.RecordFailure()

		if h.IsHealthy() {
			t.Error("Expected unhealthy status after failure")
		}

		// Check if it stays unhealthy during cooldown
		// We don't call IsHealthy() again because it might trigger a probe if we aren't careful,
		// but here we just want to check the state.
		h.mu.Lock()
		if h.state != 1 {
			t.Error("Expected state to be open (1)")
		}
		h.mu.Unlock()

		// Wait for cooldown to expire
		time.Sleep(150 * time.Millisecond)

		// Now it should attempt a probe. We need a server to respond.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		// Update probe URL to point to test server
		h.probeURL = server.URL + "/v1/models"

		if !h.IsHealthy() {
			t.Error("Expected healthy status after cooldown and successful probe")
		}
		if h.state != 0 {
			t.Errorf("Expected state to be closed (0), got %d", h.state)
		}
	})

	t.Run("Exponential backoff escalation", func(t *testing.T) {
		h := NewEndpointHealthChecker("http://localhost/v1/chat/completions", logger)
		h.baseCooldown = 100 * time.Millisecond
		h.maxCooldown = 1 * time.Second

		// First failure: cooldown = 100ms
		h.RecordFailure()
		if h.cooldown != 100*time.Millisecond {
			t.Errorf("Expected 100ms cooldown, got %v", h.cooldown)
		}

		// Second failure: cooldown = 100ms * 2 = 200ms
		h.RecordFailure()
		if h.cooldown != 200*time.Millisecond {
			t.Errorf("Expected 200ms cooldown, got %v", h.cooldown)
		}

		// Third failure: cooldown = 200ms * 2 = 400ms
		h.RecordFailure()
		if h.cooldown != 400*time.Millisecond {
			t.Errorf("Expected 400ms cooldown, got %v", h.cooldown)
		}

		// Fourth failure: cooldown = 400ms * 2 = 800ms
		h.RecordFailure()
		if h.cooldown != 800*time.Millisecond {
			t.Errorf("Expected 800ms cooldown, got %v", h.cooldown)
		}

		// Fifth failure: cooldown = 800ms * 2 = 1600ms -> capped at maxCooldown (1s)
		h.RecordFailure()
		if h.cooldown != 1*time.Second {
			t.Errorf("Expected 1s cooldown (capped), got %v", h.cooldown)
		}
	})
}

func TestEndpointHealthChecker_RecordSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("Resets state on success", func(t *testing.T) {
		h := NewEndpointHealthChecker("http://localhost/v1/chat/completions", logger)
		h.RecordFailure()
		if h.state != 1 {
			t.Fatal("Expected state to be open (1)")
		}

		h.RecordSuccess()
		if h.state != 0 {
			t.Errorf("Expected state to be closed (0) after success, got %d", h.state)
		}
		if h.consecutives != 0 {
			t.Errorf("Expected consecutive failures to be 0, got %d", h.consecutives)
		}
	})
}

func TestEndpointHealthChecker_Status(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("Healthy status", func(t *testing.T) {
		h := NewEndpointHealthChecker("http://localhost/v1/chat/completions", logger)
		status := h.Status()
		if status != "healthy" {
			t.Errorf("Expected 'healthy', got '%s'", status)
		}
	})

	t.Run("Unhealthy status with details", func(t *testing.T) {
		h := NewEndpointHealthChecker("http://localhost/v1/chat/completions", logger)
		h.baseCooldown = 10 * time.Second
		h.RecordFailure()

		status := h.Status()
		// Check if status contains "unhealthy" and "consecutive failures"
		if !strings.Contains(status, "unhealthy") || !strings.Contains(status, "1 consecutive failures") {
			t.Errorf("Unexpected status string: %s", status)
		}
	})
}
