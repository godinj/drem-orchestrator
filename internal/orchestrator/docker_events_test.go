package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// dockerEventsTestRig builds an Orchestrator wired to both a fake runtime
// and a fake spawner. Tests use the runtime to emit synthetic events and
// assert on spawner calls that would respawn the dead worker. The bare
// repo is real because the respawn path (handleWorkerDeath →
// spawnCoder → spawnTypedWorker) now pre-creates the feature branch via
// gitref.EnsureBranch, which needs an actual git object database.
func dockerEventsTestRig(t *testing.T) (*Orchestrator, *container.FakeRuntime, *fakeWorkerSpawner) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	bareRepo := testutil.SetupBareRepo(t)
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "docker-events-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	rt := container.NewFakeRuntime()
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: bareRepo, Default: "main"},
		logger:         slog.Default().With("component", "docker_events_test"),
		Spawner:        fake,
		Runtime:        rt,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, rt, fake
}

// seedInFlightTask creates an in_progress task with the given feature
// branch so event-driven respawn paths have something to target.
func seedInFlightTask(t *testing.T, o *Orchestrator) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Worker death test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/worker-death",
	}
	require.NoError(t, o.db.Create(task).Error)
	return task
}

func seedAssignedWorkerAttempt(t *testing.T, o *Orchestrator, task *model.Task, agentType model.AgentType, workerID, containerID string) *model.WorkerAttempt {
	t.Helper()
	agentID := uuid.New()
	ag := &model.Agent{
		ID:             agentID,
		ProjectID:      o.projectID,
		AgentType:      agentType,
		Name:           string(agentType) + "-death-test",
		Status:         model.AgentWorking,
		CurrentTaskID:  &task.ID,
		TmuxSession:    containerID,
		WorktreeBranch: task.WorktreeBranch,
	}
	require.NoError(t, o.db.Create(ag).Error)
	task.AssignedAgentID = &agentID
	require.NoError(t, o.db.Save(task).Error)
	attempt := &model.WorkerAttempt{
		ID:          uuid.New(),
		TaskID:      task.ID,
		AgentID:     &agentID,
		WorkerID:    workerID,
		ContainerID: containerID,
		AgentType:   string(agentType),
		State:       model.WorkerAttemptRunning,
	}
	require.NoError(t, o.db.Create(attempt).Error)
	return attempt
}

func currentAssignedWorkerAttempt(t *testing.T, o *Orchestrator, taskID uuid.UUID) model.WorkerAttempt {
	t.Helper()
	var task model.Task
	require.NoError(t, o.db.First(&task, "id = ?", taskID).Error)
	require.NotNil(t, task.AssignedAgentID)
	var attempt model.WorkerAttempt
	require.NoError(t, o.db.Where("task_id = ? AND agent_id = ? AND completed_at IS NULL", taskID, *task.AssignedAgentID).
		Order("created_at DESC").First(&attempt).Error)
	return attempt
}

func workerDeathEvent(taskID uuid.UUID, attempt model.WorkerAttempt, exitCode int, oom bool) container.Event {
	return container.Event{
		Type:        container.EventDie,
		ContainerID: attempt.ContainerID,
		ExitCode:    exitCode,
		OOMKilled:   oom,
		Labels: map[string]string{
			"drem.task_id":    taskID.String(),
			"drem.worker_id":  attempt.WorkerID,
			"drem.agent_type": attempt.AgentType,
		},
	}
}

func TestShouldAutoContinueCheckpointRequiresDurableChildCheckpoint(t *testing.T) {
	parentID := uuid.New()
	baseTask := &model.Task{ParentTaskID: &parentID}
	baseAttempt := &model.WorkerAttempt{RenderedPromptPath: "/immutable/prompt", RenderedPromptHash: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name    string
		task    *model.Task
		attempt *model.WorkerAttempt
		reason  string
		want    bool
	}{
		{name: "token budget", task: baseTask, attempt: baseAttempt, reason: "token_budget", want: true},
		{name: "timeout", task: baseTask, attempt: baseAttempt, reason: "timeout", want: true},
		{name: "ordinary tool failure", task: baseTask, attempt: baseAttempt, reason: "no_progress", want: false},
		{name: "top level task has no parent", task: &model.Task{}, attempt: baseAttempt, reason: "token_budget", want: false},
		{name: "missing immutable prompt", task: baseTask, attempt: &model.WorkerAttempt{}, reason: "token_budget", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldAutoContinueCheckpoint(tc.task, tc.attempt, tc.reason))
		})
	}
}

