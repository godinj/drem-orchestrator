package worktreehost

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
// directory. Failures are logged as warnings and do not return errors —
// callers should not block agent spawning on repo map generation.
func GenerateRepoMap(worktreePath string) {
	scriptPath := filepath.Join(worktreePath, repoMapScript)

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

// GenerateRepoMapAsync runs GenerateRepoMap in a background goroutine.
func GenerateRepoMapAsync(worktreePath string) {
	scriptPath := filepath.Join(worktreePath, repoMapScript)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return
	}

	go GenerateRepoMap(worktreePath)
}

// GenerateRepoMapAsync is a method-form wrapper over the package-level
// GenerateRepoMapAsync so *Manager can satisfy orchestrator.WorktreeManager.
func (m *Manager) GenerateRepoMapAsync(worktreePath string) {
	GenerateRepoMapAsync(worktreePath)
}

// GenerateRepoMapForMain generates the repo map for the default branch
// worktree.
func (m *Manager) GenerateRepoMapForMain() {
	mainPath, err := m.MainWorktreePath()
	if err != nil {
		slog.Warn("repo map: could not resolve main worktree path",
			"error", err,
		)
		return
	}

	if info, err := os.Stat(mainPath); err != nil || !info.IsDir() {
		slog.Warn("repo map: main worktree path does not exist",
			"path", mainPath,
			"error", fmt.Errorf("stat: %w", err),
		)
		return
	}

	GenerateRepoMap(mainPath)
}
