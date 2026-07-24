package orchestrator

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func deliveryFixture(t *testing.T) (*Orchestrator, model.Task, ArtifactSnapshot) {
	t.Helper()
	db := testutil.NewTestDB(t)
	bareRepo := setupTestRepoWithMainBranch(t)
	mainDir := bareRepo + "/main"
	runGitCmd(t, bareRepo, "worktree", "add", mainDir, "main")
	runGitCmd(t, mainDir, "config", "user.email", "test@test.com")
	runGitCmd(t, mainDir, "config", "user.name", "Test")
	featureDir := createFeatureWorktree(t, bareRepo, "exact")
	baseSHA := runGitCmd(t, featureDir, "rev-parse", "main")
	writeFile(t, featureDir, "delivery.txt", "exact delivery artifact")
	runGitCmd(t, featureDir, "add", "delivery.txt")
	runGitCmd(t, featureDir, "commit", "-m", "delivery fixture")
	commitSHA := runGitCmd(t, featureDir, "rev-parse", "HEAD")

	project := testutil.CreateProject(t, db, "delivery", bareRepo, "main")
	task := testutil.CreateTask(t, db, project.ID, "deliver exact artifact", model.StatusTestingReady)
	task.WorktreeBranch = "feature/exact"
	task.WorktreeBaseSHA = baseSHA
	require.NoError(t, db.Save(&task).Error)
	var loaded model.Task
	require.NoError(t, db.First(&loaded, "id = ?", task.ID).Error)
	now := time.Now()
	snapshot := ArtifactSnapshot{
		Branch: "feature/exact", CommitSHA: commitSHA,
		BaseBranch: "main", BaseSHA: baseSHA,
		GateWorkspaceID: "test-gate-workspace", EnvironmentFingerprint: "test-environment",
		PreliminaryEvidence: []CommandEvidence{{Command: "go test ./...", Passed: true, StartedAt: now, FinishedAt: now}},
		Actor:               "orchestrator", Source: "test",
	}
	host := NewHostManager(bareRepo, "main")
	orch := testOrchestrator(t, db, host.AsInterface())
	orch.projectID = project.ID
	return orch, loaded, snapshot
}

func TestFreezeDeliveryArtifactAtomicCAS(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	require.Equal(t, uint64(1), artifact.ArtifactVersion)

	var updated model.Task
	require.NoError(t, orch.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusVerificationReady, updated.Status)
	require.Equal(t, task.StateVersion+1, updated.StateVersion)

	var artifacts, gateRuns, events int64
	require.NoError(t, orch.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&artifacts).Error)
	require.NoError(t, orch.db.Model(&model.PreliminaryGateRun{}).Where("task_id = ?", task.ID).Count(&gateRuns).Error)
	require.NoError(t, orch.db.Model(&model.TaskEvent{}).Where("task_id = ? AND new_value = ?", task.ID, model.StatusVerificationReady).Count(&events).Error)
	require.EqualValues(t, 1, artifacts)
	require.EqualValues(t, 1, gateRuns)
	require.EqualValues(t, 1, events)
	var gateRun model.PreliminaryGateRun
	require.NotNil(t, artifact.PreliminaryGateRunID)
	require.NoError(t, orch.db.First(&gateRun, "id = ?", *artifact.PreliminaryGateRunID).Error)
	require.Equal(t, model.PreliminaryGatePassed, gateRun.Outcome)
	require.Equal(t, snapshot.CommitSHA, gateRun.CommitSHA)

	_, err = orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.Error(t, err)
	require.NoError(t, orch.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&artifacts).Error)
	require.NoError(t, orch.db.Model(&model.PreliminaryGateRun{}).Where("task_id = ?", task.ID).Count(&gateRuns).Error)
	require.EqualValues(t, 1, artifacts)
	require.EqualValues(t, 1, gateRuns)
}