func TestHandleWorkerDeathAutomaticallyContinuesAdmittedDecomposedCheckpoint(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	o.projectName = "canvas"
	bare := o.worktree.BareRepo()
	parentBranch := "feature/checkpoint-parent"
	parentDir := t.TempDir()
	testutil.AddWorktree(t, bare, parentBranch, parentDir)
	base := strings.TrimSpace(runGitCmd(t, parentDir, "rev-parse", "HEAD"))
	workerBranch := "feature/checkpoint-child"
	workerDir := t.TempDir()
	testutil.AddWorktree(t, bare, workerBranch, workerDir)
	testutil.CommitFile(t, workerDir, "allowed.txt", "partial\n", "checkpoint")
	childID := uuid.New()
	parent := &model.Task{ID: uuid.New(), ProjectID: o.projectID, Title: "parent", Description: "parent", Status: model.StatusInProgress, WorktreeBranch: parentBranch, WorktreeBaseSHA: base}
	require.NoError(t, o.db.Create(parent).Error)
	child := &model.Task{
		ID: childID, ProjectID: o.projectID, ParentTaskID: &parent.ID, Title: "decomposed implementation", Description: "child",
		Phase: "implementation", Status: model.StatusInProgress, WorktreeBranch: workerBranch,
		Context: model.JSONField{"estimated_files": []any{"allowed.txt"}, "writable_files": []any{"allowed.txt"}, "execution_lane": string(executionLaneDecomposed)},
	}
	require.NoError(t, o.db.Create(child).Error)
	o.worktree.(*FakeWorktreeManager).Features = map[string]string{"checkpoint-parent": parentDir}
	attempt := seedAssignedWorkerAttempt(t, o, child, model.AgentCoder, "checkpoint-worker", "checkpoint-container")
	attempt.BaseSHA = base
	attempt.Branch = workerBranch
	promptRoot := t.TempDir()
	t.Setenv(workerPromptRootEnv, promptRoot)
	promptPath := filepath.Join(promptRoot, child.ID.String()+".md")
	prompt := []byte("immutable prompt")
	require.NoError(t, os.WriteFile(promptPath, prompt, 0o600))
	sum := sha256.Sum256(prompt)
	attempt.RenderedPromptPath = promptPath
	attempt.RenderedPromptHash = hex.EncodeToString(sum[:])
	require.NoError(t, o.db.Save(attempt).Error)
	journalDir := filepath.Join(promptRoot, "journals", child.ID.String())
	require.NoError(t, os.MkdirAll(journalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(journalDir, "journal.json"), []byte(`{"version":1,"prompt_hash":"turn-contract","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}],"next_iteration":4,"completed":false}`), 0o600))

	ev := workerDeathEvent(child.ID, *attempt, 1, false)
	ev.Usage = &container.WorkerUsage{StopReason: "token_budget"}
	o.handleWorkerDeath(context.Background(), child, attempt, ev, newReplacementTracker())

	var gotChild, gotParent model.Task
	require.NoError(t, o.db.First(&gotChild, "id = ?", child.ID).Error)
	require.NoError(t, o.db.First(&gotParent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusInProgress, gotChild.Status)
	require.Equal(t, model.StatusInProgress, gotParent.Status)
	require.Contains(t, gotChild.Context, "checkpoint_resume")
	require.Empty(t, fake.spawnCalls, "continuation is scheduled as the same child, not an identical replacement")
}

func TestDispatchEvent_OOMTriggersHandleWorkerDeath(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-oom", "c-oom")

	tracker := newReplacementTracker()
	ev := workerDeathEvent(task.ID, *attempt, 137, true)
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Len(t, fake.spawnCalls, 1, "expected respawn on OOM")
	require.Equal(t, "coder", fake.spawnCalls[0].AgentType)

	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").Find(&evts).Error)
	require.Len(t, evts, 1)
	require.Equal(t, "infra_oom_killed", evts[0].Details["normalized_reason"])
	require.Equal(t, attempt.ID.String(), evts[0].Details["attempt_id"])
}

func TestDispatchEvent_ExitZeroNoReplacement(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-ok",
		ExitCode:    0,
		OOMKilled:   false,
		Labels: map[string]string{
			"drem.task_id": task.ID.String(),
		},
	}
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Empty(t, fake.spawnCalls, "exit 0 must not respawn")
}

