package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// loadServeConfig — TOML parsing tests
// ---------------------------------------------------------------------------

// TestLoadServeConfig_ValidTOML verifies that all [serve] TOML fields are
// parsed into the correct struct fields.
func TestLoadServeConfig_ValidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drem.toml")
	content := `
[serve]
  listen_addr  = "0.0.0.0:8080"
  bearer_token = "secret-token"
  db_path      = "/var/lib/drem/csuite.db"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatalf("loadServeConfig returned unexpected error: %v", err)
	}

	if cfg.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, "0.0.0.0:8080")
	}
	if cfg.BearerToken != "secret-token" {
		t.Errorf("BearerToken: got %q, want %q", cfg.BearerToken, "secret-token")
	}
	if cfg.DBPath != "/var/lib/drem/csuite.db" {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, "/var/lib/drem/csuite.db")
	}
}

// TestLoadServeConfig_MissingFile verifies that a missing config file returns
// a zero-value config with no error, consistent with loadWatcherConfig behaviour.
func TestLoadServeConfig_MissingFile(t *testing.T) {
	cfg, err := loadServeConfig("/nonexistent/path/drem.toml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.ListenAddr != "" {
		t.Errorf("ListenAddr: expected empty for missing file, got %q", cfg.ListenAddr)
	}
	if cfg.BearerToken != "" {
		t.Errorf("BearerToken: expected empty for missing file, got %q", cfg.BearerToken)
	}
	if cfg.DBPath != "" {
		t.Errorf("DBPath: expected empty for missing file, got %q", cfg.DBPath)
	}
}

// TestLoadServeConfig_ServeAbsent verifies that a valid TOML file without a
// [serve] section returns a zero-value config (no error).
func TestLoadServeConfig_ServeAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drem.toml")
	content := `
[watcher]
  db_path = "~/.drem-csuite/watcher.db"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != "" || cfg.BearerToken != "" || cfg.DBPath != "" {
		t.Errorf("expected zero-value serve config when [serve] absent, got %+v", cfg)
	}
}

