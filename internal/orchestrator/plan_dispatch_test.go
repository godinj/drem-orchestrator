package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// dispatchPlanTestRig stands up an Orchestrator wired to a fakeWorkerSpawner
// whose InspectWorker already reports "exited" with exit code 0 so the poll
// loop in awaitPlannerExit returns on its first tick. Callers override
// ExitCode / write a plan.json at the worktree root as needed.
//
// featureWorktree (returned as the third value) is the temp dir the fake
// worktree manager hands out for the planner's feature branch — tests drop
// plan.json here to simulate a real planner container's output.
func dispatchPlanTestRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner, string) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "plan-dispatch-test",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}).Error)

	bareRepo := t.TempDir()
	featureDir := t.TempDir()

	fake := &fakeWorkerSpawner{
		inspectResult: spawner.InspectWorkerResult{
			Status:   "exited",
			ExitCode: 0,
		},
	}
	o := &Orchestrator{
		db:        db,
		projectID: projectID,
		events:    make(chan Event, 32),
		worktree: &FakeWorktreeManager{
			BarePath: bareRepo,
			Default:  "main",
			OnFeatureWorktreePath: func(name string) string {
				return featureDir
			},
		},
		logger:         slog.Default().With("component", "plan_dispatch_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake, featureDir
}

// writeValidPlanJSON drops a minimally-valid plan.json in featureDir so
// dispatchPlan's parse+validation step passes. The shape mirrors
// plannerInstructions() from internal/prompt/prompt_planner.go.
func writeValidPlanJSON(t *testing.T, featureDir string) {
	t.Helper()
	const body = `{
  "subtasks": [
    {"title":"test add","description":"write test","agent_type":"coder","phase":"test","tests_for":[1],"files":["foo_test.go"]},
    {"title":"impl add","description":"implement","agent_type":"coder","phase":"implementation","files":["foo.go"]}
  ]
}`
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "plan.json"), []byte(body), 0o644))
}

// TestDispatchPlan_BuildsRequiredArgv verifies every flag the planner's
// entrypoint requires is present in params.Cmd with the expected value.
// Missing any of these makes the container exit 2 (flag parse error).
func TestDispatchPlan_BuildsRequiredArgv(t *testing.T) {
	o, fake, featureDir := dispatchPlanTestRig(t)
	writeValidPlanJSON(t, featureDir)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "plan argv test",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/argv-test",
	}
	require.NoError(t, o.db.Create(task).Error)

	_, err := o.dispatchPlan(context.Background(), task, "prompt body", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant-test",
	})
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	cmd := fake.spawnCalls[0].Cmd
	require.NotEmpty(t, cmd, "Cmd must be populated with planner argv")

	for _, want := range []struct{ flag, value string }{
		{"--task-id", task.ID.String()},
		{"--branch", "feature/argv-test"},
		{"--model", "claude-opus-4-6"},
		{"--effort", "high"},
	} {
		got, ok := findFlagValue(cmd, want.flag)
		require.True(t, ok, "%s must be in Cmd: %v", want.flag, cmd)
		require.Equal(t, want.value, got, "value for %s", want.flag)
	}

	// --prompt-file must be present and point at a real file that survives
	// the call (orch writes it before spawn; the container bind-mounts
	// it via /work). We just assert the flag is present — the path
	// cleanup is dispatchPlan's responsibility.
	_, ok := findFlagValue(cmd, "--prompt-file")
	require.True(t, ok, "--prompt-file must be in Cmd: %v", cmd)
}

// TestDispatchPlan_ForwardsAPIKeyViaEnv asserts the orch plumbs the
// Anthropic API key through params.Env so the planner container can
// authenticate against api.anthropic.com.
func TestDispatchPlan_ForwardsAPIKeyViaEnv(t *testing.T) {
	o, fake, featureDir := dispatchPlanTestRig(t)
	writeValidPlanJSON(t, featureDir)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "env test",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/env-test",
	}
	require.NoError(t, o.db.Create(task).Error)

	_, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant-secret",
	})
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, "sk-ant-secret", fake.spawnCalls[0].Env["ANTHROPIC_API_KEY"],
		"ANTHROPIC_API_KEY must be forwarded via SpawnWorkerParams.Env")
}

// TestDispatchPlan_FailsClosedOnMissingAPIKey is the credentials guard:
// dispatchPlan refuses to spawn a planner container if ANTHROPIC_API_KEY
// is empty. Spawning anyway would waste a planner-spawn budget on a
// container that exits 2 immediately.
func TestDispatchPlan_FailsClosedOnMissingAPIKey(t *testing.T) {
	o, fake, _ := dispatchPlanTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "no key",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/no-key",
	}
	require.NoError(t, o.db.Create(task).Error)

	_, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
	require.Empty(t, fake.spawnCalls,
		"dispatchPlan must not spawn when API key missing")
}

// TestDispatchPlan_SetsBareRepoReadOnly asserts the spawner call leaves
// BareRepoReadWrite=false (default). Unlike merger, planner only reads
// the bare repo — it clones but never pushes back.
func TestDispatchPlan_SetsBareRepoReadOnly(t *testing.T) {
	o, fake, featureDir := dispatchPlanTestRig(t)
	writeValidPlanJSON(t, featureDir)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "ro mount test",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/ro-test",
	}
	require.NoError(t, o.db.Create(task).Error)

	_, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant",
	})
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	require.False(t, fake.spawnCalls[0].BareRepoReadWrite,
		"planner must use read-only /bare mount")
}

