package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
)

func TestCaptureWorkerUsagePersistsAttemptAndPublicAgentMetrics(t *testing.T) {
	o, fake, _ := workerSpawnTestRig(t)
	taskID, agentID := uuid.New(), uuid.New()
	ag := model.Agent{
		ID: agentID, ProjectID: o.projectID, AgentType: model.AgentCoder,
		Name: "usage-agent", Status: model.AgentWorking, CurrentTaskID: &taskID,
	}
	require.NoError(t, o.db.Create(&ag).Error)
	attempt := model.WorkerAttempt{
		ID: uuid.New(), TaskID: taskID, AgentID: &agentID, AgentType: string(model.AgentCoder),
		ContainerID: "usage-container", State: model.WorkerAttemptRunning,
	}
	require.NoError(t, o.db.Create(&attempt).Error)
	fake.inspectResult = spawner.InspectWorkerResult{Usage: &spawner.WorkerUsage{
		Iterations: 5, TokensIn: 12000, TokensOut: 700, StopReason: "success",
	}}

	o.captureWorkerUsage(context.Background(), &attempt, container.Event{UsageInspected: true})

	var gotAttempt model.WorkerAttempt
	require.NoError(t, o.db.First(&gotAttempt, "id = ?", attempt.ID).Error)
	require.Equal(t, 12000, gotAttempt.TokensIn)
	require.Equal(t, 700, gotAttempt.TokensOut)
	var gotAgent model.Agent
	require.NoError(t, o.db.First(&gotAgent, "id = ?", agentID).Error)
	require.Equal(t, 12000, gotAgent.TokensIn)
	require.Equal(t, 700, gotAgent.TokensOut)
	require.EqualValues(t, 5, gotAgent.Config["direct_iterations"])
	require.Equal(t, "success", gotAgent.Config["direct_stop_reason"])
}

func TestWorkerImageUnavailableFailsSubtaskAfterOneBoundedSpawn(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, bare := workerSpawnTestRig(t)
	parentBranch := "feature/image-parent"
	pushTestFeatureBranch(t, bare, parentBranch)
	parent := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Status: model.StatusInProgress,
		WorktreeBranch: parentBranch,
	}
	child := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parent.ID,
		Title: "child", Description: "needs worker", Status: model.StatusBacklog,
	}
	require.NoError(t, o.db.Create(&parent).Error)
	require.NoError(t, o.db.Create(&child).Error)
	fake.spawnResults = []spawnOutcome{{err: errors.New("rpc SpawnWorker: worker image unavailable: registry offline")}}

	err := o.dispatchSubtaskViaSpawner(&child, model.AgentCoder)
	require.ErrorIs(t, err, errWorkerImageUnavailable)
	require.Len(t, fake.spawnCalls, 1)
	var got model.Task
	require.NoError(t, o.db.First(&got, "id = ?", child.ID).Error)
	require.Equal(t, model.StatusFailed, got.Status)
	var evt model.TaskEvent
	require.NoError(t, o.db.First(&evt, "task_id = ? AND event_type = ?", child.ID, "worker_spawn_failed").Error)
	require.Equal(t, spawnPolicyReasonImageUnavailable, evt.Details["reason"])
}

func TestCleanupTaskWorkerBranchDeletesOwnedContainerChildRef(t *testing.T) {
	o, _, bare := workerSpawnTestRig(t)
	parent := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Status: model.StatusInProgress,
		WorktreeBranch: "feature/parent",
	}
	child := model.Task{
		ID: uuid.New(), ProjectID: o.projectID, ParentTaskID: &parent.ID,
		Title: "child", Status: model.StatusDone,
	}
	require.NoError(t, o.db.Create(&parent).Error)
	require.NoError(t, o.db.Create(&child).Error)
	branch := "feature/" + child.ID.String()[:8] + "-child"
	require.NoError(t, gitref.EnsureBranch(context.Background(), bare, branch, "main"))
	ref := gitref.BranchRef{
		BareRepoPath: bare, Project: o.projectName, TaskID: child.ID.String(),
		AgentType: string(model.AgentCoder), Branch: branch, Status: gitref.StatusActive,
	}
	require.NoError(t, o.GitrefRegistry.Register(context.Background(), &ref))

	require.NoError(t, o.cleanupTaskWorkerBranch(context.Background(), &child, branch))
	_, err := gitexec.RunGit(context.Background(), bare, "rev-parse", "--verify", "refs/heads/"+branch)
	require.Error(t, err)
	got, err := o.GitrefRegistry.Get(context.Background(), ref.ID)
	require.NoError(t, err)
	require.Equal(t, gitref.StatusDeleted, got.Status)
}

func TestCleanupTaskWorkerBranchPreservesTopLevelDeliverable(t *testing.T) {
	o, _, bare := workerSpawnTestRig(t)
	task := model.Task{ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Status: model.StatusIntegrationReady}
	branch := "feature/" + task.ID.String()[:8] + "-parent"
	require.NoError(t, gitref.EnsureBranch(context.Background(), bare, branch, "main"))
	require.NoError(t, o.cleanupTaskWorkerBranch(context.Background(), &task, branch))
	_, err := gitexec.RunGit(context.Background(), bare, "rev-parse", "--verify", "refs/heads/"+branch)
	require.NoError(t, err)
}
