package ctxmon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SetupDir creates the .claude/ directory inside dir with context monitoring
// files: a status line script and settings.json with statusLine and
// PreCompact hook configuration. This is used by swarm agents to self-configure
// context monitoring in their worktrees.
func SetupDir(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("setup dir: %w", err)
	}

	if _, err := os.Stat(absDir); err != nil {
		return fmt.Errorf("setup dir: %w", err)
	}

	claudeDir := filepath.Join(absDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("setup dir: %w", err)
	}

	// Write the status line script.
	scriptPath := filepath.Join(claudeDir, "context-status.sh")
	scriptContent := StatusScript(UsageFilePath(absDir))
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		return fmt.Errorf("setup dir: write script: %w", err)
	}

	// Build settings.json with statusLine and hooks.
	settings := map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": scriptPath,
		},
		"hooks": HooksJSON(CompactionSignalPath(absDir)),
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("setup dir: marshal settings: %w", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		return fmt.Errorf("setup dir: write settings: %w", err)
	}

	return nil
}