func TestDispatchEvent_ExitZeroCurrentAttemptCompletesTaskAndAttempt(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	task.WorktreeBranch = ""
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-ok", "c-ok")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls, "exit 0 must not respawn")
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, reloaded.Status)
	require.Nil(t, reloaded.AssignedAgentID)

	var completed model.WorkerAttempt
	require.NoError(t, o.db.First(&completed, "id = ?", attempt.ID).Error)
	require.Equal(t, model.WorkerAttemptCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)

	var evidence model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_completion_evidence").First(&evidence).Error)
	require.Equal(t, "accepted", evidence.Details["evidence"].(map[string]any)["reason"])
}

func TestReconcileWorkerAttemptLifecycles_ConsumesSpawnerExitWithoutLeaseDelay(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	task.WorktreeBranch = ""
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-polled", "c-polled")
	fake.listResult = spawner.ListWorkersResult{Workers: []spawner.WorkerInfo{{
		ContainerID: attempt.ContainerID,
		ProjectID:   o.projectID.String(),
		WorkerID:    attempt.WorkerID,
		Status:      string(container.StatusExited),
	}}}
	fake.inspectResult = spawner.InspectWorkerResult{
		Status:     string(container.StatusExited),
		ExitCode:   0,
		FinishedAt: time.Now(),
	}

	o.reconcileWorkerAttemptLifecycles(context.Background())

	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, reloaded.Status)
	require.Nil(t, reloaded.AssignedAgentID)
	var completed model.WorkerAttempt
	require.NoError(t, o.db.First(&completed, "id = ?", attempt.ID).Error)
	require.Equal(t, model.WorkerAttemptCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)
	require.Equal(t, []spawner.InspectWorkerParams{{ContainerID: attempt.ContainerID}, {ContainerID: attempt.ContainerID}}, fake.inspectCalls,
		"terminal reconciliation retries inspection once when the first response has no flushed usage summary")
}

func TestReconcileWorkerAttemptLifecycles_RecoversAuthoritativelyRemovedContainer(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	task := seedInFlightTask(t, o)
	pushTestFeatureBranch(t, o.worktree.(*FakeWorktreeManager).BarePath, task.WorktreeBranch)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-removed", "c-removed")
	fake.listResult = spawner.ListWorkersResult{}
	fake.inspectResult = spawner.InspectWorkerResult{Status: string(container.StatusRemoved)}

	o.reconcileWorkerAttemptLifecycles(context.Background())

	var failed model.WorkerAttempt
	require.NoError(t, o.db.First(&failed, "id = ?", attempt.ID).Error)
	require.Equal(t, model.WorkerAttemptFailed, failed.State)
	require.NotNil(t, failed.CompletedAt)
	require.Equal(t, []spawner.InspectWorkerParams{{ContainerID: attempt.ContainerID}, {ContainerID: attempt.ContainerID}}, fake.inspectCalls)
	require.Len(t, fake.spawnCalls, 1, "removed current worker should use the normal retry path")
}

func TestDispatchEvent_ExitZeroReplayFinalizesAttemptAfterTaskEffect(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-replay", "c-replay")
	require.NoError(t, o.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": model.StatusTestingReady, "assigned_agent_id": nil,
	}).Error)

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var completed model.WorkerAttempt
	require.NoError(t, o.db.First(&completed, "id = ?", attempt.ID).Error)
	require.Equal(t, model.WorkerAttemptCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)
	var observationCount int64
	require.NoError(t, o.db.Model(&model.AttemptEvent{}).
		Where("attempt_id = ? AND type = ?", attempt.ID, "terminal_observed").Count(&observationCount).Error)
	require.Equal(t, int64(1), observationCount)
}

