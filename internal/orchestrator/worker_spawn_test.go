package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/branchpolicy"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/internal/workeridentity"
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
	spawnEntered chan struct{}
	spawnRelease chan struct{}

	destroyCalls []spawner.DestroyWorkerParams
	destroyErr   error

	listResult spawner.ListWorkersResult
	listErr    error

	inspectResult spawner.InspectWorkerResult
	inspectErr    error
	inspectCalls  []spawner.InspectWorkerParams
}

type spawnOutcome struct {
	res spawner.SpawnWorkerResult
	err error
}

func (f *fakeWorkerSpawner) SpawnWorker(_ context.Context, p spawner.SpawnWorkerParams) (spawner.SpawnWorkerResult, error) {
	f.mu.Lock()
	f.spawnCalls = append(f.spawnCalls, p)
	idx := len(f.spawnCalls) - 1
	f.mu.Unlock()
	if f.spawnEntered != nil {
		select {
		case f.spawnEntered <- struct{}{}:
		default:
		}
	}
	if f.spawnRelease != nil {
		<-f.spawnRelease
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeWorkerSpawner) InspectWorker(_ context.Context, p spawner.InspectWorkerParams) (spawner.InspectWorkerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls = append(f.inspectCalls, p)
	return f.inspectResult, f.inspectErr
}

// workerSpawnTestRig builds an Orchestrator wired to a fresh in-memory DB
// and a fakeWorkerSpawner. It returns the orchestrator, the fake, and the
// bare repo path — tests that seed parent branches use the bare path to
// call pushTestFeatureBranch before dispatch. The bare repo is real (not
// a /tmp/fake-bare literal) because spawnTypedWorker pre-creates the
// subtask branch via gitref.EnsureBranch, which requires a real git
// object database.
func workerSpawnTestRig(t *testing.T) (*Orchestrator, *fakeWorkerSpawner, string) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	bareRepo := testutil.SetupBareRepo(t)
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "worker-spawn-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}).Error)

	fake := &fakeWorkerSpawner{}
	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		projectName:    "worker-spawn-test",
		events:         make(chan Event, 32),
		worktree:       &FakeWorktreeManager{BarePath: bareRepo, Default: "main"},
		logger:         slog.Default().With("component", "worker_spawn_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}
	return o, fake, bareRepo
}

// pushTestFeatureBranch creates a new branch in a bare repo with a seed
// commit so tests that seed a parent task with a non-default
// WorktreeBranch have that branch actually present in the ref database.
// Mirrors internal/gitref/git_test.go::pushBranch but lives here so
// orchestrator tests can reuse it without crossing the package boundary.
func pushTestFeatureBranch(t *testing.T, bareRepo, branch string) {
	t.Helper()
	work := t.TempDir()
	testutil.AddWorktree(t, bareRepo, branch, work)
	testutil.CommitFile(t, work, "seed.txt", "seed\n", "seed "+branch)
}

func TestSpawnCoder_PreflightRejectsNonWritableBranchMetadata(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, bareRepo := workerSpawnTestRig(t)

	refsHead := filepath.Join(bareRepo, "refs", "heads")
	info, err := os.Stat(refsHead)
	require.NoError(t, err)
	oldMode := info.Mode().Perm()
	require.NoError(t, os.Chmod(refsHead, oldMode&^0222))
	t.Cleanup(func() { _ = os.Chmod(refsHead, oldMode) })

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Implement feature X",
		Description:    "desc",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/preflight-denied",
	}
	require.NoError(t, o.db.Create(task).Error)

	err = o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.Contains(t, err.Error(), branchpolicy.ReasonBranchPermission)
	require.Len(t, fake.spawnCalls, 0)

	var evt model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").First(&evt).Error)
	require.Equal(t, branchpolicy.ReasonBranchPermission, evt.Details["reason"])
}