func TestFreezeDeliveryArtifactConcurrentExactlyOnce(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
	var count int64
	require.NoError(t, orch.db.Model(&model.DeliveryArtifact{}).Where("task_id = ?", task.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestRecordPreliminaryGateFailureRoutesWithoutFixer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome model.PreliminaryGateOutcome
		want    model.TaskStatus
	}{
		{name: "code returns to implementation", outcome: model.PreliminaryGateCodeFailure, want: model.StatusInProgress},
		{name: "configuration parks", outcome: model.PreliminaryGateConfiguration, want: model.StatusPaused},
		{name: "infrastructure parks", outcome: model.PreliminaryGateInfraFailure, want: model.StatusPaused},
		{name: "timeout parks", outcome: model.PreliminaryGateTimeout, want: model.StatusPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			project := testutil.CreateProject(t, db, "gate-failure", t.TempDir(), "main")
			task := testutil.CreateTask(t, db, project.ID, "gate failure", model.StatusTestingReady)
			o := testOrchestrator(t, db, &FakeWorktreeManager{})
			now := time.Now()
			gate := deliveryGateResult{
				Candidate: acceptedDeliveryCandidate{
					Branch: "feature/gate", CommitSHA: strings.Repeat("a", 40),
					BaseBranch: "main", BaseSHA: strings.Repeat("b", 40),
				},
				WorkspaceID: "drem-delivery-gate-test", EnvironmentFingerprint: "test-environment",
				Evidence: CommandEvidence{Command: "test command", ExitCode: 1, Output: "failed", StartedAt: now, FinishedAt: now},
				Outcome:  tc.outcome,
			}

			require.NoError(t, o.RecordPreliminaryGateFailure(task.ID, gate))
			var updated model.Task
			require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
			require.Equal(t, tc.want, updated.Status)
			var runs []model.PreliminaryGateRun
			require.NoError(t, db.Where("task_id = ?", task.ID).Find(&runs).Error)
			require.Len(t, runs, 1)
			require.Equal(t, tc.outcome, runs[0].Outcome)

			require.Error(t, o.RecordPreliminaryGateFailure(task.ID, gate))
			require.NoError(t, db.Where("task_id = ?", task.ID).Find(&runs).Error)
			require.Len(t, runs, 1, "stale replay must roll back its gate row")
		})
	}
}

func TestTransitionTaskAtomicConcurrentExactlyOnce(t *testing.T) {
	orch, task, _ := deliveryFixture(t)
	first := task
	second := task

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, candidate := range []*model.Task{&first, &second} {
		wg.Add(1)
		go func(candidate *model.Task) {
			defer wg.Done()
			errs <- orch.transitionTaskAtomic(candidate, model.StatusVerificationReady,
				"orchestrator", "test", "claim once", nil)
		}(candidate)
	}
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)

	var updated model.Task
	require.NoError(t, orch.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusVerificationReady, updated.Status)
	require.Equal(t, task.StateVersion+1, updated.StateVersion)
	var events int64
	require.NoError(t, orch.db.Model(&model.TaskEvent{}).
		Where("task_id = ? AND new_value = ?", task.ID, model.StatusVerificationReady).Count(&events).Error)
	require.EqualValues(t, 1, events)
}

func TestTransitionTaskAtomicRestoresStaleCaller(t *testing.T) {
	orch, task, _ := deliveryFixture(t)
	winner := task
	stale := task
	require.NoError(t, orch.transitionTaskAtomic(&winner, model.StatusVerificationReady,
		"orchestrator", "test", "winner", nil))

	err := orch.transitionTaskAtomic(&stale, model.StatusVerificationReady,
		"orchestrator", "test", "stale", nil)
	require.Error(t, err)
	require.Equal(t, task.Status, stale.Status)
	require.Equal(t, task.StateVersion, stale.StateVersion)
	require.Equal(t, task.UpdatedAt, stale.UpdatedAt)
}

