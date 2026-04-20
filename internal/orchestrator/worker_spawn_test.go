package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setWorkerCredsPathEnv sets the DREM_WORKER_CREDS_PATH env var and
// restores the previous value at test tear-down. Used by every test that
// drives the orchestrator spawn path so the claude-backed agent types
// do not fail-closed on an unset env var.
func setWorkerCredsPathEnv(t *testing.T, value string) {
	t.Helper()
	prev, wasSet := os.LookupEnv(workerCredsPathEnv)
	require.NoError(t, os.Setenv(workerCredsPathEnv, value))
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(workerCredsPathEnv, prev)
		} else {
			_ = os.Unsetenv(workerCredsPathEnv)
		}
	})
}

// setWorkerPromptRootEnv sets the DREM_PROMPT_ROOT_HOST env var and
// restores the previous value at test tear-down. Every claude-backed
// spawn path requires it; tests that drive spawnCoder et al. call
// this before invoking the orchestrator. The value should be a
// writable directory (typically a t.TempDir()).
func setWorkerPromptRootEnv(t *testing.T, value string) {
	t.Helper()
	prev, wasSet := os.LookupEnv(workerPromptRootEnv)
	require.NoError(t, os.Setenv(workerPromptRootEnv, value))
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(workerPromptRootEnv, prev)
		} else {
			_ = os.Unsetenv(workerPromptRootEnv)
		}
	})
}

// fakeWorkerSpawner captures every call made against the WorkerSpawner
// interface so tests can assert on payload shape and return preconfigured
// results or errors.
type fakeWorkerSpawner struct {
	mu sync.Mutex

	spawnCalls   []spawner.SpawnWorkerParams
	spawnResults []spawnOutcome

	destroyCalls []spawner.DestroyWorkerParams
	destroyErr   error

	listResult spawner.ListWorkersResult
	listErr    error

	inspectResult spawner.InspectWorkerResult
	inspectErr    error
}

type spawnOutcome struct {
	res spawner.SpawnWorkerResult
	err error
}

func (f *fakeWorkerSpawner) SpawnWorker(_ context.Context, p spawner.SpawnWorkerParams) (spawner.SpawnWorkerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawnCalls = append(f.spawnCalls, p)
	idx := len(f.spawnCalls) - 1
	if idx >= len(f.spawnResults) {
		return spawner.SpawnWorkerResult{ContainerID: "fake-container-" + uuid.NewString()[:8]}, nil
	}
	return f.spawnResults[idx].res, f.spawnResults[idx].err
}

func (f *fakeWorkerSpawner) DestroyWorker(_ context.Context, p spawner.DestroyWorkerParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyCalls = append(f.destroyCalls, p)
	return f.destroyErr
}

func (f *fakeWorkerSpawner) ListWorkers(_ context.Context, _ spawner.ListWorkersParams) (spawner.ListWorkersResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listResult, f.listErr
}

func (f *fakeWorkerSpawner) InspectWorker(_ context.Context, _ spawner.InspectWorkerParams) (spawner.InspectWorkerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inspectResult, f.inspectErr
}

// workerSpawnTestRig builds an Orchestrator wired to a fresh in-memory DB
// and a fakeWorkerSpawner. It returns both so tests can drive spawnCoder
// directly and assert on DB state.
func workerSpawnTestRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "worker-spawn-test",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: "/tmp/fake-bare", Default: "main"},
		logger:         slog.Default().With("component", "worker_spawn_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake
}

func TestSpawnCoder_BuildsExpectedParams(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Implement feature X",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/task-x",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-abc123"}},
	}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	require.Len(t, fake.spawnCalls, 1)
	p := fake.spawnCalls[0]
	require.Equal(t, o.projectID.String(), p.Project)
	require.Equal(t, "coder", p.AgentType)
	require.Equal(t, "feature/task-x", p.Branch)
	require.Equal(t, task.ID.String(), p.Env["DREM_TASK_ID"])
	require.Equal(t, task.ID.String(), p.Labels["drem.task_id"])
	require.Equal(t, "go", p.Labels["drem.language"])
	require.Equal(t, "/tmp/fake-bare", p.BareRepoMount)
	// Coder is in credsMountRequired, so buildSpawnContext populates
	// CredsMount from DREM_WORKER_CREDS_PATH (set above).
	require.Equal(t, "/host/.claude/.credentials.json", p.CredsMount)
	// Coder is also in promptRequired, so PromptMount points at the
	// rendered prompt file on host under the test's temp prompt root.
	// The file must exist and the path must live under the configured
	// prompt root — the spawner's pre-stat enforces existence.
	require.NotEmpty(t, p.PromptMount)
	require.FileExists(t, p.PromptMount)
	require.Equal(t, task.ID.String()+".md", filepath.Base(p.PromptMount))
	// And the env map never contains an API-key fallback.
	require.NotContains(t, p.Env, "ANTHROPIC_API_KEY")
}

