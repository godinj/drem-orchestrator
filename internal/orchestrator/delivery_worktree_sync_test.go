package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestSynchronizeAcceptedWorktreeRefreshesExternallyAdvancedBranch(t *testing.T) {
	orch, task, featureDir, acceptedSHA := externalWorkerPushFixture(t)

	result, err := orch.synchronizeAcceptedWorktree(context.Background(), task, featureDir)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, acceptedSHA, result.ToSHA)
	require.FileExists(t, filepath.Join(featureDir, ".drem-canary.json"))
	require.Empty(t, runGitCmd(t, featureDir, "status", "--porcelain"))
	require.Equal(t, acceptedSHA, runGitCmd(t, featureDir, "rev-parse", "HEAD"))
}

func TestSynchronizeAcceptedWorktreeRefusesToOverwriteLocalChanges(t *testing.T) {
	orch, task, featureDir, _ := externalWorkerPushFixture(t)
	localPath := filepath.Join(featureDir, "operator-note.txt")
	require.NoError(t, os.WriteFile(localPath, []byte("preserve me\n"), 0o644))

	_, err := orch.synchronizeAcceptedWorktree(context.Background(), task, featureDir)
	require.ErrorContains(t, err, "refusing to overwrite changes")
	require.FileExists(t, localPath)
	require.NoFileExists(t, filepath.Join(featureDir, ".drem-canary.json"))
}

func TestSynchronizeAcceptedWorktreeRejectsFeatureRefDrift(t *testing.T) {
	orch, task, featureDir, _ := externalWorkerPushFixture(t)
	task.Context["branch_acceptance"] = map[string]any{"head_sha": strings.Repeat("a", 40)}

	_, err := orch.synchronizeAcceptedWorktree(context.Background(), task, featureDir)
	require.ErrorContains(t, err, "accepted ref drift")
	require.NoFileExists(t, filepath.Join(featureDir, ".drem-canary.json"))
}

func externalWorkerPushFixture(t *testing.T) (*Orchestrator, *model.Task, string, string) {
	t.Helper()
	bareRepo := setupTestRepoWithMainBranch(t)
	featureDir := createFeatureWorktree(t, bareRepo, "delivery-sync")
	producerDir := filepath.Join(t.TempDir(), "producer")
	runGitCmd(t, bareRepo, "worktree", "add", "--detach", producerDir, "main")
	runGitCmd(t, producerDir, "config", "user.email", "worker@test.local")
	runGitCmd(t, producerDir, "config", "user.name", "Worker")
	require.NoError(t, os.WriteFile(filepath.Join(producerDir, ".drem-canary.json"), []byte("{}\n"), 0o644))
	runGitCmd(t, producerDir, "add", ".drem-canary.json")
	runGitCmd(t, producerDir, "commit", "-m", "worker artifact")
	acceptedSHA := runGitCmd(t, producerDir, "rev-parse", "HEAD")
	previousSHA := runGitCmd(t, bareRepo, "rev-parse", "refs/heads/feature/delivery-sync")
	runGitCmd(t, bareRepo, "update-ref", "refs/heads/feature/delivery-sync", acceptedSHA, previousSHA)

	task := &model.Task{
		WorktreeBranch: "feature/delivery-sync",
		Context: model.JSONField{
			"branch_acceptance": map[string]any{"head_sha": acceptedSHA},
		},
	}
	return &Orchestrator{}, task, featureDir, acceptedSHA
}