func TestClaimInProgressSubtaskConcurrentExactlyOnce(t *testing.T) {
	orch, parent, _ := deliveryFixture(t)
	child := testutil.CreateTask(t, orch.db, parent.ProjectID, "recovered child", model.StatusInProgress)
	child.ParentTaskID = &parent.ID
	require.NoError(t, orch.db.Save(&child).Error)
	var loaded model.Task
	require.NoError(t, orch.db.First(&loaded, "id = ?", child.ID).Error)
	first := loaded
	second := loaded
	firstAgent := uuid.New()
	secondAgent := uuid.New()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, claim := range []struct {
		task  *model.Task
		agent uuid.UUID
	}{{&first, firstAgent}, {&second, secondAgent}} {
		wg.Add(1)
		go func(candidate *model.Task, agentID uuid.UUID) {
			defer wg.Done()
			errs <- orch.db.Transaction(func(tx *gorm.DB) error {
				return casClaimInProgressSubtask(tx, candidate, agentID, "orchestrator", "test")
			})
		}(claim.task, claim.agent)
	}
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
	require.NoError(t, orch.db.First(&loaded, "id = ?", child.ID).Error)
	require.NotNil(t, loaded.AssignedAgentID)
	require.Contains(t, []uuid.UUID{firstAgent, secondAgent}, *loaded.AssignedAgentID)
	require.Equal(t, child.StateVersion+1, loaded.StateVersion)
	var events int64
	require.NoError(t, orch.db.Model(&model.TaskEvent{}).
		Where("task_id = ? AND event_type = ?", child.ID, "subtask_claimed").Count(&events).Error)
	require.EqualValues(t, 1, events)
}

func TestVerifyAndAuthorizeDeliveryExactEvidence(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	var verificationReady model.Task
	require.NoError(t, orch.db.First(&verificationReady, "id = ?", task.ID).Error)

	verifyReq := VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: verificationReady.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "codex:thread-1", Source: "test", EnvironmentFingerprint: "macos-arm64:xcode",
		CommandEvidence: snapshot.PreliminaryEvidence, BinarySHA256: strings.Repeat("c", 64),
		Result: model.VerificationPassed, Notes: "native app passed", IdempotencyKey: "verify-once",
	}
	record, err := orch.VerifyDelivery(verifyReq)
	require.NoError(t, err)
	replayed, err := orch.VerifyDelivery(verifyReq)
	require.NoError(t, err)
	require.Equal(t, record.ID, replayed.ID)

	changed := verifyReq
	changed.Notes = "different payload"
	_, err = orch.VerifyDelivery(changed)
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	var integrationReady model.Task
	require.NoError(t, orch.db.First(&integrationReady, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusIntegrationReady, integrationReady.Status)

	auth, err := orch.AuthorizeIntegration(IntegrateDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: integrationReady.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		VerificationRecordID: record.ID, Actor: "codex:thread-2", Source: "test", IdempotencyKey: "integrate-once",
	})
	require.NoError(t, err)
	require.Equal(t, record.ID, auth.VerificationRecordID)
	var merging model.Task
	require.NoError(t, orch.db.First(&merging, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusMerging, merging.Status)
	var intent model.MergeIntent
	require.NoError(t, orch.db.First(&intent, "integration_authorization_id = ?", auth.ID).Error)
	require.Equal(t, artifact.CommitSHA, intent.ArtifactCommitSHA)
	require.Equal(t, artifact.BaseSHA, intent.TargetBaseSHA)

	completion, err := orch.completeAuthorizedMerge(task.ID, intent.ID, strings.Repeat("d", 40), "test")
	require.NoError(t, err)
	require.Equal(t, artifact.ID, completion.DeliveryArtifactID)
	require.Equal(t, record.ID, completion.VerificationRecordID)
	require.Equal(t, auth.ID, completion.IntegrationAuthorizationID)
	require.Equal(t, intent.ID, completion.MergeIntentID)
	var done model.Task
	require.NoError(t, orch.db.First(&done, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusDone, done.Status)
}

func TestAuthorizeIntegrationRejectsGitDriftAndReturnsToImplementation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantReason string
		drift      func(*testing.T, *Orchestrator)
	}{
		{
			name:       "delivery commit",
			wantReason: "delivery_commit_drift",
			drift: func(t *testing.T, orch *Orchestrator) {
				featureDir := orch.resolveIntegrationWorktree(&model.Task{WorktreeBranch: "feature/exact"})
				writeFile(t, featureDir, "delivery.txt", "changed after verification")
				runGitCmd(t, featureDir, "add", "delivery.txt")
				runGitCmd(t, featureDir, "commit", "-m", "drift delivery branch")
			},
		},
		{
			name:       "target branch",
			wantReason: "target_drift_requires_reverification",
			drift: func(t *testing.T, orch *Orchestrator) {
				mainDir, err := orch.worktree.MainWorktreePath()
				require.NoError(t, err)
				writeFile(t, mainDir, "target-drift.txt", "default branch advanced")
				runGitCmd(t, mainDir, "add", "target-drift.txt")
				runGitCmd(t, mainDir, "commit", "-m", "advance target branch")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orch, task, snapshot := deliveryFixture(t)
			artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
			require.NoError(t, err)
			var current model.Task
			require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			verification, err := orch.VerifyDelivery(VerifyDeliveryRequest{
				TaskID: task.ID, ObservedStateVersion: current.StateVersion,
				ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
				Actor: "codex:verifier", Source: "test", EnvironmentFingerprint: "macos-arm64",
				CommandEvidence: snapshot.PreliminaryEvidence, Result: model.VerificationPassed,
				IdempotencyKey: "verify-before-" + tc.wantReason,
			})
			require.NoError(t, err)
			require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			tc.drift(t, orch)

			_, err = orch.AuthorizeIntegration(IntegrateDeliveryRequest{
				TaskID: task.ID, ObservedStateVersion: current.StateVersion,
				ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
				VerificationRecordID: verification.ID, Actor: "codex:integrator", Source: "test",
				IdempotencyKey: "integrate-after-" + tc.wantReason,
			})
			require.ErrorIs(t, err, ErrStaleArtifact)

			require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			require.Equal(t, model.StatusInProgress, current.Status)
			var invalidated model.DeliveryArtifact
			require.NoError(t, orch.db.First(&invalidated, "id = ?", artifact.ID).Error)
			require.NotNil(t, invalidated.InvalidatedAt)
			require.Equal(t, tc.wantReason, invalidated.InvalidationReason)
			var authorizations int64
			require.NoError(t, orch.db.Model(&model.IntegrationAuthorization{}).
				Where("task_id = ?", task.ID).Count(&authorizations).Error)
			require.Zero(t, authorizations)
		})
	}
}

