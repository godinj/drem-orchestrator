package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestAcceptedDeliveryGateUsesExactDetachedSHAAndPreservesDirtyPersistentWorktree(t *testing.T) {
	o, task, featureDir, acceptedSHA, baseSHA := deliveryGateFixture(t)
	dirtyPath := filepath.Join(featureDir, "operator-note.txt")
	require.NoError(t, os.WriteFile(dirtyPath, []byte("preserve me\n"), 0o644))
	o.testGate.TestCommand = fmt.Sprintf(`test "$(git rev-parse HEAD)" = %s`, acceptedSHA)
	before := runGitCmd(t, o.worktree.BareRepo(), "worktree", "list", "--porcelain")

	result, err := o.runAcceptedDeliveryGate(context.Background(), &task)
	require.NoError(t, err)
	require.True(t, result.Passed)
	require.Equal(t, acceptedSHA, result.Candidate.CommitSHA)
	require.Equal(t, baseSHA, result.Candidate.BaseSHA)
	require.FileExists(t, dirtyPath)
	require.Equal(t, before, runGitCmd(t, o.worktree.BareRepo(), "worktree", "list", "--porcelain"))
	require.NoDirExists(t, filepath.Join(os.TempDir(), result.WorkspaceID))
}

func TestAcceptedDeliveryGateUsesAcceptedBaseAfterDefaultBranchDrifts(t *testing.T) {
	o, task, _, _, acceptedBaseSHA := deliveryGateFixture(t)
	mainDir := filepath.Join(t.TempDir(), "main")
	runGitCmd(t, o.worktree.BareRepo(), "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(mainDir, "main-drift.txt"), []byte("drift\n"), 0o644))
	runGitCmd(t, mainDir, "add", "main-drift.txt")
	runGitCmd(t, mainDir, "commit", "-m", "advance default")
	require.NotEqual(t, acceptedBaseSHA, runGitCmd(t, mainDir, "rev-parse", "HEAD"))
	o.testGate.TestCommand = "true"

	result, err := o.runAcceptedDeliveryGate(context.Background(), &task)
	require.NoError(t, err)
	require.Equal(t, acceptedBaseSHA, result.Candidate.BaseSHA)
	require.Equal(t, acceptedBaseSHA, result.artifactSnapshot().BaseSHA)
}

func TestAcceptedDeliveryGateRejectsBranchRefDriftBeforeExecution(t *testing.T) {
	o, task, featureDir, acceptedSHA, _ := deliveryGateFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "drift.txt"), []byte("drift\n"), 0o644))
	runGitCmd(t, featureDir, "add", "drift.txt")
	runGitCmd(t, featureDir, "commit", "-m", "drift accepted branch")
	require.NotEqual(t, acceptedSHA, runGitCmd(t, featureDir, "rev-parse", "HEAD"))
	o.testGate.TestCommand = "touch should-not-run"

	_, err := o.runAcceptedDeliveryGate(context.Background(), &task)
	require.ErrorContains(t, err, "accepted ref drift")
	require.NoFileExists(t, filepath.Join(featureDir, "should-not-run"))
}

func TestAcceptedDeliveryGateRejectsCommandMutationAndCleansWorkspace(t *testing.T) {
	o, task, _, _, _ := deliveryGateFixture(t)
	o.testGate.TestCommand = "touch generated-by-gate"
	before := runGitCmd(t, o.worktree.BareRepo(), "worktree", "list", "--porcelain")

	result, err := o.runAcceptedDeliveryGate(context.Background(), &task)
	require.ErrorContains(t, err, "mutated the accepted checkout")
	require.Equal(t, before, runGitCmd(t, o.worktree.BareRepo(), "worktree", "list", "--porcelain"))
	require.NotEmpty(t, result.WorkspaceID)
	require.NoDirExists(t, filepath.Join(os.TempDir(), result.WorkspaceID))
}

func TestProcessTestingReadyRecordsUnavailableWorktreeManager(t *testing.T) {
	o, task, snapshot := deliveryFixture(t)
	persistAcceptedSnapshot(t, o, task, snapshot)
	o.worktree = nil

	require.NoError(t, o.processTestingReady(&task))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPaused, updated.Status)
	gateRun := loadOnlyPreliminaryGateRun(t, o, task.ID)
	require.Equal(t, model.PreliminaryGateConfiguration, gateRun.Outcome)
	require.Contains(t, string(mustJSON(t, gateRun.CommandEvidence)), "delivery-gate:worktree-manager")
	var artifacts int64
	require.NoError(t, o.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&artifacts).Error)
	require.Zero(t, artifacts)
}