func TestRecordAttemptTerminalObservation_ConcurrentSourcesRemainIdempotent(t *testing.T) {
	o, _, _ := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-concurrent", "c-concurrent")
	ev := workerDeathEvent(task.ID, *attempt, 0, false)

	const observers = 12
	errs := make(chan error, observers)
	var wg sync.WaitGroup
	for range observers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- o.recordAttemptTerminalObservation(attempt, ev)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var observationCount int64
	require.NoError(t, o.db.Model(&model.AttemptEvent{}).
		Where("attempt_id = ? AND type = ?", attempt.ID, "terminal_observed").Count(&observationCount).Error)
	require.Equal(t, int64(1), observationCount)
}

func TestDispatchEvent_MergerAttemptsFinalizeWithoutAgentmonState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exitCode  int
		wantState string
	}{
		{name: "success", exitCode: 0, wantState: model.WorkerAttemptCompleted},
		{name: "failure", exitCode: 2, wantState: model.WorkerAttemptFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, fake := dockerEventsTestRig(t)
			task := seedInFlightTask(t, o)
			task.Status = model.StatusMerging
			require.NoError(t, o.db.Save(task).Error)
			attempt := &model.WorkerAttempt{
				ID: uuid.New(), TaskID: task.ID, WorkerID: "merger-worker",
				ContainerID: "merger-container", AgentType: string(model.AgentMerger),
				State: model.WorkerAttemptRunning,
			}
			require.NoError(t, o.db.Create(attempt).Error)

			o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, tc.exitCode, false), newReplacementTracker())

			require.Empty(t, fake.spawnCalls)
			var saved model.WorkerAttempt
			require.NoError(t, o.db.First(&saved, "id = ?", attempt.ID).Error)
			require.Equal(t, tc.wantState, saved.State)
			require.NotNil(t, saved.CompletedAt)
			var savedTask model.Task
			require.NoError(t, o.db.First(&savedTask, "id = ?", task.ID).Error)
			require.Equal(t, model.StatusMerging, savedTask.Status)
		})
	}
}

func TestReconcileWorkerAttemptLifecycles_IgnoresRunningAndDuplicateTerminalObservations(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	task.WorktreeBranch = ""
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-polled-once", "c-polled-once")
	fake.listResult = spawner.ListWorkersResult{Workers: []spawner.WorkerInfo{{
		ContainerID: attempt.ContainerID,
		ProjectID:   o.projectID.String(),
		Status:      string(container.StatusRunning),
	}}}

	o.reconcileWorkerAttemptLifecycles(context.Background())
	require.Empty(t, fake.inspectCalls, "running workers do not need an exact terminal inspect")

	fake.listResult.Workers[0].Status = string(container.StatusExited)
	fake.inspectResult = spawner.InspectWorkerResult{Status: string(container.StatusExited), ExitCode: 0}
	o.reconcileWorkerAttemptLifecycles(context.Background())
	o.reconcileWorkerAttemptLifecycles(context.Background())

	require.Len(t, fake.inspectCalls, 2, "one lifecycle inspect plus one usage retry; finalized attempts must not be consumed again")
	var evidenceCount int64
	require.NoError(t, o.db.Model(&model.TaskEvent{}).
		Where("task_id = ? AND event_type = ?", task.ID, "worker_completion_evidence").Count(&evidenceCount).Error)
	require.Equal(t, int64(1), evidenceCount)
}

func TestDispatchEvent_ExitZeroCompletesThroughBranchAcceptance(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	o.skipConstraintGate = true
	fwt := o.worktree.(*FakeWorktreeManager)
	fwt.Default = testutil.GetDefaultBranch(t, fwt.BarePath)
	featureDir := t.TempDir()
	testutil.AddWorktree(t, fwt.BarePath, "feature/accepted-exit-zero", featureDir)
	testutil.CommitFile(t, featureDir, "app.go", "package app\n", "add app")
	fwt.Features = map[string]string{"accepted-exit-zero": featureDir}

	task := seedInFlightTask(t, o)
	task.WorktreeBranch = "feature/accepted-exit-zero"
	task.Context = model.JSONField{"estimated_files": []any{"app.go"}}
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-ok-branch", "c-ok-branch")
	attempt.Branch = task.WorktreeBranch
	require.NoError(t, o.db.Save(attempt).Error)

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls, "exit 0 must complete rather than respawn")
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, reloaded.Status)
	require.Nil(t, reloaded.AssignedAgentID)
	require.Contains(t, reloaded.Context, "branch_acceptance")

	var accepted model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "branch_acceptance_accepted").First(&accepted).Error)
	require.Equal(t, "accepted_worker_completion", accepted.Details["reason"])
	var typedAcceptance model.BranchAcceptanceRecord
	require.NoError(t, o.db.Where("task_id = ? AND accepted = ?", task.ID, true).First(&typedAcceptance).Error)
	require.Equal(t, task.WorktreeBranch, typedAcceptance.Branch)
	require.Equal(t, accepted.Details["head_sha"], typedAcceptance.HeadSHA)

	var completed model.WorkerAttempt
	require.NoError(t, o.db.First(&completed, "id = ?", attempt.ID).Error)
	require.Equal(t, model.WorkerAttemptCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)

	var evidence model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_completion_evidence").First(&evidence).Error)
	evidenceMap := evidence.Details["evidence"].(map[string]any)
	require.Equal(t, "accepted", evidenceMap["reason"])
	require.Equal(t, "exit_zero_current_attempt", evidenceMap["normalized_reason"])
}

