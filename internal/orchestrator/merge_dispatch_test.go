package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// dispatchMergeTestRig stands up an Orchestrator wired to a fakeWorkerSpawner
// whose InspectWorker already reports "exited" so awaitMergerExit returns
// on its first tick rather than blocking on the 2-second poll loop.
// Callers override ExitCode by mutating fake.inspectResult.ExitCode before
// invoking dispatchMerge.
func dispatchMergeTestRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "merge-dispatch-test",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{
		inspectResult: spawner.InspectWorkerResult{
			Status:   "exited",
			ExitCode: 0,
		},
	}
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: "/tmp/fake-bare", Default: "main"},
		logger:         slog.Default().With("component", "merge_dispatch_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
		testGate:       TestGateConfig{TestCommand: "go test ./..."},
		orchURL:        "http://orch:8080",
		agentmonToken:  "test-token-abc",
	}
	return o, fake
}

func seedDispatchArtifact(t *testing.T, o *Orchestrator, task *model.Task) model.DeliveryArtifact {
	t.Helper()
	artifact := model.DeliveryArtifact{
		ID:                  uuid.New(),
		TaskID:              task.ID,
		ArtifactVersion:     1,
		Branch:              task.WorktreeBranch,
		CommitSHA:           strings.Repeat("a", 40),
		BaseBranch:          o.worktree.DefaultBranchName(),
		BaseSHA:             strings.Repeat("b", 40),
		PreliminaryEvidence: model.JSONField{"commands": []string{"go test ./..."}},
		CreatorActor:        "test",
		CreatorSource:       "test",
	}
	require.NoError(t, o.db.Create(&artifact).Error)
	return artifact
}

// findFlagValue walks an argv slice returning the value that follows the
// first occurrence of flag and reports whether it was present.
func findFlagValue(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// TestDispatchMerge_BuildsRequiredArgv verifies every mandatory flag that
// drem-merger's parseFlags requires is present in params.Cmd with the
// expected value. Missing any of these makes the container crash-loop on
// startup.
func TestDispatchMerge_BuildsRequiredArgv(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "test task",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/argv-test",
	}
	require.NoError(t, o.db.Create(task).Error)
	artifact := seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	cmd := fake.spawnCalls[0].Cmd
	require.NotEmpty(t, cmd, "Cmd must be populated with merger argv")

	want := map[string]string{
		"--feature-branch":       "feature/argv-test",
		"--project":              o.projectID.String(),
		"--task-id":              task.ID.String(),
		"--test-cmd":             "go test ./...",
		"--orch-url":             "http://orch:8080",
		"--agentmon-token":       "test-token-abc",
		"--expected-feature-sha": artifact.CommitSHA,
		"--expected-base-sha":    artifact.BaseSHA,
	}
	for flag, expected := range want {
		got, ok := findFlagValue(cmd, flag)
		require.True(t, ok, "%s must be in Cmd: %v", flag, cmd)
		require.Equal(t, expected, got, "value for %s", flag)
	}
}

// TestDispatchMerge_SetsBareRepoReadWrite asserts the spawner call flips
// BareRepoReadWrite to true so /bare is mounted rw for the merger.
func TestDispatchMerge_SetsBareRepoReadWrite(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "rw mount test",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/rw-test",
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	require.True(t, fake.spawnCalls[0].BareRepoReadWrite,
		"merger must request read-write /bare mount")
	require.Equal(t, "/tmp/fake-bare", fake.spawnCalls[0].BareRepoMount)
}