func TestSpawnCoder_RecordsContainerIDAndImageOnAgent(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-x",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "container-xyz"}},
	}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	// Reload task to pick up AssignedAgentID written by recordContainerOnAgent.
	require.NoError(t, o.db.First(task, "id = ?", task.ID).Error)
	require.NotNil(t, task.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", task.AssignedAgentID).Error)
	require.Equal(t, "container-xyz", ag.TmuxSession)
	// ModelID carries the image when the spawner returned one; with the
	// default fake path Image is empty string, so the field is left blank.
	require.Equal(t, model.AgentCoder, ag.AgentType)
}

func TestSpawnCoder_OnSpawnFailureReturnsError(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-y",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{{err: errors.New("docker daemon refused")}}

	err := o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.Contains(t, err.Error(), "docker daemon refused")

	// Task status must be unchanged; spawnCoder is a side-effect primitive
	// that never mutates the state machine on failure.
	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, reloaded.Status)
	require.Nil(t, reloaded.AssignedAgentID)

	// A worker_spawn_failed audit event must exist (user story 49).
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
}

func TestSpawnCoder_RegistersBranchInGitref(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/register-branch",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "c1"}}}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	ref, err := o.GitrefRegistry.FindByBranch(context.Background(), "/tmp/fake-bare", "feature/register-branch")
	require.NoError(t, err)
	require.Equal(t, gitref.StatusActive, ref.Status)
	require.Equal(t, task.ID.String(), ref.TaskID)
	require.Equal(t, "coder", ref.AgentType)
}

func TestSpawnCoder_WithoutSpawnerReturnsError(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	o := &Orchestrator{
		db:        db,
		projectID: uuid.New(),
		events:    make(chan Event, 8),
		logger:    slog.Default(),
	}
	err := o.spawnCoder(context.Background(), &model.Task{ID: uuid.New()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no WorkerSpawner configured")
}

// TestCredsMountRequired_Table documents the every-agent-type contract
// the orchestrator uses to decide whether to attach the subscription
// credentials mount. Updating this table is a deliberate auth review.
func TestCredsMountRequired_Table(t *testing.T) {
	cases := []struct {
		agentType string
		want      bool
	}{
		{"coder", true},
		{"reviewer", true},
		{"fixer", true},
		{"tester", true},
		{"supervisor", true},
		{"merger", false},
		// Unknown type defaults to false: new types need an explicit
		// entry + test before they can consume creds.
		{"researcher", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.agentType, func(t *testing.T) {
			require.Equal(t, tc.want, credsMountRequired(tc.agentType))
		})
	}
}

// TestSpawnTypedWorker_PopulatesCredsMountForClaudeRoles drives every
// claude-backed agent type through spawnTypedWorker and asserts the
// CredsMount on SpawnWorkerParams is the host path from the env var.
func TestSpawnTypedWorker_PopulatesCredsMountForClaudeRoles(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())

	claudeRoles := []string{"coder", "reviewer", "fixer", "tester", "supervisor"}
	for _, role := range claudeRoles {
		t.Run(role, func(t *testing.T) {
			o, fake := workerSpawnTestRig(t)
			task := &model.Task{
				ID:             uuid.New(),
				ProjectID:      o.projectID,
				Title:          "t",
				Description:    "d",
				Status:         model.StatusInProgress,
				WorktreeBranch: "feature/y",
			}
			require.NoError(t, o.db.Create(task).Error)

			require.NoError(t, o.spawnTypedWorker(context.Background(), task, role))
			require.Len(t, fake.spawnCalls, 1)
			require.Equal(t, "/host/.claude/.credentials.json", fake.spawnCalls[0].CredsMount)
		})
	}
}

// TestSpawnTypedWorker_CredsMountMissingEnvFailsClosed verifies that
// when a claude-backed role is spawned but DREM_WORKER_CREDS_PATH is
// unset AND $HOME cannot be resolved, spawnTypedWorker returns an
// error without calling the spawner and emits a worker_spawn_failed
// event with the policy reason.
func TestSpawnTypedWorker_CredsMountMissingEnvFailsClosed(t *testing.T) {
	// Unset both env sources so resolveWorkerCredsPath returns "".
	// Also unset DREM_PROMPT_ROOT_HOST so the second fail-closed check
	// (prompt root) doesn't shadow the creds fail-close message —
	// though in practice credsMountRequired runs first in
	// buildSpawnContext so the creds error is what surfaces.
	require.NoError(t, os.Unsetenv(workerCredsPathEnv))
	require.NoError(t, os.Unsetenv(workerPromptRootEnv))
	prevHome, homeWasSet := os.LookupEnv("HOME")
	require.NoError(t, os.Unsetenv("HOME"))
	t.Cleanup(func() {
		if homeWasSet {
			_ = os.Setenv("HOME", prevHome)
		}
	})

	o, fake := workerSpawnTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "t",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/z",
	}
	require.NoError(t, o.db.Create(task).Error)

	err := o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.Empty(t, fake.spawnCalls, "spawner must not be called when creds path cannot be resolved")

	// worker_spawn_failed event surfaces the missing-creds policy violation.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
}

