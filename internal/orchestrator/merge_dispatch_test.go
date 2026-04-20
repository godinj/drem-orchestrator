package orchestrator

import (
	"context"
	"log/slog"
	"testing"

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

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	cmd := fake.spawnCalls[0].Cmd
	require.NotEmpty(t, cmd, "Cmd must be populated with merger argv")

	want := map[string]string{
		"--feature-branch":  "feature/argv-test",
		"--project":         o.projectID.String(),
		"--task-id":         task.ID.String(),
		"--test-cmd":        "go test ./...",
		"--orch-url":        "http://orch:8080",
		"--agentmon-token":  "test-token-abc",
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

	_, err := o.dispatchMerge(context.Background(), task)
	require.NoError(t, err)

	require.Len(t, fake.spawnCalls, 1)
	require.True(t, fake.spawnCalls[0].BareRepoReadWrite,
		"merger must request read-write /bare mount")
	require.Equal(t, "/tmp/fake-bare", fake.spawnCalls[0].BareRepoMount)
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

			res, err := o.dispatchMerge(context.Background(), task)
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Equal(t, tc.exit, res.ExitCode)
			require.Equal(t, tc.wantOK, res.Success, "Success for exit %d", tc.exit)
			require.Equal(t, tc.wantReason, res.FailureReason,
				"FailureReason for exit %d", tc.exit)
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