func TestVerifyDeliveryRejectsStaleCommitWithoutMutation(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	_, err = orch.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: strings.Repeat("d", 40),
		Actor: "codex:stale", Source: "test", EnvironmentFingerprint: "macos-arm64",
		CommandEvidence: snapshot.PreliminaryEvidence, Result: model.VerificationPassed, IdempotencyKey: uuid.NewString(),
	})
	require.ErrorIs(t, err, ErrStaleArtifact)
	var count int64
	require.NoError(t, orch.db.Model(&model.VerificationRecord{}).Where("task_id = ?", task.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestFailedVerificationRetainedAndArtifactInvalidated(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	repair := testutil.CreateTask(t, orch.db, task.ProjectID, "validate assembled artifact", model.StatusDone)
	repair.ParentTaskID = &task.ID
	repair.Phase = "integration"
	repair.WorktreeBranch = "feature/completed-integration"
	repair.Context = model.JSONField{"estimated_files": []any{"src/ui/LowerZoneLayout.h"}}
	require.NoError(t, orch.db.Save(&repair).Error)
	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	now := time.Now()
	record, err := orch.VerifyDelivery(VerifyDeliveryRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "codex:verifier", Source: "test", EnvironmentFingerprint: "macos-arm64",
		CommandEvidence: []CommandEvidence{{Command: "scripts/dev verify", Passed: false, ExitCode: 1, StartedAt: now, FinishedAt: now}},
		Result:          model.VerificationFailed, Notes: "GUI regression",
		FailureMode: model.DeliveryReworkOrchestrated, FailureReason: "native compile failed",
		IdempotencyKey: "failed-verification",
	})
	require.NoError(t, err)
	require.Equal(t, model.VerificationFailed, record.Result)
	var updated model.Task
	require.NoError(t, orch.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, updated.Status)
	require.NoError(t, orch.db.First(&repair, "id = ?", repair.ID).Error)
	require.Equal(t, model.StatusDone, repair.Status, "completed owner remains immutable audit history")
	var repairChild model.Task
	require.NoError(t, orch.db.Where("parent_task_id = ? AND status = ?", task.ID, model.StatusBacklog).First(&repairChild).Error)
	require.Equal(t, repair.ID.String(), repairChild.Context[deliveryReworkSourceTaskKey])
	require.Contains(t, repairChild.Context["prompt_adjustment"], "native compile failed")
	require.Equal(t, true, repairChild.Context["delivery_rework_pending"])
	require.Equal(t, true, repairChild.Context["skip_existing_work_dedup"])
	require.Contains(t, repairChild.WorktreeBranch, "-rework-")
	var invalidated model.DeliveryArtifact
	require.NoError(t, orch.db.First(&invalidated, "id = ?", artifact.ID).Error)
	require.NotNil(t, invalidated.InvalidatedAt)
}

func TestOrchestratedDeliveryReworkCreatesDependencyOrderedScopedRepairs(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)

	modelTest := testutil.CreateTask(t, orch.db, task.ProjectID, "write lane cycling contract", model.StatusDone)
	modelTest.ParentTaskID = &task.ID
	modelTest.Phase = "test"
	modelTest.Priority = 3
	modelTest.Context = model.JSONField{
		"estimated_files": []any{"tests/integration/LaneVersionCommandTest.cpp"},
		"writable_files":  []any{"tests/integration/LaneVersionCommandTest.cpp"},
	}
	require.NoError(t, orch.db.Save(&modelTest).Error)

	action := testutil.CreateTask(t, orch.db, task.ProjectID, "implement take cycling actions", model.StatusDone)
	action.ParentTaskID = &task.ID
	action.Phase = "implementation"
	action.Priority = 2
	action.DependencyIDs = model.JSONArray{modelTest.ID.String()}
	action.Context = model.JSONField{
		"estimated_files": []any{"src/ui/ActionCoordinatorHandlers.cpp", "src/ui/ActionCoordinatorRegistration.cpp"},
		"writable_files":  []any{"src/ui/ActionCoordinatorHandlers.cpp", "src/ui/ActionCoordinatorRegistration.cpp"},
	}
	require.NoError(t, orch.db.Save(&action).Error)
	modelTest.TestsFor = model.JSONArray{action.ID.String()}
	require.NoError(t, orch.db.Save(&modelTest).Error)

	integration := testutil.CreateTask(t, orch.db, task.ProjectID, "wire take cycling keymap", model.StatusDone)
	integration.ParentTaskID = &task.ID
	integration.Phase = "integration"
	integration.Priority = 1
	integration.DependencyIDs = model.JSONArray{action.ID.String()}
	integration.Context = model.JSONField{
		"estimated_files": []any{
			"tests/integration/LaneVersionCommandTest.cpp",
			"src/ui/ActionCoordinatorHandlers.cpp",
			"src/ui/ActionCoordinatorRegistration.cpp",
			"config/default_keymap.yaml",
		},
		"writable_files": []any{"config/default_keymap.yaml"},
	}
	require.NoError(t, orch.db.Save(&integration).Error)

	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	_, err = orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
		TaskID: current.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "codex:verifier", Source: "test",
		Reason: "test APIs do not compile; action APIs are undeclared; config/default_keymap.yaml is missing Alt+j/Alt+k",
		Mode:   model.DeliveryReworkOrchestrated, IdempotencyKey: "multi-owner-delivery-rework",
	})
	require.NoError(t, err)

	var children []model.Task
	require.NoError(t, orch.db.Where("parent_task_id = ?", task.ID).Find(&children).Error)
	repairs := map[string]model.Task{}
	for _, child := range children {
		if source, ok := child.Context[deliveryReworkSourceTaskKey].(string); ok {
			repairs[source] = child
		}
	}
	require.Len(t, repairs, 3)
	testRepair := repairs[modelTest.ID.String()]
	actionRepair := repairs[action.ID.String()]
	integrationRepair := repairs[integration.ID.String()]
	require.Equal(t, []string{"tests/integration/LaneVersionCommandTest.cpp"}, extractWritableFiles(testRepair))
	require.Equal(t, []string{"src/ui/ActionCoordinatorHandlers.cpp", "src/ui/ActionCoordinatorRegistration.cpp"}, extractWritableFiles(actionRepair))
	require.Equal(t, []string{"config/default_keymap.yaml"}, extractWritableFiles(integrationRepair),
		"integration must not inherit read/merge paths as mutation authority")
	require.Equal(t, model.JSONArray{testRepair.ID.String()}, actionRepair.DependencyIDs)
	require.Equal(t, model.JSONArray{actionRepair.ID.String()}, testRepair.TestsFor)
	require.Equal(t, model.JSONArray{actionRepair.ID.String()}, integrationRepair.DependencyIDs)
	var scopedEvents int64
	require.NoError(t, orch.db.Model(&model.TaskEvent{}).
		Where("event_type = ? AND task_id IN ?", "delivery_rework_scoped", []uuid.UUID{testRepair.ID, actionRepair.ID, integrationRepair.ID}).
		Count(&scopedEvents).Error)
	require.EqualValues(t, 3, scopedEvents)

	met, err := DependenciesMet(orch.db, actionRepair.DependencyIDs)
	require.NoError(t, err)
	require.False(t, met)
	met, err = DependenciesMet(orch.db, integrationRepair.DependencyIDs)
	require.NoError(t, err)
	require.False(t, met)

	require.NoError(t, orch.db.Model(&model.Task{}).Where("id = ?", testRepair.ID).Update("status", model.StatusDone).Error)
	met, err = DependenciesMet(orch.db, actionRepair.DependencyIDs)
	require.NoError(t, err)
	require.True(t, met)
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.NoError(t, orch.checkFeatureCompletion(&current))
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, current.Status)

	require.NoError(t, orch.db.Model(&model.Task{}).Where("id = ?", actionRepair.ID).Update("status", model.StatusDone).Error)
	met, err = DependenciesMet(orch.db, integrationRepair.DependencyIDs)
	require.NoError(t, err)
	require.True(t, met)
	require.NoError(t, orch.checkFeatureCompletion(&current))
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, current.Status)

	require.NoError(t, orch.db.Model(&model.Task{}).Where("id = ?", integrationRepair.ID).Update("status", model.StatusDone).Error)
	require.NoError(t, orch.checkFeatureCompletion(&current))
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, current.Status)
	refrozen, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	require.Equal(t, uint64(2), refrozen.ArtifactVersion)
}