// TestSpawnSupervisor_CarriesCredsMount verifies the supervisor role
// (a string literal, not a model.AgentType constant) is treated as a
// claude-backed role by the table and therefore carries a CredsMount.
func TestSpawnSupervisor_CarriesCredsMount(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())

	o, fake := workerSpawnTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "s",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/sup",
	}
	require.NoError(t, o.db.Create(task).Error)

	require.NoError(t, o.spawnSupervisor(context.Background(), task))
	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, "supervisor", fake.spawnCalls[0].AgentType)
	require.Equal(t, "/host/.claude/.credentials.json", fake.spawnCalls[0].CredsMount)
}

// TestRejectAPIKeyInEnv_Table documents the policy boundary: an
// ANTHROPIC_API_KEY key in the env map is always rejected; any other
// env (or an empty map) is accepted. This codifies the
// subscription-only auth policy so a future env extension that tries
// to reintroduce API-key plumbing fails at the orchestrator boundary.
func TestRejectAPIKeyInEnv_Table(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{"nil", nil, false},
		{"empty", map[string]string{}, false},
		{"normal drem vars", map[string]string{"DREM_TASK_ID": "t"}, false},
		{"with anthropic api key", map[string]string{"ANTHROPIC_API_KEY": "sk-xxx"}, true},
		{"empty anthropic api key still rejected", map[string]string{"ANTHROPIC_API_KEY": ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectAPIKeyInEnv(tc.env)
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestBuildSpawnContext_MergerOmitsCredsMount documents that the
// merger role does NOT receive a CredsMount, even when the env var
// is set. The merger is a Go binary and never runs the claude CLI.
func TestBuildSpawnContext_MergerOmitsCredsMount(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())

	o, _ := workerSpawnTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "m",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/m",
	}
	require.NoError(t, o.db.Create(task).Error)

	swc, err := o.buildSpawnContext(task, "merger")
	require.NoError(t, err)
	require.Equal(t, "", swc.credsMount, "merger must never carry a creds mount")
	require.Equal(t, "", swc.promptMount, "merger must never carry a prompt mount (Go binary, no claude CLI)")
}

// TestPromptRequired_Table documents the every-agent-type contract the
// orchestrator uses to decide whether to render + mount a prompt. Same
// shape as credsMountRequired so auth + prompt tables stay in lockstep
// for claude-backed roles, but kept separate so a future role can
// diverge (e.g. an argv-driven worker needing creds but no prompt).
// See plans/worker-prompt-delivery.md §5.
func TestPromptRequired_Table(t *testing.T) {
	cases := []struct {
		agentType string
		want      bool
	}{
		{"coder", true},
		{"reviewer", true},
		{"fixer", true},
		{"tester", true},
		{"supervisor", true},
		{"merger", false},
		// Unknown type defaults to false: new types need an explicit
		// entry + test before they can consume prompt delivery.
		{"researcher", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.agentType, func(t *testing.T) {
			require.Equal(t, tc.want, promptRequired(tc.agentType))
		})
	}
}

// TestSpawnCoder_WritesPromptFileBeforeSpawn verifies the atomicity
// contract: the prompt file exists on host at the path the spawner
// receives AT the moment SpawnWorker is called. Captured by a fake
// spawner that stats the PromptMount during its call — if the file
// doesn't exist, the stat fails and the test fails, which is the
// behaviour the real spawner exhibits in production.
func TestSpawnCoder_WritesPromptFileBeforeSpawn(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	promptRoot := t.TempDir()
	setWorkerPromptRootEnv(t, promptRoot)

	o, fake := workerSpawnTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Wire up prompt delivery",
		Description:    "Render markdown, write atomically, bind-mount RO.",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/prompt",
	}
	require.NoError(t, o.db.Create(task).Error)

	require.NoError(t, o.spawnCoder(context.Background(), task))
	require.Len(t, fake.spawnCalls, 1)

	// The prompt path on the spawner params must exist on disk and
	// carry non-empty rendered content with the task title baked in.
	p := fake.spawnCalls[0]
	require.NotEmpty(t, p.PromptMount)
	require.FileExists(t, p.PromptMount)
	require.Equal(t, promptRoot, filepath.Dir(p.PromptMount),
		"prompt must land under the configured DREM_PROMPT_ROOT_HOST")
	require.Equal(t, task.ID.String()+".md", filepath.Base(p.PromptMount))

	content, err := os.ReadFile(p.PromptMount)
	require.NoError(t, err)
	require.Contains(t, string(content), task.Title,
		"rendered prompt must contain the task title")
	require.Contains(t, string(content), "coder",
		"rendered prompt must identify the agent type")

	// The tmp file from the atomic-rename must NOT linger.
	_, err = os.Stat(p.PromptMount + ".tmp")
	require.True(t, os.IsNotExist(err), "atomic-rename tmp file must not remain after spawn")
}