func TestDispatchEvent_BranchAcceptanceUsesWorkerSpawnBase(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	o.skipConstraintGate = true
	fwt := o.worktree.(*FakeWorktreeManager)
	fwt.Default = testutil.GetDefaultBranch(t, fwt.BarePath)
	featureDir := t.TempDir()
	testutil.AddWorktree(t, fwt.BarePath, "feature/cumulative-parent", featureDir)
	testutil.CommitFile(t, featureDir, "inherited.txt", "parent work\n", "parent sibling work")
	spawnBase := strings.TrimSpace(runGitCmd(t, featureDir, "rev-parse", "HEAD"))
	testutil.CommitFile(t, featureDir, "allowed.go", "package allowed\n", "worker work")
	fwt.Features = map[string]string{"cumulative-parent": featureDir}

	task := seedInFlightTask(t, o)
	task.WorktreeBranch = "feature/cumulative-parent"
	task.Context = model.JSONField{"estimated_files": []any{"allowed.go"}}
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-cumulative", "c-cumulative")
	attempt.Branch = task.WorktreeBranch
	attempt.BaseSHA = spawnBase
	require.NoError(t, o.db.Save(attempt).Error)

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusTestingReady, reloaded.Status)
	detail := reloaded.Context["branch_acceptance"].(map[string]any)
	require.Equal(t, spawnBase, detail["base_ref"])
	require.Equal(t, []any{"allowed.go"}, detail["accepted_files"])
}

func TestDispatchEvent_ExitZeroFailsClosedOnBranchScopeRejection(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	o.skipConstraintGate = true
	fwt := o.worktree.(*FakeWorktreeManager)
	fwt.Default = testutil.GetDefaultBranch(t, fwt.BarePath)
	featureDir := t.TempDir()
	testutil.AddWorktree(t, fwt.BarePath, "feature/rejected-exit-zero", featureDir)
	testutil.CommitFile(t, featureDir, "outside.go", "package outside\n", "add out-of-scope file")
	fwt.Features = map[string]string{"rejected-exit-zero": featureDir}

	task := seedInFlightTask(t, o)
	task.WorktreeBranch = "feature/rejected-exit-zero"
	task.Context = model.JSONField{"estimated_files": []any{"allowed.go"}}
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-bad-branch", "c-bad-branch")
	attempt.Branch = task.WorktreeBranch
	require.NoError(t, o.db.Save(attempt).Error)

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls, "scope rejection must stop rather than spend another inference attempt")
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, failureClassBranchContam, reloaded.Context["failure_class"])
}