func TestProcessTestingReadyRecordsAcceptedCheckoutMutation(t *testing.T) {
	o, task, snapshot := deliveryFixture(t)
	persistAcceptedSnapshot(t, o, task, snapshot)
	o.testGate.TestCommand = "touch generated-by-gate"

	require.NoError(t, o.processTestingReady(&task))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, updated.Status)
	gateRun := loadOnlyPreliminaryGateRun(t, o, task.ID)
	require.Equal(t, model.PreliminaryGateCodeFailure, gateRun.Outcome)
	require.Contains(t, string(mustJSON(t, gateRun.CommandEvidence)), "delivery-gate:checkout-post-command")
	var artifacts int64
	require.NoError(t, o.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&artifacts).Error)
	require.Zero(t, artifacts)
}

func TestProcessTestingReadyRecordsWorkspaceCreationFailure(t *testing.T) {
	o, task, snapshot := deliveryFixture(t)
	persistAcceptedSnapshot(t, o, task, snapshot)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	require.NoError(t, o.processTestingReady(&task))

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPaused, updated.Status)
	gateRun := loadOnlyPreliminaryGateRun(t, o, task.ID)
	require.Equal(t, model.PreliminaryGateInfraFailure, gateRun.Outcome)
	require.Contains(t, string(mustJSON(t, gateRun.CommandEvidence)), "delivery-gate:workspace-root")
}

func TestAcceptedDeliveryGateFailsClosedWithoutCompleteAcceptance(t *testing.T) {
	task := model.Task{ID: uuid.New(), WorktreeBranch: "feature/example", Context: model.JSONField{
		"branch_acceptance": map[string]any{"accepted": true, "head_sha": strings.Repeat("a", 40)},
	}}
	o := testOrchestrator(t, testutil.NewTestDB(t), &FakeWorktreeManager{})
	_, err := o.acceptedDeliveryCandidate(&task)
	require.ErrorContains(t, err, "typed branch acceptance is missing")
}

func TestClassifyPreliminaryGateTimeout(t *testing.T) {
	outcome := classifyPreliminaryGateOutcome(CommandEvidence{
		Command: "slow gate", ExitCode: -1, Output: "[killed: timeout exceeded]",
	})
	require.Equal(t, model.PreliminaryGateTimeout, outcome)
}

func deliveryGateFixture(t *testing.T) (*Orchestrator, model.Task, string, string, string) {
	t.Helper()
	bareRepo := setupTestRepoWithMainBranch(t)
	featureDir := createFeatureWorktree(t, bareRepo, "delivery-gate-"+uuid.NewString()[:8])
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "candidate.txt"), []byte("candidate\n"), 0o644))
	runGitCmd(t, featureDir, "add", "candidate.txt")
	runGitCmd(t, featureDir, "commit", "-m", "delivery candidate")
	task := model.Task{ID: uuid.New(), WorktreeBranch: strings.TrimSpace(runGitCmd(t, featureDir, "branch", "--show-current"))}
	recordBranchAcceptanceForTest(t, &task, featureDir, "main")
	db := testutil.NewTestDB(t)
	persistBranchAcceptanceForTest(t, db, &task)
	o := testOrchestrator(t, db, &FakeWorktreeManager{BarePath: bareRepo, Default: "main"})
	return o, task, featureDir,
		runGitCmd(t, featureDir, "rev-parse", "HEAD"),
		runGitCmd(t, featureDir, "rev-parse", "main")
}

func persistAcceptedSnapshot(t *testing.T, o *Orchestrator, task model.Task, snapshot ArtifactSnapshot) {
	t.Helper()
	record := model.BranchAcceptanceRecord{
		ID: uuid.New(), TaskID: task.ID, AgentID: uuid.New(),
		Branch: snapshot.Branch, Accepted: true,
		BaseBranch: snapshot.BaseBranch, BaseSHA: snapshot.BaseSHA, HeadSHA: snapshot.CommitSHA,
		Details: model.JSONField{"accepted": true}, Actor: "test", Source: "test",
	}
	require.NoError(t, o.db.Create(&record).Error)
}

func loadOnlyPreliminaryGateRun(t *testing.T, o *Orchestrator, taskID uuid.UUID) model.PreliminaryGateRun {
	t.Helper()
	var runs []model.PreliminaryGateRun
	require.NoError(t, o.db.Where("task_id = ?", taskID).Find(&runs).Error)
	require.Len(t, runs, 1)
	return runs[0]
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
