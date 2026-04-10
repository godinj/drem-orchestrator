package worktree

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	// repoMapScript is the path to the repo map generation script, relative to
	// the worktree root.
	repoMapScript = "scripts/generate-repo-map.sh"

	// repoMapTimeout is the maximum time allowed for repo map generation.
	repoMapTimeout = 30 * time.Second
)

// GenerateRepoMap runs the repo map generation script in the given worktree
// directory. The function is non-blocking in the sense that failures are
// logged as warnings and do not return errors — callers should not block
// agent spawning on repo map generation.
//
// The script is expected at <worktreePath>/scripts/generate-repo-map.sh.
// If the script does not exist, the function returns silently (graceful
// degradation).
func GenerateRepoMap(worktreePath string) {
	scriptPath := filepath.Join(worktreePath, repoMapScript)

	// Check if the script exists before attempting to run it.
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), repoMapTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Dir = worktreePath

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("repo map generation failed",
			"worktree", worktreePath,
			"error", err,
			"output", string(output),
		)
		return
	}

	slog.Info("repo map generated", "worktree", worktreePath)
}

// GenerateRepoMapAsync runs GenerateRepoMap in a background goroutine so
// that it does not block the caller. Errors are logged by GenerateRepoMap.
func GenerateRepoMapAsync(worktreePath string) {
	// Quick check: skip goroutine if script doesn't exist.
	scriptPath := filepath.Join(worktreePath, repoMapScript)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return
	}

	go GenerateRepoMap(worktreePath)
}

// GenerateRepoMapForMain generates the repo map for the default branch
// worktree. It resolves the main worktree path from the Manager and runs
// the generation script. Failures are logged as warnings.
func (m *Manager) GenerateRepoMapForMain() {
	mainPath, err := m.MainWorktreePath()
	if err != nil {
		slog.Warn("repo map: could not resolve main worktree path",
			"error", err,
		)
		return
	}

	// Verify the main worktree directory exists.
	if info, err := os.Stat(mainPath); err != nil || !info.IsDir() {
		slog.Warn("repo map: main worktree path does not exist",
			"path", mainPath,
			"error", fmt.Errorf("stat: %w", err),
		)
		return
	}

	GenerateRepoMap(mainPath)
}