func TestDispatchMerge_RecordsDurableAttemptWithoutCoderAttribution(t *testing.T) {
	o, _ := dispatchMergeTestRig(t)
	agentID := uuid.New()
	require.NoError(t, o.db.Create(&model.Agent{
		ID:        agentID,
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "coder",
		Status:    model.AgentWorking,
	}).Error)
	task := &model.Task{
		ID:              uuid.New(),
		ProjectID:       o.projectID,
		Title:           "merge attempt attribution",
		Status:          model.StatusMerging,
		WorktreeBranch:  "feature/merge-attribution",
		AssignedAgentID: &agentID,
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	var attempt model.WorkerAttempt
	require.NoError(t, o.db.First(&attempt, "task_id = ?", task.ID).Error)
	require.Equal(t, string(model.AgentMerger), attempt.AgentType)
	require.Nil(t, attempt.AgentID)
	require.NotEqual(t, uuid.Nil, attempt.ID)

	var spawn model.TaskEvent
	require.NoError(t, o.db.First(&spawn, "task_id = ? AND event_type = ?", task.ID, "worker_spawned").Error)
	require.Equal(t, attempt.ID.String(), spawn.Details["attempt_id"])
	require.Empty(t, spawn.Details["agent_id"])
}

func TestDispatchMerge_UsesTypedAttemptAndClearsLegacyMergeContext(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "current-container"}}}
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "merge attempt context",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/merge-context",
		Context: model.JSONField{
			"merge_commit":              "stale-sha",
			"merge_conflicts":           []string{"stale.txt"},
			"merge_failure_reason":      "conflict",
			"merge_test_output":         "old output",
			"merge_result_attempt_id":   uuid.NewString(),
			"merge_result_container_id": "old-container",
		},
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	var attempt model.WorkerAttempt
	require.NoError(t, o.db.First(&attempt, "task_id = ? AND agent_type = ?", task.ID, string(model.AgentMerger)).Error)
	var saved model.Task
	require.NoError(t, o.db.First(&saved, "id = ?", task.ID).Error)
	require.Equal(t, model.WorkerAttemptCompleted, attempt.State)
	require.NotNil(t, attempt.CompletedAt)
	require.NotContains(t, saved.Context, "current_merge_attempt_id")
	require.NotContains(t, saved.Context, "current_merge_container_id")
	require.NotContains(t, saved.Context, "current_merge_worker_id")
	require.NotContains(t, saved.Context, "merge_commit")
	require.NotContains(t, saved.Context, "merge_conflicts")
	require.NotContains(t, saved.Context, "merge_failure_reason")
	require.NotContains(t, saved.Context, "merge_test_output")
	require.NotContains(t, saved.Context, "merge_result_attempt_id")
	require.NotContains(t, saved.Context, "merge_result_container_id")
}

func TestDispatchMerge_IgnoresMergeContextFromDifferentAttempt(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "current-container"}}}
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "stale merge result context",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/stale-merge-result",
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	done := make(chan struct{})
	var result *MergeResult
	var dispatchErr error
	go func() {
		defer close(done)
		result, dispatchErr = o.dispatchMerge(context.Background(), task)
	}()

	require.Eventually(t, func() bool {
		var count int64
		require.NoError(t, o.db.Model(&model.WorkerAttempt{}).Where("task_id = ?", task.ID).Count(&count).Error)
		return count == 1
	}, 3*time.Second, 20*time.Millisecond)
	require.NoError(t, o.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("context", model.JSONField{
		"current_merge_attempt_id":   uuid.NewString(),
		"current_merge_container_id": "other-container",
		"current_merge_worker_id":    "other-worker",
		"merge_result_attempt_id":    uuid.NewString(),
		"merge_result_container_id":  "other-container",
		"merge_commit":               "stale-sha",
		"merge_conflicts":            []string{"stale.txt"},
	}).Error)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchMerge did not finish")
	}
	require.NoError(t, dispatchErr)
	require.NotNil(t, result)
	require.Empty(t, result.MergeCommit)
	require.Empty(t, result.Conflicts)
}

// TestDispatchMerge_OmitsPromptAndCredsMounts documents that merger,
// a Go binary, receives neither a prompt nor a creds mount — the
// promptRequired and credsMountRequired tables both return false for
// "merger" and dispatchMerge does not populate either field. A
// regression here would accidentally charge the operator's claude
// subscription pool or render a prompt the merger has no way to use.
// See plans/worker-prompt-delivery.md §§5, 8.
func TestDispatchMerge_OmitsPromptAndCredsMounts(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "merger mount omissions",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/merger-mounts",
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	p := fake.spawnCalls[0]
	require.Empty(t, p.CredsMount, "merger must not carry a creds mount (Go binary, no claude CLI)")
	require.Empty(t, p.PromptMount, "merger must not carry a prompt mount (Go binary, takes argv flags)")
}

// TestDispatchMerge_DefaultIntegrationBranch_OmitsFlag asserts that a
// plain "main" (or "master") default branch produces an argv without a
// --integration-branch pair — drem-merger's own default is "master" so
// redundancy is avoided for the common case.
func TestDispatchMerge_DefaultIntegrationBranch_OmitsFlag(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	// worktree.Default = "main" (from dispatchMergeTestRig) — a default
	// integration branch. The flag should be omitted.
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "default branch test",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/default-integration",
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	cmd := fake.spawnCalls[0].Cmd
	_, ok := findFlagValue(cmd, "--integration-branch")
	require.False(t, ok, "--integration-branch must be omitted for plain main/master: %v", cmd)
}