func TestSpawnCoder_ConcurrentDuplicateDoesNotLaunchSecondContainer(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)
	fake.spawnEntered = make(chan struct{}, 1)
	fake.spawnRelease = make(chan struct{})
	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "container-first"}}}

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Implement race guard",
		Description:    "desc",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/race-guard",
	}
	require.NoError(t, o.db.Create(task).Error)

	firstDone := make(chan error, 1)
	go func() { firstDone <- o.spawnCoder(context.Background(), task) }()

	select {
	case <-fake.spawnEntered:
	case <-time.After(time.Second):
		t.Fatal("first spawn did not reach fake spawner")
	}

	secondErr := o.spawnCoder(context.Background(), task)
	require.Error(t, secondErr)
	require.ErrorIs(t, secondErr, workeridentity.ErrTaskAlreadyClaimed)

	close(fake.spawnRelease)
	require.NoError(t, <-firstDone)

	fake.mu.Lock()
	spawnCalls := append([]spawner.SpawnWorkerParams(nil), fake.spawnCalls...)
	fake.mu.Unlock()
	require.Len(t, spawnCalls, 1, "duplicate reservation must fail before container launch")

	var attempts []model.WorkerAttempt
	require.NoError(t, o.db.Where("task_id = ?", task.ID).Find(&attempts).Error)
	require.Len(t, attempts, 1)
	require.Equal(t, model.WorkerAttemptRunning, attempts[0].State)
	require.Equal(t, "container-first", attempts[0].ContainerID)

	var failures []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&failures).Error)
	require.Len(t, failures, 1)
	require.Equal(t, "worker_already_active", failures[0].Details["reason"])
}

func TestSpawnCoder_BuildsExpectedParams(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, bareRepo := workerSpawnTestRig(t)

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
	// Dual-label contract (plans/dual-label-worker-spawn.md): Project
	// carries the human-readable name (maps to drem.project; matches
	// agentmon's DREM_PROJECT env filter); ProjectID carries the stable
	// UUID (maps to drem.project_id; used by every internal orch filter).
	// Both MUST be populated or the v13-v14 silent-outage regressions.
	require.Equal(t, "worker-spawn-test", p.Project,
		"SpawnWorkerParams.Project must be the project name so drem.project label matches agentmon's DREM_PROJECT filter")
	require.Equal(t, o.projectID.String(), p.ProjectID,
		"SpawnWorkerParams.ProjectID must be the project UUID so drem.project_id label matches orch's internal filters")
	// Env mirrors the name for worker-side logging.
	require.Equal(t, "worker-spawn-test", p.Env["DREM_PROJECT"])
	require.Equal(t, "coder", p.AgentType)
	require.Equal(t, "feature/task-x", p.Branch)
	require.Equal(t, task.ID.String(), p.Env["DREM_TASK_ID"])
	require.Equal(t, task.ID.String(), p.Labels["drem.task_id"])
	require.Equal(t, "go", p.Labels["drem.language"])
	require.Equal(t, bareRepo, p.BareRepoMount)
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
	// /bare must be mounted read-write so the worker's watchdog can
	// push committed in-flight work to the feature branch. A read-only
	// mount would surface as "remote unpack failed" inside the
	// container with no local orch-side signal. See
	// plans/worker-bare-mount-rw.md.
	require.True(t, p.BareRepoReadWrite,
		"workers need /bare mounted rw so the watchdog can push commits")
}

