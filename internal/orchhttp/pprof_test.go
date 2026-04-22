package orchhttp_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/orchhttp"
)

// TestPprofDisabledWhenEnvUnset asserts that StartPprofListener is a
// no-op when DREM_PPROF is empty, unset, or "0". A pprof listener
// running by default would expose runtime internals on any box that
// pulled the orch image, so the env gate is load-bearing.
func TestPprofDisabledWhenEnvUnset(t *testing.T) {
	for _, v := range []string{"", "0"} {
		t.Run("DREM_PPROF="+v, func(t *testing.T) {
			if v == "" {
				// Explicitly ensure unset.
				t.Setenv("DREM_PPROF", "")
			} else {
				t.Setenv("DREM_PPROF", v)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			addr, stop, err := orchhttp.StartPprofListener(ctx)
			if err != nil {
				t.Fatalf("StartPprofListener: %v", err)
			}
			t.Cleanup(func() { _ = stop(context.Background()) })
			if addr != "" {
				t.Fatalf("expected empty addr when DREM_PPROF unset; got %q", addr)
			}
		})
	}
}

// TestPprofEnabledReturns200 asserts that with DREM_PPROF=1 the listener
// binds to localhost and serves the standard /debug/pprof index page.
// The port is ephemeral (":0") under test to avoid colliding with
// production 6060.
func TestPprofEnabledReturns200(t *testing.T) {
	t.Setenv("DREM_PPROF", "1")
	t.Setenv("DREM_PPROF_ADDR", "127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	addr, stop, err := orchhttp.StartPprofListener(ctx)
	if err != nil {
		t.Fatalf("StartPprofListener: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	if addr == "" {
		t.Fatal("expected non-empty addr when DREM_PPROF=1")
	}

	// Give the listener a breath to start accepting.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200\nbody:\n%s", resp.StatusCode, body)
	}
}

// TestPprofBindsLocalhostOnly asserts that StartPprofListener refuses
// to bind anything other than a loopback address. The plan explicitly
// restricts the pprof surface to 127.0.0.1 — a 0.0.0.0 bind would leak
// runtime profiles to anything that can reach the container port.
func TestPprofBindsLocalhostOnly(t *testing.T) {
	t.Setenv("DREM_PPROF", "1")
	t.Setenv("DREM_PPROF_ADDR", "0.0.0.0:0")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, stop, err := orchhttp.StartPprofListener(ctx)
	if stop != nil {
		t.Cleanup(func() { _ = stop(context.Background()) })
	}
	if err == nil {
		t.Fatal("expected error binding pprof to 0.0.0.0; got nil")
	}
}
