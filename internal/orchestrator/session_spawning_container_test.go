package orchestrator

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// newContainerSessionRig builds an Orchestrator wired with a
// fakeWorkerSpawner and a FakeWorktreeManager whose Features map
// points the requested feature name at a real tempdir (so
// resolveIntegrationWorktree's os.Stat check succeeds). The bare repo
// is a real `git init --bare` output so spawnTypedWorker's
// gitref.EnsureBranch step can write refs into an actual object
// database — the reviewer/fixer session tests used to pass with a
// plain tempdir because the container path wasn't yet wired through
// EnsureBranch.
//
// See plans/phase-3.5-subtask-dispatch-migration.md §"Test strategy"
// test (4) and plans/orch-container-subtask-branch-provisioning.md.
func newContainerSessionRig(t *testing.T, featureName string) (*Orchestrator, *fakeWorkerSpawner, model.Project, string) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	bare := testutil.SetupBareRepo(t)
	project := testutil.CreateProject(t, db, "phase35-session-test", bare, "main")

	// Seed the feature branch that the reviewer/fixer session tests use
	// as the task's WorktreeBranch so spawnTypedWorker's EnsureBranch
	// no-ops instead of trying to fork from a ghost ref. pushBranchForSession
	// creates "feature/<featureName>" with one seed commit; tests that
	// need a different branch name can call testutil.AddWorktree
	// directly.
	work := t.TempDir()
	testutil.AddWorktree(t, bare, "feature/"+featureName, work)
	testutil.CommitFile(t, work, "seed.txt", "seed\n", "seed feature/"+featureName)

	// resolveIntegrationWorktree returns the path via
	// FakeWorktreeManager.FeatureWorktreePath which, given a populated
	// Features map, returns the tempdir directly. Using a real dir so
	// the os.Stat check inside resolveIntegrationWorktree succeeds.
	integrationDir := filepath.Join(bare, "feature", featureName, "integration")
	require.NoError(t, os.MkdirAll(integrationDir, 0o755))

	fake := &fakeWorkerSpawner{}
	o := &Orchestrator{
		db:        db,
		projectID: project.ID,
		events:    make(chan Event, 64),
		worktree: &FakeWorktreeManager{
			BarePath: bare,
			Default:  "main",
			Features: map[string]string{featureName: integrationDir},
		},
		logger:         slog.Default().With("component", "session_spawning_container_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake, project, integrationDir
}

// TestSpawnFixerSession_DispatchesViaSpawner asserts that
// SpawnFixerSession routes through o.Spawner.SpawnWorker with
// AgentType="fixer" when o.Spawner is wired. Mirrors the reviewer
// container-dispatch test; the shape is identical.
//
// See plans/phase-3.5-subtask-dispatch-migration.md Commit 4.
func TestSpawnFixerSession_DispatchesViaSpawner(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, _ := newContainerSessionRig(t, "phase35-fixer")

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      project.ID,
		Title:          "fixer-container",
		Description:    "fixer in container mode",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/phase35-fixer",
	}
	require.NoError(t, o.db.Create(&task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-fixer-phase35"}},
	}

	session, err := o.SpawnFixerSession(taskID)
	require.NoError(t, err)
	assert.Equal(t, "container-fixer-phase35", session)

	require.Len(t, fake.spawnCalls, 1)
	assert.Equal(t, "fixer", fake.spawnCalls[0].AgentType)
	assert.Equal(t, "/host/.claude/.credentials.json", fake.spawnCalls[0].CredsMount)

	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", taskID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", reloaded.AssignedAgentID).Error)
	assert.Equal(t, "container-fixer-phase35", ag.TmuxSession)
	assert.Equal(t, model.AgentFixer, ag.AgentType)
}

// TestSpawnReviewerSession_DispatchesViaSpawner asserts that
// SpawnReviewerSession in feature-review mode (task in TESTING_READY)
// routes through o.Spawner.SpawnWorker with AgentType="reviewer" when
// o.Spawner is wired and the direct plan-reviewer config is unset.
// The returned "session name" is the container ID (TmuxSession on the
// Agent row is repurposed as the container handle in container mode).
//
// See plans/phase-3.5-subtask-dispatch-migration.md Commit 3.
func TestSpawnReviewerSession_DispatchesViaSpawner(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, project, _ := newContainerSessionRig(t, "phase35-reviewer")

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      project.ID,
		Title:          "reviewer-container",
		Description:    "reviewer in container mode",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/phase35-reviewer",
	}
	require.NoError(t, o.db.Create(&task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-reviewer-phase35"}},
	}

	session, err := o.SpawnReviewerSession(taskID)
	require.NoError(t, err)
	assert.Equal(t, "container-reviewer-phase35", session,
		"container mode returns the container ID as the session handle")

	// SpawnWorker called once with AgentType=reviewer.
	require.Len(t, fake.spawnCalls, 1)
	assert.Equal(t, "reviewer", fake.spawnCalls[0].AgentType)
	assert.Equal(t, "/host/.claude/.credentials.json", fake.spawnCalls[0].CredsMount)

	// AssignedAgentID was populated by recordContainerOnAgent.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", taskID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", reloaded.AssignedAgentID).Error)
	assert.Equal(t, "container-reviewer-phase35", ag.TmuxSession)
	assert.Equal(t, model.AgentReviewer, ag.AgentType)
}
