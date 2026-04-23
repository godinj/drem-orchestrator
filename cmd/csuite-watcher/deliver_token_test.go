package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServe_LoadsTokenFromFileWhenEnvEmpty verifies that with
// CSUITE_WATCHER_TOKEN unset and CSUITE_WATCHER_TOKEN_FILE pointed at
// a populated file, loadDeliverToken resolves to the trimmed file
// contents. This is the scoreboard item 33 watcher-side mitigation:
// the compose template's valueless CSUITE_WATCHER_TOKEN: form lands
// empty when the operator forgot to `export` before compose-up, and
// the file-fallback takes over so POST /deliver no longer 401s.
func TestServe_LoadsTokenFromFileWhenEnvEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "csuite-watcher-token")
	if err := os.WriteFile(path, []byte("top-secret-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	t.Setenv("CSUITE_WATCHER_TOKEN", "")
	t.Setenv("CSUITE_WATCHER_TOKEN_FILE", path)

	tok, err := loadDeliverToken()
	if err != nil {
		t.Fatalf("loadDeliverToken: %v", err)
	}
	if tok != "top-secret-token" {
		t.Errorf("tok = %q, want %q (trailing newline must be trimmed)", tok, "top-secret-token")
	}
}

// TestServe_ReturnsErrorWhenBothEmpty verifies that with neither
// CSUITE_WATCHER_TOKEN nor CSUITE_WATCHER_TOKEN_FILE set,
// loadDeliverToken fails closed with a clear error instead of
// silently returning "". The pre-fix behaviour was a silent accept
// that let the watcher boot and 401 every subsequent POST /deliver;
// explicit fail-closed here catches the misconfiguration at startup
// where the operator can see it in `docker logs`.
func TestServe_ReturnsErrorWhenBothEmpty(t *testing.T) {
	t.Setenv("CSUITE_WATCHER_TOKEN", "")
	t.Setenv("CSUITE_WATCHER_TOKEN_FILE", "")

	_, err := loadDeliverToken()
	if err == nil {
		t.Fatalf("expected error when both env vars are empty, got nil")
	}
	if !strings.Contains(err.Error(), "CSUITE_WATCHER_TOKEN") {
		t.Errorf("error should name the env var, got %q", err.Error())
	}
}

// TestServe_EnvTakesPrecedenceOverFile verifies that when both
// CSUITE_WATCHER_TOKEN and CSUITE_WATCHER_TOKEN_FILE are populated,
// the env value wins and the file is not consulted. This mirrors
// cmd/csuite-persona/main.go:81-90 precedence.
func TestServe_EnvTakesPrecedenceOverFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "csuite-watcher-token")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	t.Setenv("CSUITE_WATCHER_TOKEN", "from-env")
	t.Setenv("CSUITE_WATCHER_TOKEN_FILE", path)

	tok, err := loadDeliverToken()
	if err != nil {
		t.Fatalf("loadDeliverToken: %v", err)
	}
	if tok != "from-env" {
		t.Errorf("tok = %q, want %q (env must win over file)", tok, "from-env")
	}
}

// TestServe_LoadDeliverToken_FileMissingSurfacesError verifies that
// when CSUITE_WATCHER_TOKEN is empty and CSUITE_WATCHER_TOKEN_FILE
// points at a non-existent path, the ReadFile error is surfaced
// with the path in the message (not silently treated as "no file,
// so empty token"). Helps operators debug a misconfigured
// bind-mount.
func TestServe_LoadDeliverToken_FileMissingSurfacesError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	t.Setenv("CSUITE_WATCHER_TOKEN", "")
	t.Setenv("CSUITE_WATCHER_TOKEN_FILE", missing)

	_, err := loadDeliverToken()
	if err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error should mention the missing path %q, got %q", missing, err.Error())
	}
}

// TestServe_LoadDeliverToken_EmptyFileFailsClosed verifies that a
// file which exists but contains only whitespace resolves to an
// empty token and trips the fail-closed guard — the watcher must
// not boot with Token="" because the deliver handler uses
// constant-time equality that would match an empty X-Csuite-Token
// header.
func TestServe_LoadDeliverToken_EmptyFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "csuite-watcher-token")
	if err := os.WriteFile(path, []byte("   \n\t\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("CSUITE_WATCHER_TOKEN", "")
	t.Setenv("CSUITE_WATCHER_TOKEN_FILE", path)

	_, err := loadDeliverToken()
	if err == nil {
		t.Fatalf("expected error for whitespace-only file, got nil")
	}
	if !strings.Contains(err.Error(), "CSUITE_WATCHER_TOKEN") {
		t.Errorf("error should name the env var, got %q", err.Error())
	}
}
