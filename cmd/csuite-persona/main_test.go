package main

// main_test.go exercises the thin flag/env wiring in main.go. The
// heart of the poller is covered in internal/csuite/persona; these
// tests assert only the subprocess-entry-point behaviours:
//
//   - Scoreboard item 33 fail-fast: CSUITE_SIGNAL_ENDPOINT set with
//     empty CSUITE_WATCHER_TOKEN exits non-zero with a diagnostic
//     on stderr rather than proceeding into a 401-loop.

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// envSnapshot is a minimal helper to save/restore env vars touched by
// tests. Avoids t.Setenv's goroutine-hostile restore so the tests can
// safely run with -race.
func envSnapshot(keys ...string) func() {
	saved := make(map[string]string, len(keys))
	unset := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		if ok {
			saved[k] = v
		} else {
			unset[k] = struct{}{}
		}
	}
	return func() {
		for k, v := range saved {
			_ = os.Setenv(k, v)
		}
		for k := range unset {
			_ = os.Unsetenv(k)
		}
	}
}

// TestRun_FailsFastWhenEndpointSetButTokenEmpty is the regression proof
// for scoreboard item 33: before this check, the persona binary booted
// cleanly with an empty CSUITE_WATCHER_TOKEN and every subsequent
// /deliver POST returned 401. Now the binary exits non-zero at
// startup with a diagnostic on stderr so the operator sees the
// misconfiguration in `docker logs` immediately.
func TestRun_FailsFastWhenEndpointSetButTokenEmpty(t *testing.T) {
	restore := envSnapshot("CSUITE_SIGNAL_ENDPOINT", "CSUITE_WATCHER_TOKEN")
	defer restore()

	if err := os.Setenv("CSUITE_SIGNAL_ENDPOINT", "http://csuite-watcher:8090/deliver"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	if err := os.Unsetenv("CSUITE_WATCHER_TOKEN"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	// stdout + stderr go to pipes so we can assert on the stderr
	// diagnostic. run() writes to the supplied *os.File — we use
	// os.Pipe to capture.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	defer stdoutR.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	defer stderrR.Close()

	code := run([]string{"-persona", "seth"}, stdoutW, stderrW)
	stdoutW.Close()
	stderrW.Close()

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (fail-fast on empty token)", code)
	}

	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(stderrR)
	body := stderrBuf.String()

	wantNeedles := []string{
		"CSUITE_WATCHER_TOKEN is empty",
		"export CSUITE_WATCHER_TOKEN",
		"watcher will reject every signal with 401",
	}
	for _, n := range wantNeedles {
		if !strings.Contains(body, n) {
			t.Errorf("stderr missing %q in %q", n, body)
		}
	}
}

// TestRun_TokenFileFallback verifies that when CSUITE_WATCHER_TOKEN
// is empty but CSUITE_WATCHER_TOKEN_FILE points at a readable file,
// the persona binary reads the token from disk rather than failing
// fast with the item-33 diagnostic. This is the file-mount mitigation
// path (compose bind-mounts the operator's token file into the
// container at a known path instead of depending on host shell env).
func TestRun_TokenFileFallback(t *testing.T) {
	restore := envSnapshot(
		"CSUITE_SIGNAL_ENDPOINT", "CSUITE_WATCHER_TOKEN", "CSUITE_WATCHER_TOKEN_FILE")
	defer restore()

	tokenPath := t.TempDir() + "/tok"
	if err := os.WriteFile(tokenPath, []byte("abc123\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	_ = os.Setenv("CSUITE_SIGNAL_ENDPOINT", "http://csuite-watcher:8090/deliver")
	_ = os.Unsetenv("CSUITE_WATCHER_TOKEN")
	_ = os.Setenv("CSUITE_WATCHER_TOKEN_FILE", tokenPath)

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	defer stdoutR.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	defer stderrR.Close()

	// Will fail on missing inbox dir but must NOT hit the item-33 diagnostic.
	_ = run([]string{"-persona", "seth", "-inbox-dir", "/nonexistent"}, stdoutW, stderrW)
	stdoutW.Close()
	stderrW.Close()

	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(stderrR)
	body := stderrBuf.String()

	if strings.Contains(body, "CSUITE_WATCHER_TOKEN is empty") {
		t.Errorf("token file fallback should suppress item-33 diagnostic; stderr=%q", body)
	}
}

// TestRun_TokenFileUnreadable verifies the explicit error when the
// token-file env var points at an unreadable path.
func TestRun_TokenFileUnreadable(t *testing.T) {
	restore := envSnapshot(
		"CSUITE_SIGNAL_ENDPOINT", "CSUITE_WATCHER_TOKEN", "CSUITE_WATCHER_TOKEN_FILE")
	defer restore()

	_ = os.Setenv("CSUITE_SIGNAL_ENDPOINT", "http://csuite-watcher:8090/deliver")
	_ = os.Unsetenv("CSUITE_WATCHER_TOKEN")
	_ = os.Setenv("CSUITE_WATCHER_TOKEN_FILE", "/nonexistent/path/tok")

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer stdoutR.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer stderrR.Close()

	code := run([]string{"-persona", "seth"}, stdoutW, stderrW)
	stdoutW.Close()
	stderrW.Close()

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}

	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(stderrR)
	body := stderrBuf.String()
	if !strings.Contains(body, "CSUITE_WATCHER_TOKEN_FILE") {
		t.Errorf("stderr missing token-file diagnostic: %q", body)
	}
}

// TestRun_AllowsEmptyBothForDisabledSignaling confirms the
// null-signaling configuration still works: BOTH endpoint and token
// empty means signaling is disabled and the binary is allowed to
// proceed into the normal poller loop (which fails validation here
// because the inbox dir doesn't exist — that's a separate error, but
// it is NOT the item-33 diagnostic).
func TestRun_AllowsEmptyBothForDisabledSignaling(t *testing.T) {
	restore := envSnapshot("CSUITE_SIGNAL_ENDPOINT", "CSUITE_WATCHER_TOKEN")
	defer restore()
	_ = os.Unsetenv("CSUITE_SIGNAL_ENDPOINT")
	_ = os.Unsetenv("CSUITE_WATCHER_TOKEN")

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	defer stdoutR.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	defer stderrR.Close()

	// Will fail due to missing inbox dir (not installed), but must
	// NOT emit the item-33 diagnostic.
	_ = run([]string{"-persona", "seth", "-inbox-dir", "/nonexistent"}, stdoutW, stderrW)
	stdoutW.Close()
	stderrW.Close()

	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(stderrR)
	body := stderrBuf.String()

	if strings.Contains(body, "CSUITE_WATCHER_TOKEN is empty") {
		t.Errorf("item-33 diagnostic should NOT fire when both endpoint and token are empty; stderr=%q", body)
	}
}
