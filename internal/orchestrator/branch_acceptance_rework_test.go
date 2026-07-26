package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRejectedTestContractPersistsAcceptanceForBoundedRework(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bare)
	featureDir := t.TempDir()
	workerBranch := "feature/test-contract-persistence"
	testutil.AddWorktree(t, bare, workerBranch, featureDir)
	testutil.CommitFile(t, featureDir, "marker_command_test.cpp",
		"TEST_CASE(\"marker command red checkpoint\") { CHECK(true); }\n", "write incomplete red checkpoint")

	db := testutil.NewTestDB(t)
	o := testOrchestrator(t, db, &FakeWorktreeManager{BarePath: bare, Default: defaultBranch})
	task := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "marker command test", Description: "x",
		Status: model.StatusInProgress, Phase: "test", WorktreeBranch: workerBranch,
		Context: model.JSONField{
			"estimated_files":            []any{"marker_command_test.cpp"},
			"planned_interface_contract": `{"red_mode":"compile_missing_symbol","semantic_contracts":[{"kind":"cpp_function","state":"planned","signature":"void dc::EditorAdapter::markerAddWithArgs(const std::string& args)"}]}`,
		},
	}
	agent := &model.Agent{
		ID: uuid.New(), ProjectID: o.projectID, AgentType: model.AgentCoder, Name: "first",
		Status: model.AgentWorking, CurrentTaskID: &task.ID, WorktreeBranch: workerBranch,
	}
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.Create(agent).Error)

	accepted, err := o.acceptWorkerBranchCompletion(context.Background(), agent, task, featureDir)
	require.NoError(t, err)
	require.False(t, accepted)
	// Production classifies the named model.JSONField immediately. Do not
	// reload before this call: a reload normalizes the nested map and previously
	// hid the live type-assertion defect.
	launcher := &testContractReworkLauncher{db: db}
	o.SetWorkerLaunchService(launcher)
	require.NoError(t, o.rejectWorkerBranchCompletion(context.Background(), agent, task))
	require.Equal(t, 1, launcher.launches)

	var reloaded model.Task
	require.NoError(t, db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Contains(t, reloaded.Context["prompt_adjustment"], "markerAddWithArgs")
	require.True(t, strings.Contains(reloaded.WorktreeBranch, "test-contract-persistence"))
	require.Equal(t, failureClassTestContract, reloaded.Context["latest_failure_type"])
	require.Equal(t, false, reloaded.Context["latest_failure_retry_exhausted"])

	var event model.TaskEvent
	require.NoError(t, db.Where("task_id = ? AND event_type = ?", task.ID, "branch_acceptance_rejected").First(&event).Error)
	require.Equal(t, failureClassTestContract, event.Details["reason"])
}

func TestMarkerRegistryIncidentReplayDispatchesCorrectionAndReachesFrozenArtifact(t *testing.T) {
	bare := testutil.SetupBareRepo(t)
	defaultBranch := testutil.GetDefaultBranch(t, bare)
	featureDir := t.TempDir()
	workerBranch := "feature/marker-registry-incident-replay"
	testutil.AddWorktree(t, bare, workerBranch, featureDir)
	require.NoError(t, os.MkdirAll(filepath.Join(featureDir, "tests/integration"), 0o755))
	testutil.CommitFile(t, featureDir, "tests/integration/test_jump_motions.cpp", `
TEST_CASE("mark command creates named marker")
{
    f.simulateExCommand("mark \"Verse 1\"");
    CHECK(markers.getNumMarkers() == 1);
}
`, "write behavioral marker red test")

	db := testutil.NewTestDB(t)
	o := testOrchestrator(t, db, &FakeWorktreeManager{BarePath: bare, Default: defaultBranch})
	task := &model.Task{
		ID: uuid.New(), ProjectID: o.projectID, Title: "marker registry incident", Description: "x",
		Status: model.StatusInProgress, Phase: "test", WorktreeBranch: workerBranch,
		Context: model.JSONField{
			"estimated_files":            []any{"tests/integration/test_jump_motions.cpp"},
			"planned_interface_contract": `{"red_mode":"runtime_assertion","semantic_contracts":[{"kind":"registry_action","state":"planned","action_id":"marker.add"}]}`,
		},
	}
	agent := &model.Agent{
		ID: uuid.New(), ProjectID: o.projectID, AgentType: model.AgentCoder, Name: "marker-first",
		Status: model.AgentWorking, CurrentTaskID: &task.ID, WorktreeBranch: workerBranch,
	}
	task.AssignedAgentID = &agent.ID
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.Create(agent).Error)

	accepted, err := o.acceptWorkerBranchCompletion(context.Background(), agent, task, featureDir)
	require.NoError(t, err)
	require.False(t, accepted)
	launcher := &testContractReworkLauncher{db: db}
	o.SetWorkerLaunchService(launcher)
	require.NoError(t, o.rejectWorkerBranchCompletion(context.Background(), agent, task))
	require.Equal(t, 1, launcher.launches)

	testutil.CommitFile(t, featureDir, "tests/integration/test_jump_motions.cpp", `
TEST_CASE("mark command creates named marker")
{
    const auto& actions = f.engine.getActionRegistry().getAllActions();
    CHECK(std::any_of(actions.begin(), actions.end(), [](const auto& action) { return action.id == "marker.add" && static_cast<bool>(action.executeWithArgs); }));
    f.simulateExCommand("mark \"Verse 1\"");
    CHECK(markers.getNumMarkers() == 1);
}
`, "repair marker registry assertion")

	var repairedTask model.Task
	require.NoError(t, db.First(&repairedTask, "id = ?", task.ID).Error)
	var replacement model.Agent
	require.NoError(t, db.First(&replacement, "id = ?", *repairedTask.AssignedAgentID).Error)
	accepted, err = o.acceptWorkerBranchCompletion(context.Background(), &replacement, &repairedTask, featureDir)
	require.NoError(t, err)
	require.True(t, accepted)
	require.NotContains(t, repairedTask.Context, "failure_class")

	require.NoError(t, o.transitionTaskAtomic(&repairedTask, model.StatusTestingReady,
		"orchestrator", "incident_replay", "repaired marker checkpoint accepted", nil))
	commitSHA := runGitCmd(t, featureDir, "rev-parse", "HEAD")
	baseSHA := runGitCmd(t, featureDir, "rev-parse", defaultBranch)
	now := time.Unix(2, 0)
	artifact, err := o.FreezeDeliveryArtifact(task.ID, ArtifactSnapshot{
		Branch: workerBranch, CommitSHA: commitSHA, BaseBranch: defaultBranch, BaseSHA: baseSHA,
		GateWorkspaceID: "marker-v9-replay", EnvironmentFingerprint: "linux-cgo-replay",
		PreliminaryEvidence: []CommandEvidence{{
			Command: "marker registry incident replay", Passed: true, StartedAt: now, FinishedAt: now,
		}},
		Actor: "orchestrator", Source: "incident_replay",
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), artifact.ArtifactVersion)
	require.Equal(t, commitSHA, artifact.CommitSHA)
	var frozen model.Task
	require.NoError(t, db.First(&frozen, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusVerificationReady, frozen.Status)
}
