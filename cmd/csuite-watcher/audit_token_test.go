package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadAuditToken_HappyPath verifies a 0600 file with a non-empty
// body returns the trimmed token string.
func TestLoadAuditToken_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tok, err := loadAuditToken(path)
	if err != nil {
		t.Fatalf("loadAuditToken: %v", err)
	}
	if tok != "secret-token" {
		t.Errorf("tok = %q, want %q", tok, "secret-token")
	}
}

// TestLoadAuditToken_MissingFile verifies missing files produce an
// error that mentions the path so the operator knows where to place
// it.
func TestLoadAuditToken_MissingFile(t *testing.T) {
	_, err := loadAuditToken(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should mention the path, got %q", err.Error())
	}
}

// TestLoadAuditToken_WorldReadableRejected verifies a 0644 token
// file is rejected at load time — the plan mandates 0600 enforcement
// at watcher startup.
func TestLoadAuditToken_WorldReadableRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadAuditToken(path)
	if err == nil {
		t.Fatalf("expected error for 0644 file")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should mention 0600, got %q", err.Error())
	}
}

// TestLoadAuditToken_GroupReadableRejected verifies that even 0640
// (group-readable) trips the check. The 0600 rule is strict.
func TestLoadAuditToken_GroupReadableRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("secret"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadAuditToken(path)
	if err == nil {
		t.Fatalf("expected error for 0640 file")
	}
}

// TestLoadAuditToken_EmptyFileRejected verifies an empty body is
// rejected so a misconfigured token cannot silently authenticate
// any client with an empty bearer.
func TestLoadAuditToken_EmptyFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadAuditToken(path)
	if err == nil {
		t.Fatalf("expected error for empty file")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty, got %q", err.Error())
	}
}

// TestAuditTokenPath_DefaultHome verifies the path expands to the
// user home dir when no override env is set.
func TestAuditTokenPath_DefaultHome(t *testing.T) {
	t.Setenv("DREM_AUDIT_TOKEN_PATH", "")
	got := auditTokenPath()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".drem", "csuite-watcher.token")
	if got != want {
		t.Errorf("auditTokenPath = %q, want %q", got, want)
	}
}

// TestAuditTokenPath_EnvOverride verifies DREM_AUDIT_TOKEN_PATH
// overrides the default.
func TestAuditTokenPath_EnvOverride(t *testing.T) {
	t.Setenv("DREM_AUDIT_TOKEN_PATH", "/custom/path")
	got := auditTokenPath()
	if got != "/custom/path" {
		t.Errorf("auditTokenPath = %q, want /custom/path", got)
	}
}
