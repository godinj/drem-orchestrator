package orchestrator

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestAdoptFailedChildAcceptsExactRepairedHeadAndResumesParent(t *testing.T) {
	o, parent, child, attempt, featureDir, workerBranch := codexAdoptionRig(t, []any{"allowed.txt"})
	testutil.CommitFile(t, featureDir, "seed.txt", "parent remains isolated\n", "parent seed")
	head := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", workerBranch))

	var mergedBranch, mergedTarget string
	fwt := o.worktree.(*FakeWorktreeManager)
	fwt.OnFindWorktreeByBranch = func(string) (string, error) { return "", errors.New("worker checkout owned by host") }
	fwt.OnMergeBranch = func(source, target string) (*WorktreeMergeResult, error) {
		mergedBranch, mergedTarget = source, target
		return &WorktreeMergeResult{Success: true, SourceBranch: source, TargetBranch: parent.WorktreeBranch, MergeCommit: "merge-sha"}, nil
	}

	require.NoError(t, o.AdoptFailedChild(child.ID, head, "codex:thread-pilot"))
	require.Equal(t, workerBranch, mergedBranch)
	require.Equal(t, featureDir, mergedTarget)

	var gotChild, gotParent model.Task
	require.NoError(t, o.db.First(&gotChild, "id = ?", child.ID).Error)
	require.NoError(t, o.db.First(&gotParent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusDone, gotChild.Status)
	require.Equal(t, model.StatusTestWriting, gotParent.Status)
	require.Nil(t, gotChild.AssignedAgentID)
	require.NotContains(t, gotChild.Context, "failure_class")
	require.NotContains(t, gotParent.Context, "failure_reason")

	var acceptance model.BranchAcceptanceRecord
	require.NoError(t, o.db.Where("task_id = ? AND source = ?", child.ID, "codex_adapter_adoption").First(&acceptance).Error)
	require.True(t, acceptance.Accepted)
	require.Equal(t, attempt.BaseSHA, acceptance.BaseSHA)
	require.Equal(t, head, acceptance.HeadSHA)
	require.Equal(t, "codex:thread-pilot", acceptance.Actor)
}

func TestAdoptFailedChildRejectsStaleRequestedHeadBeforeMerge(t *testing.T) {
	o, _, child, _, _, _ := codexAdoptionRig(t, []any{"allowed.txt"})
	called := false
	o.worktree.(*FakeWorktreeManager).OnMergeBranch = func(_, _ string) (*WorktreeMergeResult, error) {
		called = true
		return &WorktreeMergeResult{Success: true}, nil
	}

	err := o.AdoptFailedChild(child.ID, strings.Repeat("f", 40), "codex:thread-pilot")
	require.ErrorIs(t, err, ErrCodexAdoptionConflict)
	require.False(t, called)
}

func TestAdoptFailedChildRejectsRemainingScopeContaminationBeforeMerge(t *testing.T) {
	o, _, child, _, featureDir, workerBranch := codexAdoptionRig(t, []any{"different.txt"})
	head := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", workerBranch))
	called := false
	o.worktree.(*FakeWorktreeManager).OnMergeBranch = func(_, _ string) (*WorktreeMergeResult, error) {
		called = true
		return &WorktreeMergeResult{Success: true}, nil
	}

	err := o.AdoptFailedChild(child.ID, head, "codex:thread-pilot")
	require.ErrorIs(t, err, ErrCodexAdoptionConflict)
	require.Contains(t, err.Error(), "allowed.txt")
	require.False(t, called)

	var got model.Task
	require.NoError(t, o.db.First(&got, "id = ?", child.ID).Error)
	require.Equal(t, model.StatusFailed, got.Status)
}

func TestAdoptFailedIntegrationChildWhenParentAlreadyInProgress(t *testing.T) {
	o, parent, child, _, featureDir, workerBranch := codexAdoptionRig(t, []any{"allowed.txt"})
	require.NoError(t, o.db.Model(parent).Updates(map[string]any{
		"status":  model.StatusInProgress,
		"context": model.JSONField{"failure_reason": "stale child failure"},
	}).Error)
	require.NoError(t, o.db.Model(child).Update("phase", "integration").Error)
	head := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", workerBranch))

	fwt := o.worktree.(*FakeWorktreeManager)
	fwt.OnFindWorktreeByBranch = func(string) (string, error) { return "", errors.New("worker checkout owned by host") }
	fwt.OnMergeBranch = func(source, target string) (*WorktreeMergeResult, error) {
		return &WorktreeMergeResult{Success: true, SourceBranch: source, TargetBranch: parent.WorktreeBranch, MergeCommit: "merge-sha"}, nil
	}

	require.NoError(t, o.AdoptFailedChild(child.ID, head, "codex:thread-pilot"))

	var gotChild, gotParent model.Task
	require.NoError(t, o.db.First(&gotChild, "id = ?", child.ID).Error)
	require.NoError(t, o.db.First(&gotParent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusDone, gotChild.Status)
	require.Equal(t, model.StatusInProgress, gotParent.Status)
	require.NotContains(t, gotParent.Context, "failure_reason")
}

func codexAdoptionRig(t *testing.T, scopes []any) (*Orchestrator, *model.Task, *model.Task, *model.WorkerAttempt, string, string) {
	t.Helper()
	bare := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bare)
	featureDir := t.TempDir()
	parentBranch := "feature/codex-adoption-parent"
	testutil.AddWorktree(t, bare, parentBranch, featureDir)
	base := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", "HEAD"))
	workerBranch := "feature/codex-adoption-worker"
	workerDir := t.TempDir()
	testutil.AddWorktree(t, bare, workerBranch, workerDir)
	testutil.CommitFile(t, workerDir, "allowed.txt", "repaired\n", "repair in declared scope")

	db := testutil.NewTestDB(t)
	fwt := &FakeWorktreeManager{
		BarePath: bare, Default: defaultBranch,
		Features: map[string]string{"codex-adoption-parent": featureDir},
	}
	o := testOrchestrator(t, db, fwt)
	parent := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Description: "parent",
		Status: model.StatusFailed, StateVersion: 4, WorktreeBranch: parentBranch,
		Context: model.JSONField{"failure_reason": "child failed"},
	}
	require.NoError(t, db.Create(parent).Error)
	child := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parent.ID,
		Title: "test child", Description: "test child", Phase: "test",
		Status: model.StatusFailed, StateVersion: 3,
		Context: model.JSONField{"estimated_files": scopes, "failure_class": "branch_contamination"},
	}
	require.NoError(t, db.Create(child).Error)
	now := time.Now()
	attempt := &model.WorkerAttempt{
		ID: uuid.New(), TaskID: child.ID, Branch: workerBranch, BaseSHA: base,
		State: model.WorkerAttemptFailed, CompletedAt: &now, CreatedAt: now,
	}
	require.NoError(t, db.Create(attempt).Error)
	return o, parent, child, attempt, featureDir, workerBranch
}