// TestDispatchMerge_NonDefaultIntegrationBranch_IncludesFlag asserts that
// a project whose default branch differs from main/master produces an
// --integration-branch flag with the correct value.
func TestDispatchMerge_NonDefaultIntegrationBranch_IncludesFlag(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	o.worktree = &FakeWorktreeManager{BarePath: "/tmp/fake-bare", Default: "develop"}
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "custom branch test",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/custom-integration",
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	cmd := fake.spawnCalls[0].Cmd
	got, ok := findFlagValue(cmd, "--integration-branch")
	require.True(t, ok, "--integration-branch must be present for non-default branch: %v", cmd)
	require.Equal(t, "develop", got)
}

// TestDispatchMerge_ExitCodeMapping walks every documented exit code from
// drem-merger's exitCodeFor table (plus one unknown value) and asserts
// MergeResult.FailureReason is mapped correctly. This keeps the
// orchestrator-side string and the merger-side integer in lockstep.
func TestDispatchMerge_ExitCodeMapping(t *testing.T) {
	cases := []struct {
		exit       int
		wantReason string
		wantOK     bool
	}{
		{0, "", true},
		{1, "misc", false},
		{2, "conflict", false},
		{3, "tests_failed", false},
		{4, "push_failed", false},
		{5, "stale_evidence", false},
		{99, "unknown", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.wantReason+"-"+itoaExit(tc.exit), func(t *testing.T) {
			o, fake := dispatchMergeTestRig(t)
			fake.inspectResult = spawner.InspectWorkerResult{
				Status:   "exited",
				ExitCode: tc.exit,
			}
			task := &model.Task{
				ID:             uuid.New(),
				ProjectID:      o.projectID,
				Title:          "exit test",
				Status:         model.StatusMerging,
				WorktreeBranch: "feature/exit-" + itoaExit(tc.exit),
			}
			require.NoError(t, o.db.Create(task).Error)
			seedDispatchArtifact(t, o, task)

			res, err := o.dispatchMerge(context.Background(), task)
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Equal(t, tc.exit, res.ExitCode)
			require.Equal(t, tc.wantOK, res.Success, "Success for exit %d", tc.exit)
			require.Equal(t, tc.wantReason, res.FailureReason,
				"FailureReason for exit %d", tc.exit)
			var attempt model.WorkerAttempt
			require.NoError(t, o.db.First(&attempt, "task_id = ? AND agent_type = ?", task.ID, string(model.AgentMerger)).Error)
			require.NotNil(t, attempt.CompletedAt)
			if tc.wantOK {
				require.Equal(t, model.WorkerAttemptCompleted, attempt.State)
			} else {
				require.Equal(t, model.WorkerAttemptFailed, attempt.State)
				require.NotNil(t, attempt.FailedAt)
				require.NotEmpty(t, attempt.FailureClassification)
			}
		})
	}
}

// itoaExit avoids importing strconv for a two-line test helper while
// keeping the subtest name readable. Negative codes never occur in
// practice (drem-merger emits unsigned codes via os.Exit) so signed
// formatting is unnecessary.
func itoaExit(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestDispatchMerge_EmptyTelemetryCoordinatesStillSpawns(t *testing.T) {
	o, fake := dispatchMergeTestRig(t)
	o.orchURL = ""
	o.agentmonToken = ""
	t.Setenv("DREM_ORCH_URL", "")
	t.Setenv("DREM_AGENTMON_TOKEN", "")

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "empty token guard",
		Status:         model.StatusMerging,
		WorktreeBranch: "feature/no-token",
	}
	require.NoError(t, o.db.Create(task).Error)
	seedDispatchArtifact(t, o, task)

	res, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, fake.spawnCalls, 1)
	_, hasURL := findFlagValue(fake.spawnCalls[0].Cmd, "--orch-url")
	_, hasToken := findFlagValue(fake.spawnCalls[0].Cmd, "--agentmon-token")
	require.False(t, hasURL)
	require.False(t, hasToken)
}