func TestOrchestratedDeliveryReworkSingleOwnerCompatibilityAndNoOwnerFailure(t *testing.T) {
	t.Run("single owner", func(t *testing.T) {
		orch, task, snapshot := deliveryFixture(t)
		owner := testutil.CreateTask(t, orch.db, task.ProjectID, "repair action", model.StatusDone)
		owner.ParentTaskID = &task.ID
		owner.Phase = "implementation"
		owner.Context = model.JSONField{"writable_files": []any{"src/action.cpp"}}
		require.NoError(t, orch.db.Save(&owner).Error)
		artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
		require.NoError(t, err)
		var current model.Task
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		_, err = orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion,
			ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
			Actor: "codex:verifier", Source: "test", Reason: "compile failure",
			Mode: model.DeliveryReworkOrchestrated, IdempotencyKey: "single-owner-rework",
		})
		require.NoError(t, err)
		var repair model.Task
		require.NoError(t, orch.db.Where("parent_task_id = ? AND status = ?", task.ID, model.StatusBacklog).First(&repair).Error)
		require.Equal(t, owner.ID.String(), repair.Context[deliveryReworkSourceTaskKey])
		require.Equal(t, []string{"src/action.cpp"}, extractWritableFiles(repair))
	})

	t.Run("no completed owner fails atomically", func(t *testing.T) {
		orch, task, snapshot := deliveryFixture(t)
		artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
		require.NoError(t, err)
		var current model.Task
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		_, err = orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
			TaskID: task.ID, ObservedStateVersion: current.StateVersion,
			ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
			Actor: "codex:verifier", Source: "test", Reason: "compile failure",
			Mode: model.DeliveryReworkOrchestrated, IdempotencyKey: "missing-owner-rework",
		})
		require.ErrorContains(t, err, "no completed test, implementation, or integration subtask")
		require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
		require.Equal(t, model.StatusVerificationReady, current.Status)
		var invalidated model.DeliveryArtifact
		require.NoError(t, orch.db.First(&invalidated, "id = ?", artifact.ID).Error)
		require.Nil(t, invalidated.InvalidatedAt)
	})
}

