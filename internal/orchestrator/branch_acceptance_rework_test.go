package orchestrator

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

type testContractReworkLauncher struct {
	db       *gorm.DB
	launches int
}

func (l *testContractReworkLauncher) Launch(_ context.Context, task *model.Task, role model.AgentType) (*WorkerLaunch, error) {
	l.launches++
	agentID := uuid.New()
	agent := model.Agent{
		ID: agentID, ProjectID: task.ProjectID, AgentType: role, Name: "contract-rework",
		Status: model.AgentWorking, CurrentTaskID: &task.ID, WorktreeBranch: task.WorktreeBranch,
	}
	if err := l.db.Create(&agent).Error; err != nil {
		return nil, err
	}
	task.AssignedAgentID = &agentID
	if err := l.db.Save(task).Error; err != nil {
		return nil, err
	}
	return &WorkerLaunch{TaskID: task.ID, AgentID: agentID, AgentType: role}, nil
}

func (*testContractReworkLauncher) LaunchMerge(context.Context, *model.Task) (*MergeResult, error) {
	return nil, nil
}

func (*testContractReworkLauncher) DestroyForTask(context.Context, *model.Task) error { return nil }

func TestRejectWorkerBranchCompletionRetriesTestContractOnce(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uuid.New()
	task := &model.Task{
		ID: uuid.New(), ProjectID: projectID, Title: "contract test", Description: "x",
		Status: model.StatusInProgress, Phase: "test", WorktreeBranch: "feature/contract-test",
		Context: model.JSONField{
			"branch_acceptance": map[string]any{
				"accepted": false,
				"rejected": []any{map[string]any{
					"path": "divideAtTransients", "status": "compile_missing_symbol", "reason": "missing_active_contract_assertion",
				}},
			},
		},
	}
	agent := &model.Agent{
		ID: uuid.New(), ProjectID: projectID, AgentType: model.AgentCoder, Name: "first",
		Status: model.AgentWorking, CurrentTaskID: &task.ID, WorktreeBranch: task.WorktreeBranch,
	}
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.Create(agent).Error)

	launcher := &testContractReworkLauncher{db: db}
	o := testOrchestrator(t, db, nil)
	o.SetWorkerLaunchService(launcher)
	require.NoError(t, o.rejectWorkerBranchCompletion(context.Background(), agent, task))
	require.Equal(t, 1, launcher.launches)

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.NotEqual(t, agent.ID, *reloaded.AssignedAgentID)
	require.NotContains(t, reloaded.Context, "branch_acceptance")
	require.Contains(t, reloaded.Context["prompt_adjustment"], "divideAtTransients")
	require.Equal(t, failureClassTestContract, reloaded.Context["latest_failure_type"])
	require.Equal(t, false, reloaded.Context["latest_failure_retry_exhausted"])

	priorAttempt := &model.WorkerAttempt{AgentID: &agent.ID}
	require.True(t, testContractReworkDispatched(&reloaded, priorAttempt))

	var replacement model.Agent
	require.NoError(t, db.First(&replacement, "id = ?", *reloaded.AssignedAgentID).Error)
	reloaded.Context["branch_acceptance"] = map[string]any{
		"accepted": false,
		"rejected": []any{map[string]any{
			"path": "divideAtTransients", "status": "compile_missing_symbol", "reason": "missing_active_contract_assertion",
		}},
	}
	require.NoError(t, db.Save(&reloaded).Error)
	require.NoError(t, o.rejectWorkerBranchCompletion(context.Background(), &replacement, &reloaded))
	require.Equal(t, 1, launcher.launches, "the bounded repair must not launch a second replacement")

	require.NoError(t, db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, failureClassTestContract, reloaded.Context["failure_class"])
	require.Equal(t, true, reloaded.Context["latest_failure_retry_exhausted"])
}

func TestTestContractOnlyRejectionsExcludeScopeViolations(t *testing.T) {
	task := &model.Task{Phase: "test", Context: model.JSONField{
		"branch_acceptance": map[string]any{
			"accepted": false,
			"rejected": []any{
				map[string]any{"path": "divideAtTransients", "reason": "missing_active_contract_assertion"},
				map[string]any{"path": "CMakeLists.txt", "reason": "outside_accepted_scope"},
			},
		},
	}}
	_, ok := testContractOnlyRejections(task)
	require.False(t, ok, "scope contamination must fail closed instead of entering contract rework")
}