// TestBuildMergerArgv_EmptyTestCmdRejected covers Bug H's fail-close
// guard. drem-merger's parseFlags rejects empty --test-cmd with exit 1,
// so orchestrator must refuse to spawn rather than crash-loop. See
// plans/bug-h-merger-crash-on-v17-advance.md.
//
// Three assertions:
//  1. buildMergerArgv returns errMergerSpawnSkippedEmptyTestCmd for
//     empty TestCommand, whitespace-only TestCommand, and tab.
//  2. dispatchMerge with empty TestCommand does NOT call the spawner.
//  3. The task is transitioned to FAILED with failure_reason set to
//     the operator-visible string Seth approved.
func TestBuildMergerArgv_EmptyTestCmdRejected(t *testing.T) {
	t.Run("unit: buildMergerArgv returns sentinel on empty testCmd", func(t *testing.T) {
		task := &model.Task{
			ID:             uuid.New(),
			WorktreeBranch: "feature/empty-test-cmd",
		}
		for _, tc := range []struct {
			name    string
			testCmd string
		}{
			{"empty", ""},
			{"whitespace", "   "},
			{"tab", "\t"},
			{"newline", "\n"},
		} {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				argv, err := buildMergerArgv(task, "proj-id", "main", tc.testCmd, "http://orch", "tok", strings.Repeat("a", 40), strings.Repeat("b", 40))
				require.Nil(t, argv)
				require.ErrorIs(t, err, errMergerSpawnSkippedEmptyTestCmd)
			})
		}
	})

	t.Run("unit: buildMergerArgv accepts non-empty testCmd", func(t *testing.T) {
		task := &model.Task{
			ID:             uuid.New(),
			WorktreeBranch: "feature/ok",
		}
		argv, err := buildMergerArgv(task, "proj-id", "main", "go test ./...", "http://orch", "tok", strings.Repeat("a", 40), strings.Repeat("b", 40))
		require.NoError(t, err)
		require.NotEmpty(t, argv)
	})

	t.Run("integration: dispatchMerge fails task without spawning", func(t *testing.T) {
		o, fake := dispatchMergeTestRig(t)
		// Override test gate: simulate a project whose drem.toml has no
		// test_command and whose main worktree lacks any known project
		// marker (so inference returned empty).
		o.testGate = TestGateConfig{TestCommand: ""}

		task := &model.Task{
			ID:             uuid.New(),
			ProjectID:      o.projectID,
			Title:          "empty test cmd guard",
			Status:         model.StatusMerging,
			WorktreeBranch: "feature/no-test-cmd",
		}
		require.NoError(t, o.db.Create(task).Error)
		seedDispatchArtifact(t, o, task)

		res, err := o.dispatchMerge(context.Background(), task)
		require.Nil(t, res)
		require.ErrorIs(t, err, errMergerSpawnSkippedEmptyTestCmd,
			"dispatchMerge must surface the sentinel so executeMerge swallows it cleanly")

		require.Empty(t, fake.spawnCalls,
			"fail-close: spawner must NOT be invoked when TestCommand is empty")

		// Reload the task and verify the FAILED transition + failure_reason.
		var reloaded model.Task
		require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
		require.Equal(t, model.StatusFailed, reloaded.Status,
			"task must be transitioned to FAILED")
		require.NotNil(t, reloaded.Context)
		reason, ok := reloaded.Context["failure_reason"].(string)
		require.True(t, ok, "failure_reason must be populated on task.Context")
		require.Equal(t, "merger spawn skipped: project has no test command", reason,
			"failure_reason must match the Seth-approved operator-facing phrasing")
	})

	t.Run("integration: executeMerge swallows the sentinel", func(t *testing.T) {
		o, fake := dispatchMergeTestRig(t)
		o.testGate = TestGateConfig{TestCommand: ""}

		task := &model.Task{
			ID:             uuid.New(),
			ProjectID:      o.projectID,
			Title:          "executeMerge swallow",
			Status:         model.StatusMerging,
			WorktreeBranch: "feature/swallow",
		}
		require.NoError(t, o.db.Create(task).Error)

		// executeMerge must return nil — not a wrapped error — so the
		// dispatchMerges tick loop does not emit a spurious error log
		// for a task that was handled cleanly (FAILED + reason set).
		err := o.executeMerge(task)
		require.NoError(t, err,
			"executeMerge must swallow errMergerSpawnSkippedEmptyTestCmd: task is already FAILED")
		require.Empty(t, fake.spawnCalls)
	})
}