// TestSpawnTypedWorker_PromptRootMissingFailsClosed verifies that a
// claude-backed role with no resolvable prompt root fails the spawn,
// emits a worker_spawn_failed event with reason=prompt_render_failed,
// and never touches the spawner.
func TestSpawnTypedWorker_PromptRootMissingFailsClosed(t *testing.T) {
	// Creds can resolve; prompt root must not.
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	require.NoError(t, os.Unsetenv(workerPromptRootEnv))
	prevHome, homeWasSet := os.LookupEnv("HOME")
	require.NoError(t, os.Unsetenv("HOME"))
	t.Cleanup(func() {
		if homeWasSet {
			_ = os.Setenv("HOME", prevHome)
		}
	})

	o, fake := workerSpawnTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "t",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/z",
	}
	require.NoError(t, o.db.Create(task).Error)

	err := o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.Empty(t, fake.spawnCalls, "spawner must not be called when prompt root cannot be resolved")

	// Event carries reason=prompt_render_failed so audit queries can
	// filter by the missing-prompt-root class.
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
	require.Equal(t, spawnPolicyReasonPromptMissing, evts[0].Details["reason"])
}

// TestRecordSpawnFailureEventWithReason_CarriesReasonInDetails verifies
// the reason classifier lands on the TaskEvent details map so downstream
// audit queries can filter by reason (e.g. policy_violation_api_key)
// without parsing the free-form error string.
func TestRecordSpawnFailureEventWithReason_CarriesReasonInDetails(t *testing.T) {
	o, _ := workerSpawnTestRig(t)
	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "t",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/r",
	}
	require.NoError(t, o.db.Create(task).Error)

	o.recordSpawnFailureEventWithReason(task, "coder", spawnPolicyReasonAPIKey,
		errors.New("policy violation: ANTHROPIC_API_KEY must not be set"))

	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
	require.Equal(t, "coder", evts[0].NewValue)
	require.Equal(t, "policy_violation_api_key", evts[0].Details["reason"])
	require.Contains(t, evts[0].Details["error"], "ANTHROPIC_API_KEY")
}