func TestVerifyDeliveryRejectsContradictoryOrMalformedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*VerifyDeliveryRequest)
	}{
		{
			name: "passing result with failed command",
			mutate: func(req *VerifyDeliveryRequest) {
				req.CommandEvidence[0].Passed = false
				req.CommandEvidence[0].ExitCode = 1
			},
		},
		{
			name: "missing command timestamps",
			mutate: func(req *VerifyDeliveryRequest) {
				req.CommandEvidence[0].StartedAt = time.Time{}
			},
		},
		{
			name: "invalid binary hash",
			mutate: func(req *VerifyDeliveryRequest) {
				req.BinarySHA256 = "not-a-sha256"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orch, task, snapshot := deliveryFixture(t)
			artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
			require.NoError(t, err)
			var current model.Task
			require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			req := VerifyDeliveryRequest{
				TaskID: task.ID, ObservedStateVersion: current.StateVersion,
				ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
				Actor: "codex:evidence-audit", Source: "test", EnvironmentFingerprint: "macos-arm64",
				CommandEvidence: append([]CommandEvidence(nil), snapshot.PreliminaryEvidence...),
				Result:          model.VerificationPassed, IdempotencyKey: "invalid-evidence-" + tc.name,
			}
			tc.mutate(&req)
			_, err = orch.VerifyDelivery(req)
			require.Error(t, err)

			var recordCount int64
			require.NoError(t, orch.db.Model(&model.VerificationRecord{}).
				Where("task_id = ?", task.ID).Count(&recordCount).Error)
			require.Zero(t, recordCount)
			require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			require.Equal(t, model.StatusVerificationReady, current.Status)
		})
	}
}