func TestDispatchEvent_RejectsOutOfScopeWorkerBeforeParentMerge(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	o.skipConstraintGate = true
	fwt := o.worktree.(*FakeWorktreeManager)
	fwt.Default = testutil.GetDefaultBranch(t, fwt.BarePath)
	parentDir := t.TempDir()
	testutil.AddWorktree(t, fwt.BarePath, "feature/premerge-parent", parentDir)
	testutil.CommitFile(t, parentDir, "inherited.txt", "parent work\n", "parent work")
	spawnBase := strings.TrimSpace(runGitCmd(t, parentDir, "rev-parse", "HEAD"))
	workerDir := t.TempDir()
	testutil.AddWorktree(t, fwt.BarePath, "feature/premerge-worker", workerDir)
	runGitCmd(t, workerDir, "reset", "--hard", spawnBase)
	testutil.CommitFile(t, workerDir, "outside.txt", "bad worker change\n", "out-of-scope work")
	fwt.Features = map[string]string{"premerge-parent": parentDir}

	task := seedInFlightTask(t, o)
	task.WorktreeBranch = "feature/premerge-parent"
	task.Context = model.JSONField{"estimated_files": []any{"allowed.go"}}
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-premerge", "c-premerge")
	attempt.Branch = "feature/premerge-worker"
	attempt.BaseSHA = spawnBase
	require.NoError(t, o.db.Save(attempt).Error)
	require.NoError(t, o.db.Model(&model.Agent{}).Where("id = ?", attempt.AgentID).
		Update("worktree_branch", attempt.Branch).Error)

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	require.Equal(t, spawnBase, strings.TrimSpace(runGitCmd(t, parentDir, "rev-parse", "HEAD")),
		"rejected worker must not advance the parent branch")
	_, err := os.Stat(filepath.Join(parentDir, "outside.txt"))
	require.True(t, os.IsNotExist(err), "rejected file must never enter the parent worktree")
}

func TestDispatchEvent_DeathAfterPriorBranchRejectionFailsWithoutRespawn(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	task.Context = model.JSONField{"branch_acceptance": map[string]any{"accepted": false}}
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-legacy-retry", "c-legacy-retry")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 143, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, failureClassBranchContam, reloaded.Context["failure_class"])
}

func TestDispatchEvent_ExitZeroStaleAttemptDoesNotMutateCurrentAssignment(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	oldAttempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-old-ok", "c-old-ok")
	oldAgentID := *oldAttempt.AgentID
	now := time.Now()
	oldAttempt.State = model.WorkerAttemptFailed
	oldAttempt.CompletedAt = &now
	require.NoError(t, o.db.Save(oldAttempt).Error)
	require.NoError(t, o.db.Model(&model.Agent{}).Where("id = ?", oldAgentID).Updates(map[string]any{
		"status":          model.AgentDead,
		"current_task_id": nil,
		"completed_at":    now,
	}).Error)
	current := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-current-ok", "c-current-ok")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *oldAttempt, 0, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Equal(t, *current.AgentID, *reloaded.AssignedAgentID)

	var currentAgent model.Agent
	require.NoError(t, o.db.First(&currentAgent, "id = ?", *current.AgentID).Error)
	require.Equal(t, model.AgentWorking, currentAgent.Status)

	var stale model.WorkerAttempt
	require.NoError(t, o.db.First(&stale, "id = ?", oldAttempt.ID).Error)
	require.Equal(t, model.WorkerAttemptFailed, stale.State)

	var evidence model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_completion_evidence").First(&evidence).Error)
	evidenceMap := evidence.Details["evidence"].(map[string]any)
	require.Equal(t, "ignored", evidenceMap["reason"])
	require.Equal(t, "stale_attempt", evidenceMap["normalized_reason"])
}

func TestDispatchEvent_ReplacementCapExhaustsAfterThreeAttempts(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-dead-0", "c-dead-0")

	tracker := newReplacementTracker()
	for i := 0; i < replacementCap; i++ {
		attempt := currentAssignedWorkerAttempt(t, o, task.ID)
		ev := workerDeathEvent(task.ID, attempt, 137, false)
		o.dispatchEvent(context.Background(), ev, tracker)
	}
	require.Len(t, fake.spawnCalls, replacementCap)

	// Task must still be active — refresh first.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)

	// Fourth death must push the task into failed without spawning again.
	attempt := currentAssignedWorkerAttempt(t, o, task.ID)
	ev := workerDeathEvent(task.ID, attempt, 137, false)
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Len(t, fake.spawnCalls, replacementCap,
		"spawner must not be invoked beyond the replacement cap")

	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
}

