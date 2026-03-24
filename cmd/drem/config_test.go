package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigTmuxSocket(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TmuxSocket != "drem" {
		t.Errorf("TmuxSocket: got %q, want %q", cfg.TmuxSocket, "drem")
	}
}

func TestDefaultConfigTmuxConfigFile(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TmuxConfigFile != "master/.tmux.conf" {
		t.Errorf("TmuxConfigFile: got %q, want %q", cfg.TmuxConfigFile, "master/.tmux.conf")
	}
}

func TestLoadConfigTmuxOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	data := []byte(`tmux_socket = "custom"
tmux_config_file = "custom.conf"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.TmuxSocket != "custom" {
		t.Errorf("TmuxSocket: got %q, want %q", cfg.TmuxSocket, "custom")
	}
	if cfg.TmuxConfigFile != "custom.conf" {
		t.Errorf("TmuxConfigFile: got %q, want %q", cfg.TmuxConfigFile, "custom.conf")
	}
}

func TestLoadConfigTmuxDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// TOML file with no tmux fields — defaults must survive.
	data := []byte(`database_path = "./other.db"
`)
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.TmuxSocket != "drem" {
		t.Errorf("TmuxSocket: got %q, want %q", cfg.TmuxSocket, "drem")
	}
	if cfg.TmuxConfigFile != "master/.tmux.conf" {
		t.Errorf("TmuxConfigFile: got %q, want %q", cfg.TmuxConfigFile, "master/.tmux.conf")
	}
	// Verify the explicit override still applied.
	if cfg.DatabasePath != "./other.db" {
		t.Errorf("DatabasePath: got %q, want %q", cfg.DatabasePath, "./other.db")
	}
}