// TestDispatchPlan_ReturnsPlanOnSuccess is the happy path: exit 0 + valid
// plan.json → PlanResult.Success=true with Plan populated.
func TestDispatchPlan_ReturnsPlanOnSuccess(t *testing.T) {
	o, _, featureDir := dispatchPlanTestRig(t)
	writeValidPlanJSON(t, featureDir)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "happy path",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/happy",
	}
	require.NoError(t, o.db.Create(task).Error)

	res, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success)
	require.Equal(t, 0, res.ExitCode)
	require.NotNil(t, res.Plan)
	require.Contains(t, res.Plan, "subtasks")
}

// TestDispatchPlan_MissingPlanFileIsFailure: exit 0 but no plan.json →
// Success=false with a descriptive error, so orch can surface it as
// "silent CLI failure" and retry per plan §7.
func TestDispatchPlan_MissingPlanFileIsFailure(t *testing.T) {
	o, _, _ := dispatchPlanTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "no plan",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/no-plan",
	}
	require.NoError(t, o.db.Create(task).Error)

	res, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.Equal(t, "missing_plan_file", res.FailureReason)
	require.Nil(t, res.Plan)
}

// TestDispatchPlan_InvalidJSONIsFailure: plan.json does not parse →
// FailureReason=plan_parse_error. Retries are decided upstream in
// processPlanning; dispatchPlan just surfaces the reason.
func TestDispatchPlan_InvalidJSONIsFailure(t *testing.T) {
	o, _, featureDir := dispatchPlanTestRig(t)
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "plan.json"),
		[]byte("not json at all"), 0o644))

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "bad json",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/bad-json",
	}
	require.NoError(t, o.db.Create(task).Error)

	res, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.Equal(t, "plan_parse_error", res.FailureReason)
}

// TestDispatchPlan_EmptySubtasksIsFailure: a plan with no subtasks is
// malformed per plan §6. Must not silently advance a task to plan_review
// with a zero-subtask plan; orch retries with feedback appended.
func TestDispatchPlan_EmptySubtasksIsFailure(t *testing.T) {
	o, _, featureDir := dispatchPlanTestRig(t)
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "plan.json"),
		[]byte(`{"subtasks": []}`), 0o644))

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "empty",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/empty",
	}
	require.NoError(t, o.db.Create(task).Error)

	res, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.Equal(t, "plan_validation_failed", res.FailureReason)
}

// TestDispatchPlan_InvalidTestsForIndexIsFailure: a plan whose tests_for
// points outside the subtasks slice is malformed per plan §6 bullet 3.
func TestDispatchPlan_InvalidTestsForIndexIsFailure(t *testing.T) {
	o, _, featureDir := dispatchPlanTestRig(t)
	// tests_for: [99] — there are only two subtasks, so index 99 is invalid.
	const body = `{
  "subtasks": [
    {"title":"t","description":"","agent_type":"coder","phase":"test","tests_for":[99],"files":["a.go"]},
    {"title":"i","description":"","agent_type":"coder","phase":"implementation","files":["b.go"]}
  ]
}`
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "plan.json"), []byte(body), 0o644))

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "bad index",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/bad-index",
	}
	require.NoError(t, o.db.Create(task).Error)

	res, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
		Model:  "claude-opus-4-6",
		Effort: "high",
		APIKey: "sk-ant",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.Equal(t, "plan_validation_failed", res.FailureReason)
}

// TestDispatchPlan_ExitCodeMapping walks every documented exit code from
// the planner entrypoint's contract and asserts PlanResult.FailureReason
// is mapped correctly per plans/warm-direct-planner.md §7.
func TestDispatchPlan_ExitCodeMapping(t *testing.T) {
	cases := []struct {
		exit       int
		planFile   bool
		wantReason string
		wantOK     bool
	}{
		{0, true, "", true},
		{0, false, "missing_plan_file", false},
		{1, false, "cli_error", false},
		{2, false, "precondition_failed", false},
		{137, false, "timeout", false},
		{42, false, "unknown", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.wantReason+"-"+itoaExit(tc.exit), func(t *testing.T) {
			o, fake, featureDir := dispatchPlanTestRig(t)
			fake.inspectResult = spawner.InspectWorkerResult{
				Status:   "exited",
				ExitCode: tc.exit,
			}
			if tc.planFile {
				writeValidPlanJSON(t, featureDir)
			}
			task := &model.Task{
				ID:             uuid.New(),
				ProjectID:      o.projectID,
				Title:          "exit-" + tc.wantReason,
				Status:         model.StatusPlanning,
				WorktreeBranch: "feature/exit-" + itoaExit(tc.exit),
			}
			require.NoError(t, o.db.Create(task).Error)

			res, err := o.dispatchPlan(context.Background(), task, "prompt", PlannerSpawnConfig{
				Model:  "claude-opus-4-6",
				Effort: "high",
				APIKey: "sk-ant",
			})
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Equal(t, tc.exit, res.ExitCode)
			require.Equal(t, tc.wantOK, res.Success, "Success for exit %d", tc.exit)
			require.Equal(t, tc.wantReason, res.FailureReason,
				"FailureReason for exit %d", tc.exit)
		})
	}
}
