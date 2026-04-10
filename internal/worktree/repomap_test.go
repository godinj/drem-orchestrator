package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateRepoMap_ScriptMissing(t *testing.T) {
	// When the script does not exist, GenerateRepoMap should return silently
	// without error (graceful degradation).
	dir := t.TempDir()
	GenerateRepoMap(dir) // should not panic or log errors
}

func TestGenerateRepoMap_ScriptExists(t *testing.T) {
	dir := t.TempDir()

	// Create a scripts directory and a minimal generate-repo-map.sh that
	// writes repo-map.md.
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := `#!/bin/bash
echo "# Repo Map" > "$PWD/repo-map.md"
`
	scriptPath := filepath.Join(scriptsDir, "generate-repo-map.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	GenerateRepoMap(dir)

	// Verify repo-map.md was created.
	repoMapPath := filepath.Join(dir, "repo-map.md")
	data, err := os.ReadFile(repoMapPath)
	if err != nil {
		t.Fatalf("repo-map.md not created: %v", err)
	}
	if string(data) != "# Repo Map\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestGenerateRepoMap_ScriptFails(t *testing.T) {
	dir := t.TempDir()

	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Script that exits with error.
	script := `#!/bin/bash
exit 1
`
	scriptPath := filepath.Join(scriptsDir, "generate-repo-map.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Should not panic; failure is logged as a warning.
	GenerateRepoMap(dir)
}

func TestGenerateRepoMapAsync_ScriptMissing(t *testing.T) {
	// When script doesn't exist, the goroutine should not be launched
	// (early return). No way to assert no goroutine, but at least verify
	// no panic.
	dir := t.TempDir()
	GenerateRepoMapAsync(dir)
}