func TestDispatchEvent_RepeatedToolLoopExhaustsDurableRetryBudget(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-loop-0", "c-loop-0")
	tracker := newReplacementTracker()

	first := currentAssignedWorkerAttempt(t, o, task.ID)
	firstEvent := workerDeathEvent(task.ID, first, 1, false)
	firstEvent.Labels["drem.failure_class"] = "tool_loop"
	o.dispatchEvent(context.Background(), firstEvent, tracker)
	require.Len(t, fake.spawnCalls, 1)

	second := currentAssignedWorkerAttempt(t, o, task.ID)
	secondEvent := workerDeathEvent(task.ID, second, 1, false)
	secondEvent.Labels["drem.failure_class"] = "tool_loop"
	o.dispatchEvent(context.Background(), secondEvent, tracker)

	require.Len(t, fake.spawnCalls, 1, "exhausted tool-loop budget must not respawn again")
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, failureClassToolLoop, reloaded.Context["latest_failure_type"])
	require.Equal(t, true, reloaded.Context["latest_failure_retry_exhausted"])

	budgets, ok := reloaded.Context[retryBudgetsContextKey].(map[string]any)
	require.True(t, ok)
	var found bool
	for _, raw := range budgets {
		entry, ok := raw.(map[string]any)
		if !ok || entry["class"] != failureClassToolLoop {
			continue
		}
		found = true
		require.Equal(t, float64(2), entry["attempts"])
		require.Equal(t, true, entry["exhausted"])
	}
	require.True(t, found, "expected persisted tool-loop budget")
}

func TestDispatchEvent_TokenBudgetFailureDoesNotRespawnIdenticalWorker(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-budget", "c-budget")

	ev := workerDeathEvent(task.ID, *attempt, 1, false)
	ev.Usage = &container.WorkerUsage{Iterations: 9, TokensIn: 31061, TokensOut: 400, StopReason: "token_budget"}
	ev.UsageInspected = true
	o.dispatchEvent(context.Background(), ev, newReplacementTracker())

	require.Empty(t, fake.spawnCalls, "deterministic inference-budget exhaustion must park after the first attempt")
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, failureClassInferenceBudget, reloaded.Context["latest_failure_type"])
	require.Equal(t, true, reloaded.Context["latest_failure_retry_exhausted"])
}

func TestDispatchEvent_PreservesMutatedCheckpointInsteadOfRespawning(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	worktree := filepath.Join(t.TempDir(), "worker")
	testutil.AddWorktree(t, o.worktree.BareRepo(), task.WorktreeBranch, worktree)
	testutil.CommitFile(t, worktree, "checkpoint.txt", "useful partial work\n", "checkpoint")
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-checkpoint", "c-checkpoint")
	attempt.BaseSHA = strings.TrimSpace(runGitCmd(t, o.worktree.BareRepo(), "rev-parse", "main"))
	attempt.Branch = task.WorktreeBranch
	require.NoError(t, o.db.Save(attempt).Error)

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 1, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls, "a mutated checkpoint must become a handoff, not an identical retry")
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusFailed, reloaded.Status)
	require.Equal(t, failureClassArtifactHandoff, reloaded.Context["failure_class"])
	handoff, ok := reloaded.Context["checkpoint_handoff"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, task.WorktreeBranch, handoff["branch"])
	require.NotEmpty(t, handoff["sha"])
}

func TestDispatchEvent_StaleDeathDoesNotKillCurrentAssignedWorker(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	oldAttempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-old", "c-old")
	oldAgentID := *oldAttempt.AgentID
	now := time.Now()
	oldAttempt.State = model.WorkerAttemptFailed
	oldAttempt.CompletedAt = &now
	require.NoError(t, o.db.Save(oldAttempt).Error)
	require.NoError(t, o.db.Model(&model.Agent{}).Where("id = ?", oldAgentID).Updates(map[string]any{
		"status":          model.AgentDead,
		"current_task_id": nil,
		"completed_at":    now,
	}).Error)
	current := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-current", "c-current")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *oldAttempt, 1, false), newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Equal(t, *current.AgentID, *reloaded.AssignedAgentID)
	var currentAgent model.Agent
	require.NoError(t, o.db.First(&currentAgent, "id = ?", *current.AgentID).Error)
	require.Equal(t, model.AgentWorking, currentAgent.Status)

	var event model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").First(&event).Error)
	require.Equal(t, oldAttempt.ID.String(), event.Details["attempt_id"])
	require.Equal(t, "tool_exit_nonzero", event.Details["normalized_reason"])
}