// TestLoadServeConfig_PartialSection verifies that only specified fields are
// populated when the [serve] section is present but incomplete.
func TestLoadServeConfig_PartialSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drem.toml")
	content := `
[serve]
  listen_addr = "127.0.0.1:9090"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, "127.0.0.1:9090")
	}
	if cfg.BearerToken != "" {
		t.Errorf("BearerToken: expected empty for unset field, got %q", cfg.BearerToken)
	}
	if cfg.DBPath != "" {
		t.Errorf("DBPath: expected empty for unset field, got %q", cfg.DBPath)
	}
}

// ---------------------------------------------------------------------------
// applyServeEnvOverrides — 12-factor env-var precedence tests
// ---------------------------------------------------------------------------

// TestApplyServeEnvOverrides_EnvOnly verifies that env vars populate an
// otherwise empty config (the no-toml path the per-project compose service
// uses).
func TestApplyServeEnvOverrides_EnvOnly(t *testing.T) {
	t.Setenv("DREM_BEARER_TOKEN", "env-token")
	t.Setenv("DREM_LISTEN_ADDR", ":8090")
	t.Setenv("DREM_DB_PATH", "/var/lib/drem/csuite.db")

	cfg := serveTomlConfig{}
	applyServeEnvOverrides(&cfg)

	if cfg.BearerToken != "env-token" {
		t.Errorf("BearerToken: got %q, want %q", cfg.BearerToken, "env-token")
	}
	if cfg.ListenAddr != ":8090" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, ":8090")
	}
	if cfg.DBPath != "/var/lib/drem/csuite.db" {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, "/var/lib/drem/csuite.db")
	}
}

// TestApplyServeEnvOverrides_TomlOnly verifies that an unset env var leaves
// the toml-loaded value untouched (the legacy host-mode path).
func TestApplyServeEnvOverrides_TomlOnly(t *testing.T) {
	// Clear any inherited env so this test is hermetic.
	t.Setenv("DREM_BEARER_TOKEN", "")
	t.Setenv("DREM_LISTEN_ADDR", "")
	t.Setenv("DREM_DB_PATH", "")

	cfg := serveTomlConfig{
		BearerToken: "toml-token",
		ListenAddr:  "127.0.0.1:9090",
		DBPath:      "/tmp/toml.db",
	}
	applyServeEnvOverrides(&cfg)

	if cfg.BearerToken != "toml-token" {
		t.Errorf("BearerToken: got %q, want %q", cfg.BearerToken, "toml-token")
	}
	if cfg.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, "127.0.0.1:9090")
	}
	if cfg.DBPath != "/tmp/toml.db" {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, "/tmp/toml.db")
	}
}

// TestApplyServeEnvOverrides_EnvOverridesToml verifies that when both sources
// supply a value, env wins — the documented precedence.
func TestApplyServeEnvOverrides_EnvOverridesToml(t *testing.T) {
	t.Setenv("DREM_BEARER_TOKEN", "env-token")
	t.Setenv("DREM_LISTEN_ADDR", ":8090")
	t.Setenv("DREM_DB_PATH", "/var/lib/drem/csuite.db")

	cfg := serveTomlConfig{
		BearerToken: "toml-token",
		ListenAddr:  "127.0.0.1:9090",
		DBPath:      "/tmp/toml.db",
	}
	applyServeEnvOverrides(&cfg)

	if cfg.BearerToken != "env-token" {
		t.Errorf("BearerToken: env should win, got %q", cfg.BearerToken)
	}
	if cfg.ListenAddr != ":8090" {
		t.Errorf("ListenAddr: env should win, got %q", cfg.ListenAddr)
	}
	if cfg.DBPath != "/var/lib/drem/csuite.db" {
		t.Errorf("DBPath: env should win, got %q", cfg.DBPath)
	}
}

// ---------------------------------------------------------------------------
// run() subcommand dispatch — serve recognition tests
// ---------------------------------------------------------------------------

// TestRun_ServeRecognized verifies that 'serve' is a known subcommand and does
// not trigger the "unknown subcommand" error path.
func TestRun_ServeRecognized(t *testing.T) {
	var buf bytes.Buffer
	run([]string{"serve"}, &buf)
	if strings.Contains(buf.String(), "unknown subcommand") {
		t.Errorf("'serve' must be a recognised subcommand; stderr: %q", buf.String())
	}
}

// TestRun_ServeExitsNonZeroWithoutServer verifies that the serve subcommand
// exits non-zero until a real server is implemented (stub behaviour).
func TestRun_ServeExitsNonZeroWithoutServer(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"serve"}, &buf)
	if code == 0 {
		t.Error("serve stub must return non-zero until implemented")
	}
}

// TestRun_UnknownSubcommandStillErrors verifies that adding 'serve' to the
// switch has not broken the default error case for unknown subcommands.
func TestRun_UnknownSubcommandStillErrors(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"notacommand"}, &buf)
	if code == 0 {
		t.Error("expected non-zero exit for unknown subcommand, got 0")
	}
	if !strings.Contains(buf.String(), "unknown subcommand") {
		t.Errorf("expected 'unknown subcommand' in stderr, got: %q", buf.String())
	}
}

// TestParseRescanInterval covers the env var parsing for the periodic
// rescan loop. Scoreboard item 5: a periodic rescan catches missed
// signals without operator intervention.
func TestParseRescanInterval(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string // Duration.String() — 0 means disabled
	}{
		{"empty falls back to default", "", "5m0s"},
		{"malformed falls back to default", "not-a-duration", "5m0s"},
		{"explicit 1 minute", "1m", "1m0s"},
		{"explicit 30 seconds", "30s", "30s"},
		{"zero disables", "0s", "0s"},
		{"negative disables", "-1s", "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRescanInterval(tt.env)
			if got.String() != tt.want {
				t.Errorf("parseRescanInterval(%q) = %v, want %s", tt.env, got, tt.want)
			}
		})
	}
}

func TestParseMaxRescanFilesPerPersona(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int
	}{
		{"empty falls back to safe default", "", 25},
		{"malformed falls back to safe default", "many", 25},
		{"zero falls back to safe default", "0", 25},
		{"negative falls back to safe default", "-1", 25},
		{"positive override", "7", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMaxRescanFilesPerPersona(tt.env)
			if got != tt.want {
				t.Errorf("parseMaxRescanFilesPerPersona(%q) = %d, want %d", tt.env, got, tt.want)
			}
		})
	}
}
