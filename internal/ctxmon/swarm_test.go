package ctxmon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupDir(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string // returns dir path
		wantErr bool
	}{
		{
			name: "valid directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name: "nonexistent directory",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "does-not-exist")
			},
			wantErr: true,
		},
		{
			name: "idempotent",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := SetupDir(dir); err != nil {
					t.Fatalf("first SetupDir: %v", err)
				}
				return dir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			err := SetupDir(dir)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify .claude/ directory exists.
			claudeDir := filepath.Join(dir, ".claude")
			info, err := os.Stat(claudeDir)
			if err != nil {
				t.Fatalf(".claude/ not created: %v", err)
			}
			if !info.IsDir() {
				t.Fatal(".claude/ is not a directory")
			}

			// Verify context-status.sh is executable and has correct content.
			scriptPath := filepath.Join(claudeDir, "context-status.sh")
			scriptInfo, err := os.Stat(scriptPath)
			if err != nil {
				t.Fatalf("context-status.sh not created: %v", err)
			}
			if scriptInfo.Mode()&0o111 == 0 {
				t.Error("context-status.sh is not executable")
			}

			scriptContent, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatalf("reading script: %v", err)
			}
			if !strings.HasPrefix(string(scriptContent), "#!/bin/sh") {
				t.Error("script missing shebang")
			}
			if !strings.Contains(string(scriptContent), "context-usage.json") {
				t.Error("script does not reference context-usage.json")
			}

			// Verify settings.json is valid JSON with expected keys.
			settingsPath := filepath.Join(claudeDir, "settings.json")
			settingsData, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}

			var settings map[string]any
			if err := json.Unmarshal(settingsData, &settings); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			if _, ok := settings["statusLineScript"]; !ok {
				t.Error("settings.json missing statusLineScript key")
			}

			hooks, ok := settings["hooks"].(map[string]any)
			if !ok {
				t.Fatal("settings.json missing hooks key or not a map")
			}
			if _, ok := hooks["PreCompact"]; !ok {
				t.Error("hooks missing PreCompact key")
			}
		})
	}
}

func TestSetupDirNoNotificationHook(t *testing.T) {
	dir := t.TempDir()
	if err := SetupDir(dir); err != nil {
		t.Fatalf("SetupDir: %v", err)
	}

	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks not a map")
	}

	if _, ok := hooks["Notification"]; ok {
		t.Error("hooks should not contain Notification key (that's drem-orchestrator-specific)")
	}
}
