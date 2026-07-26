package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestResumeFailedCheckpointContinuesDecomposedChildWithoutMergingOrCompleting(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bare)
	featureDir := t.TempDir()
	parentBranch := "feature/checkpoint-parent"
	testutil.AddWorktree(t, bare, parentBranch, featureDir)
	base := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", "HEAD"))
	workerBranch := "feature/checkpoint-worker"
	workerDir := t.TempDir()
	testutil.AddWorktree(t, bare, workerBranch, workerDir)
	testutil.CommitFile(t, workerDir, "allowed.txt", "partial\n", "partial checkpoint")
	head := strings.TrimSpace(runGitCmd(t, workerDir, "rev-parse", "HEAD"))

	db := testutil.NewTestDB(t)
	fwt := &FakeWorktreeManager{BarePath: bare, Default: defaultBranch, Features: map[string]string{"checkpoint-parent": featureDir}}
	o := testOrchestrator(t, db, fwt)
	o.projectName = "canvas"
	parent := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Description: "parent",
		Status: model.StatusFailed, StateVersion: 2, WorktreeBranch: parentBranch, WorktreeBaseSHA: base,
		Context: model.JSONField{"failure_reason": "partial child"},
	}
	require.NoError(t, db.Create(parent).Error)
	child := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parent.ID,
		Title: "decomposed child", Description: "decomposed child", Phase: "implementation",
		Status: model.StatusFailed, StateVersion: 3,
		Context: model.JSONField{
			"estimated_files": []any{"allowed.txt"}, "writable_files": []any{"allowed.txt"},
			"execution_lane": string(executionLaneDecomposed), "failure_class": failureClassArtifactHandoff,
		},
	}
	require.NoError(t, db.Create(child).Error)

	promptRoot := t.TempDir()
	t.Setenv(workerPromptRootEnv, promptRoot)
	promptPath := filepath.Join(promptRoot, child.ID.String()+".md")
	promptBytes := []byte("immutable prompt")
	require.NoError(t, os.WriteFile(promptPath, promptBytes, 0o600))
	promptSum := sha256.Sum256(promptBytes)
	now := time.Now()
	attempt := &model.WorkerAttempt{
		ID: uuid.New(), TaskID: child.ID, Branch: workerBranch, BaseSHA: base,
		State: model.WorkerAttemptFailed, CompletedAt: &now, CreatedAt: now,
		RenderedPromptPath: promptPath, RenderedPromptHash: hex.EncodeToString(promptSum[:]),
	}
	require.NoError(t, db.Create(attempt).Error)
	journalDir := filepath.Join(promptRoot, "journals", child.ID.String())
	require.NoError(t, os.MkdirAll(journalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(journalDir, "journal.json"), []byte(`{
  "version": 1,
  "prompt_hash": "turn-contract",
  "messages": [{"role":"system","content":"s"},{"role":"user","content":"u"}],
  "next_iteration": 4,
  "completed": false
}`), 0o600))

	require.NoError(t, o.ResumeFailedCheckpoint(child.ID, head, "codex:self-verification"))

	var gotChild, gotParent model.Task
	require.NoError(t, db.First(&gotChild, "id = ?", child.ID).Error)
	require.NoError(t, db.First(&gotParent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusInProgress, gotChild.Status)
	require.Equal(t, model.StatusInProgress, gotParent.Status)
	require.Empty(t, gotChild.WorktreeBranch)
	require.NotContains(t, gotChild.Context, "failure_class")
	require.Contains(t, gotChild.Context, "checkpoint_resume")

	// Continuation is not adoption: the partial worker branch remains
	// unmerged and the child is not declared done.
	parentHead := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", "HEAD"))
	require.Equal(t, base, parentHead)
}

func TestBuildSpawnContextUsesCheckpointBranchWithoutChangingIntegrationIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	o := testOrchestrator(t, db, &FakeWorktreeManager{})
	task := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "resume", Phase: "implementation",
		Context: model.JSONField{"checkpoint_resume": map[string]any{"branch": "feature/checkpoint-worker"}},
	}

	swc, err := o.buildSpawnContext(task, "coder")
	require.NoError(t, err)
	require.Equal(t, "feature/checkpoint-worker", swc.branch)
	require.Empty(t, task.WorktreeBranch)
}