func TestSpawnCoder_UsesSGLangDirectContainerHarness(t *testing.T) {
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)
	o.SetDirectToolAgentConfig(&agent.DirectToolAgentConfig{
		Endpoint:                 "http://gq:8090/v1/chat/completions",
		Model:                    "gemma4-26b",
		MaxTokens:                2048,
		MaxIterations:            7,
		MaxCumulativeInputTokens: 12345,
		MaxReadsBeforeMutation:   4,
		// Keep enough cumulative headroom for the configured 18k
		// pre-mutation ceiling. Smaller totals are deliberately clamped by
		// DirectToolAgentConfig.ForPhase to reserve one mutation turn.
		TestMaxCumulativeInputTokens:     40000,
		TestMaxReadsBeforeMutation:       8,
		MaxToolCalls:                     12,
		TestMaxInputTokensBeforeMutation: 18000,
		Temperature:                      0.2,
		Timeout:                          30 * time.Second,
		ChatTemplateKwargs:               map[string]any{"enable_thinking": false},
		ToolArgumentsFormat:              agent.ToolArgumentsString,
	})
	o.deliveryPolicy.VerificationPolicy = model.VerificationExternalAck
	o.runner = agent.NewRunner(o.db, nil, nil, "/bin/false", "", 1, func(at model.AgentType) model.AgentCLIConfig {
		require.Equal(t, model.AgentCoder, at)
		return model.AgentCLIConfig{Provider: model.ProviderSGLangDirect, Model: "gemma4-26b"}
	})

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Run sglang-direct in a container harness",
		Description:    "d",
		Status:         model.StatusInProgress,
		Phase:          "test",
		WorktreeBranch: "feature/no-container-sglang",
		Context: model.JSONField{
			"estimated_files":            []any{"tests/unit/test_marker.cpp"},
			"writable_files":             []any{"tests/unit/test_marker.cpp"},
			"planned_interface_contract": `{"kind":"planned_api","expected_missing_symbols":["Marker::set()"]}`,
			"internal_retry_failure":     "must not leak",
		},
	}
	require.NoError(t, o.db.Create(task).Error)

	require.NoError(t, o.spawnCoder(context.Background(), task))
	require.Len(t, fake.spawnCalls, 1)
	p := fake.spawnCalls[0]
	require.Equal(t, "sglang-direct", p.Env["DREM_AGENT_HARNESS"])
	require.Equal(t, "http://gq:8090/v1/chat/completions", p.Env["DREM_DIRECT_ENDPOINT"])
	require.Equal(t, "gemma4-26b", p.Env["DREM_MODEL"])
	require.Equal(t, "2048", p.Env["DREM_DIRECT_MAX_TOKENS"])
	iterations, err := strconv.Atoi(p.Env["DREM_DIRECT_MAX_ITERATIONS"])
	require.NoError(t, err)
	require.GreaterOrEqual(t, iterations, 10)
	cumulative, err := strconv.Atoi(p.Env["DREM_DIRECT_MAX_CUMULATIVE_INPUT_TOKENS"])
	require.NoError(t, err)
	require.Greater(t, cumulative, 40000)
	require.Equal(t, "8", p.Env["DREM_DIRECT_MAX_READS_BEFORE_MUTATION"])
	toolCalls, err := strconv.Atoi(p.Env["DREM_DIRECT_MAX_TOOL_CALLS"])
	require.NoError(t, err)
	require.GreaterOrEqual(t, toolCalls, 16)
	preMutation, err := strconv.Atoi(p.Env["DREM_DIRECT_MAX_INPUT_TOKENS_BEFORE_MUTATION"])
	require.NoError(t, err)
	require.GreaterOrEqual(t, preMutation, 18000)
	require.LessOrEqual(t, preMutation, cumulative-20000)
	require.Equal(t, "true", p.Env["DREM_DIRECT_PROTECT_EXISTING_FILES"])
	require.Equal(t, "0.2", p.Env["DREM_DIRECT_TEMPERATURE"])
	require.Equal(t, "30s", p.Env["DREM_DIRECT_TIMEOUT"])
	require.JSONEq(t, `{"enable_thinking":false}`, p.Env["DREM_DIRECT_CHAT_TEMPLATE_KWARGS"])
	require.Equal(t, agent.ToolArgumentsString, p.Env["DREM_DIRECT_TOOL_ARGUMENTS_FORMAT"])
	require.Equal(t, "coder", p.Env["DREM_GQ_CALLER"])
	require.Equal(t, "normal", p.Env["DREM_GQ_PRIORITY"])
	require.JSONEq(t, `["tests/unit/test_marker.cpp"]`, p.Env["DREM_SCOPED_FILES_JSON"])
	require.Empty(t, p.CredsMount)
	require.NotEmpty(t, p.PromptMount)
	rawPrompt, err := os.ReadFile(p.PromptMount)
	require.NoError(t, err)
	rendered := string(rawPrompt)
	require.Contains(t, rendered, "TEST phase")
	require.Contains(t, rendered, "Marker::set()")
	require.Contains(t, rendered, "/home/drem/work")
	require.Contains(t, rendered, task.WorktreeBranch)
	require.NotContains(t, rendered, "Repository Map")
	require.NotContains(t, rendered, "must not leak")
	require.Less(t, len(rendered), 8000)

	integrationTask := *task
	integrationTask.ID = uuid.New()
	integrationTask.Title = "Validate assembled change"
	integrationTask.Phase = "integration"
	integrationTask.Context["estimated_files"] = []any{"src/ui/ActionCoordinator.cpp", "cmake/DremCanvasSources.cmake"}
	integrationTask.Context["writable_files"] = []any{"cmake/DremCanvasSources.cmake"}
	integrationTask.WorktreeBranch = "feature/read-only-integration"
	integrationTask.AssignedAgentID = nil
	require.NoError(t, o.db.Create(&integrationTask).Error)
	require.NoError(t, o.spawnCoder(context.Background(), &integrationTask))
	require.Len(t, fake.spawnCalls, 2)
	require.JSONEq(t, `["cmake/DremCanvasSources.cmake"]`, fake.spawnCalls[1].Env["DREM_SCOPED_FILES_JSON"])
	require.Equal(t, "true", fake.spawnCalls[1].Env["DREM_DIRECT_ALLOW_READ_ONLY_COMPLETION"])
	require.Empty(t, fake.spawnCalls[1].Env["DREM_DIRECT_PROTECT_EXISTING_FILES"])

	reworkTask := integrationTask
	reworkTask.ID = uuid.New()
	reworkTask.Title = "Repair failed exact artifact"
	reworkTask.WorktreeBranch = "feature/delivery-rework"
	reworkTask.AssignedAgentID = nil
	reworkTask.Context["delivery_rework_pending"] = true
	require.NoError(t, o.db.Create(&reworkTask).Error)
	require.NoError(t, o.spawnCoder(context.Background(), &reworkTask))
	require.Len(t, fake.spawnCalls, 3)
	require.Empty(t, fake.spawnCalls[2].Env["DREM_DIRECT_ALLOW_READ_ONLY_COMPLETION"])
	require.Equal(t, "true", fake.spawnCalls[2].Env["DREM_DIRECT_PROTECT_EXISTING_FILES"])
}