func TestRequestDeliveryReworkIsExactAtomicAndIdempotent(t *testing.T) {
	for _, afterVerification := range []bool{false, true} {
		name := "verification_ready"
		if afterVerification {
			name = "integration_ready"
		}
		t.Run(name, func(t *testing.T) {
			orch, task, snapshot := deliveryFixture(t)
			artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
			require.NoError(t, err)
			repair := testutil.CreateTask(t, orch.db, task.ProjectID, "repair verified artifact", model.StatusDone)
			repair.ParentTaskID = &task.ID
			repair.Phase = "implementation"
			require.NoError(t, orch.db.Save(&repair).Error)
			var current model.Task
			require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			if afterVerification {
				_, err = orch.VerifyDelivery(VerifyDeliveryRequest{
					TaskID: task.ID, ObservedStateVersion: current.StateVersion,
					ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
					Actor: "codex:verifier", Source: "test", EnvironmentFingerprint: "macos-arm64",
					CommandEvidence: snapshot.PreliminaryEvidence, Result: model.VerificationPassed,
					IdempotencyKey: "verify-before-rework",
				})
				require.NoError(t, err)
				require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
			}
			req := RequestDeliveryReworkRequest{
				TaskID: task.ID, ObservedStateVersion: current.StateVersion,
				ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
				Actor: "codex:reviewer", Source: "test", Reason: "native behavior differs",
				Mode:           model.DeliveryReworkOrchestrated,
				IdempotencyKey: "rework-" + name,
			}
			record, err := orch.RequestDeliveryRework(req)
			require.NoError(t, err)
			replay, err := orch.RequestDeliveryRework(req)
			require.NoError(t, err)
			require.Equal(t, record.ID, replay.ID)

			var updated model.Task
			require.NoError(t, orch.db.First(&updated, "id = ?", task.ID).Error)
			require.Equal(t, model.StatusInProgress, updated.Status)
			var invalidated model.DeliveryArtifact
			require.NoError(t, orch.db.First(&invalidated, "id = ?", artifact.ID).Error)
			require.Equal(t, "rework_requested", invalidated.InvalidationReason)
			require.NotNil(t, invalidated.InvalidatedAt)

			changed := req
			changed.Reason = "different"
			_, err = orch.RequestDeliveryRework(changed)
			require.ErrorIs(t, err, ErrIdempotencyConflict)
		})
	}
}