func TestResumeFailedCheckpointFinalizesCompletedJournalWithoutSpawning(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bare)
	featureDir := t.TempDir()
	parentBranch := "feature/completed-checkpoint-parent"
	testutil.AddWorktree(t, bare, parentBranch, featureDir)
	base := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", "HEAD"))
	workerBranch := "feature/completed-checkpoint-worker"
	workerDir := t.TempDir()
	testutil.AddWorktree(t, bare, workerBranch, workerDir)
	testutil.CommitFile(t, workerDir, "allowed.txt", "complete\n", "completed checkpoint")
	head := strings.TrimSpace(runGitCmd(t, workerDir, "rev-parse", "HEAD"))

	db := testutil.NewTestDB(t)
	fwt := &FakeWorktreeManager{BarePath: bare, Default: defaultBranch, Features: map[string]string{"completed-checkpoint-parent": featureDir}}
	fwt.OnFindWorktreeByBranch = func(string) (string, error) { return "", os.ErrNotExist }
	fwt.OnMergeBranch = func(sourceBranch, targetWorktree string) (*WorktreeMergeResult, error) {
		runGitCmd(t, targetWorktree, "merge", "--ff-only", sourceBranch)
		return &WorktreeMergeResult{
			Success: true, SourceBranch: sourceBranch,
			MergeCommit: strings.TrimSpace(runGitCmd(t, targetWorktree, "rev-parse", "HEAD")),
		}, nil
	}
	o := testOrchestrator(t, db, fwt)
	o.projectName = "canvas"
	o.skipConstraintGate = true
	parent := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Description: "parent",
		Status: model.StatusFailed, StateVersion: 2, WorktreeBranch: parentBranch, WorktreeBaseSHA: base,
		Context: model.JSONField{"failure_reason": "completed child misclassified"},
	}
	require.NoError(t, db.Create(parent).Error)
	child := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parent.ID,
		Title: "atomic child", Description: "atomic child", Phase: "implementation",
		Status: model.StatusFailed, StateVersion: 3,
		Context: model.JSONField{
			"estimated_files": []any{"allowed.txt"}, "writable_files": []any{"allowed.txt"},
			"execution_lane": string(executionLaneAtomic), "failure_class": failureClassArtifactHandoff,
		},
	}
	require.NoError(t, db.Create(child).Error)
	agentID := uuid.New()
	ag := &model.Agent{
		ID: agentID, ProjectID: o.projectID, AgentType: model.AgentCoder, Name: "completed-worker",
		Status: model.AgentIdle, WorktreeBranch: workerBranch,
	}
	require.NoError(t, db.Create(ag).Error)

	promptRoot := t.TempDir()
	t.Setenv(workerPromptRootEnv, promptRoot)
	promptPath := filepath.Join(promptRoot, child.ID.String()+".md")
	promptBytes := []byte("immutable prompt")
	require.NoError(t, os.WriteFile(promptPath, promptBytes, 0o600))
	promptSum := sha256.Sum256(promptBytes)
	now := time.Now()
	attempt := &model.WorkerAttempt{
		ID: uuid.New(), TaskID: child.ID, AgentID: &agentID, Branch: workerBranch, BaseSHA: base,
		State: model.WorkerAttemptFailed, CompletedAt: &now, CreatedAt: now,
		RenderedPromptPath: promptPath, RenderedPromptHash: hex.EncodeToString(promptSum[:]),
	}
	require.NoError(t, db.Create(attempt).Error)
	journalDir := filepath.Join(promptRoot, "journals", child.ID.String())
	require.NoError(t, os.MkdirAll(journalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(journalDir, "journal.json"), []byte(`{
  "version": 1,
  "prompt_hash": "turn-contract",
  "messages": [{"role":"system","content":"s"},{"role":"user","content":"u"}],
  "next_iteration": 8,
  "completed": true
}`), 0o600))

	require.NoError(t, o.ResumeFailedCheckpoint(child.ID, head, "orchestrator:checkpoint-recovery"))

	var gotChild, gotParent model.Task
	require.NoError(t, db.First(&gotChild, "id = ?", child.ID).Error)
	require.NoError(t, db.First(&gotParent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusDone, gotChild.Status)
	require.Equal(t, model.StatusTestingReady, gotParent.Status)
	require.Empty(t, gotChild.WorktreeBranch)
	require.Equal(t, head, strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", "HEAD")))
}