func TestSpawnCoder_RecordsContainerIDAndModelMetadataOnAgent(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)
	o.runner = agent.NewRunner(o.db, nil, nil, "/bin/false", "", 1, func(at model.AgentType) model.AgentCLIConfig {
		require.Equal(t, model.AgentCoder, at)
		return model.AgentCLIConfig{
			Provider: model.ProviderOpenCode,
			Model:    "ollama/qwen3-coder",
			Effort:   "minimal",
		}
	})

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

	// Reload task to pick up AssignedAgentID written by worker identity recording.
	require.NoError(t, o.db.First(task, "id = ?", task.ID).Error)
	require.NotNil(t, task.AssignedAgentID)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", task.AssignedAgentID).Error)
	require.Equal(t, "container-xyz", ag.TmuxSession)
	require.Equal(t, "opencode", ag.Provider)
	require.Equal(t, "ollama/qwen3-coder", ag.ModelID)
	require.Equal(t, "minimal", ag.Effort)
	require.Equal(t, model.AgentCoder, ag.AgentType)
	require.Equal(t, "opencode", fake.spawnCalls[0].Env["DREM_AGENT_HARNESS"])
	require.Equal(t, "ollama/qwen3-coder", fake.spawnCalls[0].Env["DREM_MODEL"])
	require.Equal(t, "minimal", fake.spawnCalls[0].Env["DREM_EFFORT"])

	var attempt model.WorkerAttempt
	require.NoError(t, o.db.First(&attempt, "task_id = ?", task.ID).Error)
	require.Equal(t, task.ID, attempt.TaskID)
	require.NotNil(t, attempt.AgentID)
	require.Equal(t, ag.ID, *attempt.AgentID)
	require.Equal(t, "container-xyz", attempt.ContainerID)
	require.Equal(t, fake.spawnCalls[0].WorkerID, attempt.WorkerID)

	var spawn model.TaskEvent
	require.NoError(t, o.db.First(&spawn, "task_id = ? AND event_type = ?", task.ID, "worker_spawned").Error)
	require.Equal(t, attempt.ID.String(), spawn.Details["attempt_id"])
}

