package orchhttp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/orchhttp"
)

// TestGoroutineDumpOnSIGUSR1 is the W3.2 regression test. It installs
// the signal handler, fires SIGUSR1 at the current process, and
// asserts that a /tmp/drem-goroutines-<ts>.log-shaped file appears
// within 1 second. Zero-dep — the test uses syscall.Kill on the self
// PID rather than spawning a subprocess, so it works in the same
// container the plan targets.
func TestGoroutineDumpOnSIGUSR1(t *testing.T) {
	// Route dumps into a test-scoped directory so we do not collide
	// with any parallel tests or real /tmp state.
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := orchhttp.InstallGoroutineDumpHandler(ctx, dir); err != nil {
		t.Fatalf("InstallGoroutineDumpHandler: %v", err)
	}

	// Fire the signal at ourselves.
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("syscall.Kill SIGUSR1: %v", err)
	}

	// Wait up to 1 second for the dump file to land.
	deadline := time.Now().Add(1 * time.Second)
	var found string
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "drem-goroutines-") && strings.HasSuffix(e.Name(), ".log") {
					found = filepath.Join(dir, e.Name())
					break
				}
			}
		}
		if found != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found == "" {
		t.Fatal("no drem-goroutines-*.log file appeared within 1s of SIGUSR1")
	}

	body, err := os.ReadFile(found)
	if err != nil {
		t.Fatalf("read dump file: %v", err)
	}
	// Dumps produced by runtime.Stack(buf, true) always begin with a
	// 'goroutine' header for goroutine 1.  Assert that signature so
	// a partial write or empty-file bug would fail the test.
	if !strings.Contains(string(body), "goroutine ") {
		t.Fatalf("dump file missing 'goroutine ' header; contents:\n%s", string(body))
	}
}