func TestRequestDeliveryReworkStagesQuickFixCorrectionWorker(t *testing.T) {
	orch, task, snapshot := deliveryFixture(t)
	task.Category = model.CategoryQuickFix
	require.NoError(t, orch.db.Save(&task).Error)

	artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
	require.NoError(t, err)
	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)

	_, err = orch.RequestDeliveryRework(RequestDeliveryReworkRequest{
		TaskID: task.ID, ObservedStateVersion: current.StateVersion,
		ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
		Actor: "codex:reviewer", Source: "test", Reason: "move the requested tab instead",
		Mode: model.DeliveryReworkOrchestrated, IdempotencyKey: "quickfix-correction",
	})
	require.NoError(t, err)

	require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, current.Status)
	require.Equal(t, true, current.Context[quickFixDeliveryReworkPendingKey])
	require.Equal(t, "move the requested tab instead", current.Context["prompt_adjustment"])
}

func TestDeliveryPolicyMatrixStopsAtTheConfiguredBoundary(t *testing.T) {
	for _, verificationPolicy := range []model.VerificationPolicy{
		model.VerificationExternalAck,
		model.VerificationLocalAutomated,
	} {
		for _, integrationPolicy := range []model.IntegrationPolicy{
			model.IntegrationPrepareBranch,
			model.IntegrationAutoMerge,
		} {
			name := string(verificationPolicy) + "/" + string(integrationPolicy)
			t.Run(name, func(t *testing.T) {
				orch, task, snapshot := deliveryFixture(t)
				require.NoError(t, orch.SetDeliveryPolicyConfig(DeliveryPolicyConfig{
					VerificationPolicy: verificationPolicy,
					IntegrationPolicy:  integrationPolicy,
				}))
				artifact, err := orch.FreezeDeliveryArtifact(task.ID, snapshot)
				require.NoError(t, err)

				var current model.Task
				require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
				require.NoError(t, orch.processVerificationReady(&current))
				require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)

				var verification model.VerificationRecord
				if verificationPolicy == model.VerificationExternalAck {
					require.Equal(t, model.StatusVerificationReady, current.Status)
					verificationPtr, err := orch.VerifyDelivery(VerifyDeliveryRequest{
						TaskID: task.ID, ObservedStateVersion: current.StateVersion,
						ArtifactVersion: artifact.ArtifactVersion, CommitSHA: artifact.CommitSHA,
						Actor: "codex:matrix-verifier", Source: "test", EnvironmentFingerprint: "macos-arm64",
						CommandEvidence: snapshot.PreliminaryEvidence, Result: model.VerificationPassed,
						IdempotencyKey: "matrix-external-verification",
					})
					require.NoError(t, err)
					verification = *verificationPtr
				} else {
					require.Equal(t, model.StatusIntegrationReady, current.Status)
					require.NoError(t, orch.db.Where("delivery_artifact_id = ?", artifact.ID).First(&verification).Error)
					require.Equal(t, "orchestrator", verification.VerifierActor)
				}

				require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)
				require.Equal(t, model.StatusIntegrationReady, current.Status)
				require.NoError(t, orch.processIntegrationReady(&current))
				require.NoError(t, orch.db.First(&current, "id = ?", task.ID).Error)

				var authorizationCount int64
				require.NoError(t, orch.db.Model(&model.IntegrationAuthorization{}).
					Where("delivery_artifact_id = ?", artifact.ID).Count(&authorizationCount).Error)
				if integrationPolicy == model.IntegrationPrepareBranch {
					require.Equal(t, model.StatusIntegrationReady, current.Status)
					require.Zero(t, authorizationCount)
				} else {
					require.Equal(t, model.StatusMerging, current.Status)
					require.EqualValues(t, 1, authorizationCount)
					var authorization model.IntegrationAuthorization
					require.NoError(t, orch.db.Where("delivery_artifact_id = ?", artifact.ID).First(&authorization).Error)
					require.Equal(t, verification.ID, authorization.VerificationRecordID)
					require.Equal(t, "orchestrator", authorization.Actor)
				}
			})
		}
	}
}