func TestSpawnCoder_OnSpawnFailureReturnsError(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)

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
	require.Empty(t, fake.destroyCalls, "no container exists when SpawnWorker fails")

	var attempt model.WorkerAttempt
	require.NoError(t, o.db.First(&attempt, "task_id = ?", task.ID).Error)
	require.Equal(t, model.WorkerAttemptAborted, attempt.State)
	require.NotNil(t, attempt.CompletedAt)

	// A worker_spawn_failed audit event must exist (user story 49).
	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?", task.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
}

func TestSpawnCoder_FinalizeFailureDestroysContainerAndClearsReservation(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/finalize-fails",
	}
	require.NoError(t, o.db.Create(task).Error)
	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "container-finalize-fails"}}}

	failed := false
	workerAttemptUpdates := 0
	o.db.Callback().Update().Before("gorm:update").Register("test_fail_finalize_attempt_once", func(tx *gorm.DB) {
		if failed || tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "WorkerAttempt" {
			return
		}
		workerAttemptUpdates++
		if workerAttemptUpdates < 2 {
			return
		}
		failed = true
		tx.AddError(errors.New("injected finalize failure"))
	})

	err := o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.Contains(t, err.Error(), "finalize identity")
	require.True(t, failed)
	require.Len(t, fake.destroyCalls, 1)
	require.Equal(t, "container-finalize-fails", fake.destroyCalls[0].ContainerID)

	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.Nil(t, reloaded.AssignedAgentID)

	var attempt model.WorkerAttempt
	require.NoError(t, o.db.First(&attempt, "task_id = ?", task.ID).Error)
	require.Equal(t, model.WorkerAttemptAborted, attempt.State)
	require.NotNil(t, attempt.CompletedAt)
}

func TestSpawnCoder_ConcurrentSameTaskCallsSpawnerOnce(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)
	fake.spawnEntered = make(chan struct{}, 1)
	fake.spawnRelease = make(chan struct{})
	fake.spawnResults = []spawnOutcome{{res: spawner.SpawnWorkerResult{ContainerID: "container-one"}}}

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Test",
		Description:    "x",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/concurrent",
	}
	require.NoError(t, o.db.Create(task).Error)

	firstErr := make(chan error, 1)
	go func() { firstErr <- o.spawnCoder(context.Background(), task) }()
	<-fake.spawnEntered

	secondErr := o.spawnCoder(context.Background(), task)
	require.Error(t, secondErr)
	require.True(t, errors.Is(secondErr, workeridentity.ErrTaskAlreadyClaimed))
	close(fake.spawnRelease)
	require.NoError(t, <-firstErr)

	require.Len(t, fake.spawnCalls, 1)
	var attempts []model.WorkerAttempt
	require.NoError(t, o.db.Where("task_id = ? AND completed_at IS NULL", task.ID).Find(&attempts).Error)
	require.Len(t, attempts, 1)
	require.Equal(t, model.WorkerAttemptRunning, attempts[0].State)

	var reloaded model.Task
	require.NoError(t, o.db.First(&reloaded, "id = ?", task.ID).Error)
	require.NotNil(t, reloaded.AssignedAgentID)
}