func TestDispatchEvent_ReviewerDeathRespawnsReviewer(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	task.Status = model.StatusTestingReady
	require.NoError(t, o.db.Save(task).Error)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentReviewer, "worker-reviewer", "c-reviewer")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 1, false), newReplacementTracker())

	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, string(model.AgentReviewer), fake.spawnCalls[0].AgentType)
}

func TestDispatchEvent_FixerDeathRespawnsFixerInProgress(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentFixer, "worker-fixer", "c-fixer")

	o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, 1, false), newReplacementTracker())

	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, string(model.AgentFixer), fake.spawnCalls[0].AgentType)
}

func TestDispatchEvent_UnmatchedDeathAuditsWithoutRespawnOrClear(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := seedInFlightTask(t, o)
	current := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-current", "c-current")

	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-unknown",
		ExitCode:    1,
		Labels: map[string]string{
			"drem.task_id":   task.ID.String(),
			"drem.worker_id": "worker-unknown",
		},
	}
	o.dispatchEvent(context.Background(), ev, newReplacementTracker())

	require.Empty(t, fake.spawnCalls)
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
	require.Equal(t, *current.AgentID, *reloaded.AssignedAgentID)
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").Find(&evts).Error)
	require.Len(t, evts, 1)
}

func TestDispatchEvent_IgnoresEventsWithoutTaskIDLabel(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-strange",
		ExitCode:    1,
		Labels:      map[string]string{},
	}
	o.dispatchEvent(context.Background(), ev, tracker)
	require.Empty(t, fake.spawnCalls)
}

func TestWatchDockerEvents_ClosesCleanlyOnCtxCancel(t *testing.T) {
	o, _, _ := dockerEventsTestRig(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.watchDockerEvents(ctx) }()

	// Give the subscription time to register.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("watchDockerEvents did not exit within 1s")
	}
}

func TestDispatchEvent_TerminalTaskStillFinalizesLateAttempt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exitCode  int
		wantState string
	}{
		{name: "successful worker exit", exitCode: 0, wantState: model.WorkerAttemptCompleted},
		{name: "failed worker exit", exitCode: 1, wantState: model.WorkerAttemptFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, fake := dockerEventsTestRig(t)
			task := seedInFlightTask(t, o)
			attempt := seedAssignedWorkerAttempt(t, o, task, model.AgentCoder, "worker-late", "c-late")
			task.Status = model.StatusFailed
			require.NoError(t, o.db.Save(task).Error)

			o.dispatchEvent(context.Background(), workerDeathEvent(task.ID, *attempt, tc.exitCode, false), newReplacementTracker())

			require.Empty(t, fake.spawnCalls)
			var reloaded model.Task
			require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
			require.Equal(t, model.StatusFailed, reloaded.Status)
			var finalized model.WorkerAttempt
			require.NoError(t, o.db.First(&finalized, "id = ?", attempt.ID).Error)
			require.Equal(t, tc.wantState, finalized.State)
			require.NotNil(t, finalized.CompletedAt)
			var observations int64
			require.NoError(t, o.db.Model(&model.AttemptEvent{}).
				Where("attempt_id = ? AND type = ?", attempt.ID, "terminal_observed").Count(&observations).Error)
			require.Equal(t, int64(1), observations)
		})
	}
}

// Guard: dispatch path must not mutate tasks that are already terminal.
func TestDispatchEvent_SkipsTerminalTasks(t *testing.T) {
	o, _, fake := dockerEventsTestRig(t)
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       "Already done",
		Description: "x",
		Status:      model.StatusDone,
	}
	require.NoError(t, o.db.Create(task).Error)

	tracker := newReplacementTracker()
	ev := container.Event{
		Type:        container.EventDie,
		ContainerID: "c-late",
		ExitCode:    1,
		Labels:      map[string]string{"drem.task_id": task.ID.String()},
	}
	o.dispatchEvent(context.Background(), ev, tracker)

	require.Empty(t, fake.spawnCalls)
	// Container death audit row is still written even for terminal tasks.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "container_died").Find(&evts).Error)
	require.Len(t, evts, 1)
}

// Unused spawner result guard — ensures compile-time dependency to spawner.
var _ = spawner.ListWorkersParams{}