func TestSpawnCoder_RegistersBranchInGitref(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, bareRepo := workerSpawnTestRig(t)

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

	ref, err := o.GitrefRegistry.FindByBranch(context.Background(), bareRepo, "feature/register-branch")
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
			o, fake, _ := workerSpawnTestRig(t)
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

	o, fake, _ := workerSpawnTestRig(t)
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

	o, fake, _ := workerSpawnTestRig(t)
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

	o, _, _ := workerSpawnTestRig(t)
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

	o, fake, _ := workerSpawnTestRig(t)
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

	o, fake, _ := workerSpawnTestRig(t)
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
	o, _, _ := workerSpawnTestRig(t)
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

// TestSpawnTypedWorker_SubtaskBranchesOffParent verifies the branch
// derivation contract for container-dispatched subtasks: when a task
// has a ParentTaskID, spawnTypedWorker pre-creates the subtask's
// feature branch in the bare repo off of the parent's WorktreeBranch.
// Regression coverage for the T3 canary v5 failure where the worker's
// `git clone --branch` hit a missing ref because nothing created the
// branch between planning and spawn.
func TestSpawnTypedWorker_SubtaskBranchesOffParent(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, bareRepo := workerSpawnTestRig(t)

	// Seed the parent's integration branch with one real commit so
	// gitref.EnsureBranch can fork the subtask off it.
	parentBranch := "feature/parent-x"
	pushTestFeatureBranch(t, bareRepo, parentBranch)

	parentID := uuid.New()
	parent := &model.Task{
		ID:             parentID,
		ProjectID:      o.projectID,
		Title:          "Parent feature",
		Description:    "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: parentBranch,
	}
	require.NoError(t, o.db.Create(parent).Error)

	subtask := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		ParentTaskID:   &parentID,
		Title:          "Sub",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/sub-x",
	}
	require.NoError(t, o.db.Create(subtask).Error)

	require.NoError(t, o.spawnCoder(context.Background(), subtask))

	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, "feature/sub-x", fake.spawnCalls[0].Branch)

	// Post-conditions on the bare repo: subtask branch exists and its
	// tip matches the parent branch's tip (forked cleanly).
	ctx := context.Background()
	subExists, err := gitref.BranchExists(ctx, bareRepo, "feature/sub-x")
	require.NoError(t, err)
	require.True(t, subExists, "subtask branch must be created in the bare repo before the worker clone")

	parentTip, err := gitref.HeadCommit(ctx, bareRepo, parentBranch)
	require.NoError(t, err)
	subTip, err := gitref.HeadCommit(ctx, bareRepo, "feature/sub-x")
	require.NoError(t, err)
	require.Equal(t, parentTip, subTip,
		"freshly-created subtask branch must point at the parent's tip")
}

// TestSpawnTypedWorker_IdempotentPreservesInFlightCommits is the
// anti-clobber guarantee wired at the orchestrator level: a respawn
// against a feature branch that has already advanced (because a prior
// worker pushed commits before dying) must NOT rewind the branch to
// its source tip. Without this, the event-driven respawn loop would
// destroy work every time a worker container exited non-zero.
func TestSpawnTypedWorker_IdempotentPreservesInFlightCommits(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, bareRepo := workerSpawnTestRig(t)

	// Pre-create the feature branch with an in-flight commit on top of
	// the default branch, mirroring "worker pushed then died".
	pushTestFeatureBranch(t, bareRepo, "feature/live")

	ctx := context.Background()
	tipBefore, err := gitref.HeadCommit(ctx, bareRepo, "feature/live")
	require.NoError(t, err)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Respawn after crash",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/live",
	}
	require.NoError(t, o.db.Create(task).Error)

	require.NoError(t, o.spawnCoder(ctx, task))
	require.Len(t, fake.spawnCalls, 1)

	tipAfter, err := gitref.HeadCommit(ctx, bareRepo, "feature/live")
	require.NoError(t, err)
	require.Equal(t, tipBefore, tipAfter,
		"respawn must not rewind an in-flight worker's pushed commits")
}

// TestRecordContainerOnAgent_CreatePathPopulatesBranchAndTask is the
// regression test for the v13 canary failure: when the spawn path
// creates a synthetic agent row for a container worker (no prior
// AssignedAgentID on the task), the row MUST carry WorktreeBranch and
// CurrentTaskID so the reconcile_stuck.go commit-check guard
// (`featureDir != "" && ag.WorktreeBranch != ""`) can route a
// post-push container exit through synthesizeCompletion rather than
// failing the task with "agent session died without producing commits."
//
// Before this fix, the create-synthetic branch in
// the old container recording path omitted WorktreeBranch entirely; the update
// path omitted it AND the task/agent-type coupling. Both are covered
// by this pair of tests.
func TestRecordContainerOnAgent_CreatePathPopulatesBranchAndTask(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Branch must flow to agent row",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/abc",
	}
	require.NoError(t, o.db.Create(task).Error)

	fake.spawnResults = []spawnOutcome{
		{res: spawner.SpawnWorkerResult{ContainerID: "c-create-path"}},
	}

	require.NoError(t, o.spawnCoder(context.Background(), task))

	// Reload task + agent from the DB and assert the agent row
	// carries the branch/task/type coupling the reconciler needs.
	require.NoError(t, o.db.First(task, "id = ?", task.ID).Error)
	require.NotNil(t, task.AssignedAgentID,
		"worker identity recording must attach a synthetic agent when none was assigned")

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", task.AssignedAgentID).Error)

	require.Equal(t, "feature/abc", ag.WorktreeBranch,
		"agent row must carry the task's feature branch so reconcile_stuck.go can check for commits")
	require.NotNil(t, ag.CurrentTaskID,
		"agent row must carry CurrentTaskID so the reconciler can join agent → task")
	require.Equal(t, task.ID, *ag.CurrentTaskID,
		"CurrentTaskID must match the spawning task")
	require.Equal(t, model.AgentCoder, ag.AgentType)
	require.Equal(t, "c-create-path", ag.TmuxSession)
}

// TestSpawnCoder_PreAssignedTaskFailsBeforeExternalSpawn codifies the
// reservation CAS: container worker spawn only claims tasks whose
// assigned_agent_id is NULL. A pre-assigned task is already owned and
// must not be updated by attaching a new external container.
func TestSpawnCoder_PreAssignedTaskFailsBeforeExternalSpawn(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	setWorkerPromptRootEnv(t, t.TempDir())
	o, fake, _ := workerSpawnTestRig(t)

	task := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		Title:          "Existing agent update path",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/def",
	}
	require.NoError(t, o.db.Create(task).Error)

	preExisting := model.Agent{
		ID:        uuid.New(),
		ProjectID: o.projectID,
		AgentType: model.AgentCoder,
		Name:      "legacy-agent",
		Status:    model.AgentIdle,
	}
	require.NoError(t, o.db.Create(&preExisting).Error)
	task.AssignedAgentID = &preExisting.ID
	require.NoError(t, o.db.Save(task).Error)

	err := o.spawnCoder(context.Background(), task)
	require.Error(t, err)
	require.True(t, errors.Is(err, workeridentity.ErrTaskAlreadyClaimed))
	require.Empty(t, fake.spawnCalls)

	var ag model.Agent
	require.NoError(t, o.db.First(&ag, "id = ?", preExisting.ID).Error)
	require.Empty(t, ag.TmuxSession)
	require.Empty(t, ag.WorktreeBranch)
	require.Nil(t, ag.CurrentTaskID)
}

// TestSpawnTypedWorker_SubtaskWithMissingParentBranchFailsClosed
// verifies the fail-closed posture for a subtask whose parent carries
// no WorktreeBranch. Silently falling back to main would mask an
// upstream planning gap; instead, the spawner is NOT called and a
// worker_spawn_failed event lands with reason=branch_missing so the
// operator can surface the planning miss.
func TestSpawnTypedWorker_SubtaskWithMissingParentBranchFailsClosed(t *testing.T) {
	setWorkerCredsPathEnv(t, "/host/.claude/.credentials.json")
	o, fake, _ := workerSpawnTestRig(t)

	parentID := uuid.New()
	parent := &model.Task{
		ID:          parentID,
		ProjectID:   o.projectID,
		Title:       "Parent with no branch",
		Description: "d",
		Status:      model.StatusInProgress,
		// WorktreeBranch deliberately empty.
	}
	require.NoError(t, o.db.Create(parent).Error)

	subtask := &model.Task{
		ID:             uuid.New(),
		ProjectID:      o.projectID,
		ParentTaskID:   &parentID,
		Title:          "Orphaned sub",
		Description:    "d",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/sub-orphan",
	}
	require.NoError(t, o.db.Create(subtask).Error)

	err := o.spawnCoder(context.Background(), subtask)
	require.Error(t, err)
	require.Empty(t, fake.spawnCalls, "spawner must not be called when parent branch is missing")

	var evts []model.TaskEvent
	require.NoError(t, o.db.Where("task_id = ? AND event_type = ?",
		subtask.ID, "worker_spawn_failed").Find(&evts).Error)
	require.Len(t, evts, 1)
	require.Equal(t, spawnPolicyReasonBranchMissing, evts[0].Details["reason"],
		"audit event must carry the branch_missing classifier so operators can filter")
}
