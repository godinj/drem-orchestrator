package orchestrator

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/gitref"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/spawner"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// planRoutingRig builds an Orchestrator wired with both a fake spawner
// and a real Runner whose AgentConfig function the test can control. The
// returned featureDir is the fake worktree manager's feature path — tests
// drop plan.json there to simulate a real planner container.
func planRoutingRig(t *testing.T, plannerCfg model.AgentCLIConfig) (*Orchestrator, *fakeWorkerSpawner, string, model.Task) {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, &gitref.BranchRef{})
	projectID := uuid.New()
	require.NoError(t, db.Create(&model.Project{
		ID:            projectID,
		Name:          "plan-routing-test",
		BareRepoPath:  "/tmp/fake-bare",
		DefaultBranch: "main",
	}).Error)

	bareRepo := t.TempDir()
	featureDir := t.TempDir()

	fake := &fakeWorkerSpawner{
		inspectResult: spawner.InspectWorkerResult{Status: "exited", ExitCode: 0},
	}
	wt := &FakeWorktreeManager{
		BarePath:              bareRepo,
		Default:               "main",
		OnFeatureWorktreePath: func(name string) string { return featureDir },
	}

	// Runner with maxConcurrent=1 so CanSpawn returns true. agentConfigs
	// routes AgentPlanner to the test-supplied plannerCfg; everything
	// else gets a benign default. Worktree arg is nil — the routing tests
	// never exercise SpawnAgent, so the runner's own worktree manager
	// stays unused.
	cfg := plannerCfg
	runner := agent.NewRunner(db, nil, nil, "/bin/false", "", 1, func(at model.AgentType) model.AgentCLIConfig {
		if at == model.AgentPlanner {
			return cfg
		}
		return model.AgentCLIConfig{Effort: "medium"}
	})

	o := &Orchestrator{
		db:             db,
		projectID:      projectID,
		events:         make(chan Event, 32),
		worktree:       wt,
		runner:         runner,
		logger:         slog.Default().With("component", "plan_routing_test"),
		Spawner:        fake,
		GitrefRegistry: gitref.NewRegistry(db),
	}

	task := model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "plan routing test",
		Description:    "test",
		Status:         model.StatusPlanning,
		WorktreeBranch: "feature/routing",
	}
	require.NoError(t, db.Create(&task).Error)
	return o, fake, featureDir, task
}

// TestShouldSpawnPlannerContainer_TrueWhenClaudeProviderAndSpawner: the
// positive routing case. Provider claude + spawner configured → container
// path.
func TestShouldSpawnPlannerContainer_TrueWhenClaudeProviderAndSpawner(t *testing.T) {
	o, _, _, _ := planRoutingRig(t, model.AgentCLIConfig{
		Provider: model.ProviderClaude,
		Model:    "claude-opus-4-6",
	})
	require.True(t, o.shouldSpawnPlannerContainer())
}

// TestShouldSpawnPlannerContainer_TrueWhenProviderEmpty: empty provider
// defaults to claude (EffectiveProvider). Container path applies.
func TestShouldSpawnPlannerContainer_TrueWhenProviderEmpty(t *testing.T) {
	o, _, _, _ := planRoutingRig(t, model.AgentCLIConfig{
		Provider: "",
		Model:    "claude-opus-4-6",
	})
	require.True(t, o.shouldSpawnPlannerContainer())
}

// TestShouldSpawnPlannerContainer_FalseWhenSpawnerNil: no spawner → legacy
// path even if provider is claude.
func TestShouldSpawnPlannerContainer_FalseWhenSpawnerNil(t *testing.T) {
	o, _, _, _ := planRoutingRig(t, model.AgentCLIConfig{
		Provider: model.ProviderClaude,
	})
	o.Spawner = nil
	require.False(t, o.shouldSpawnPlannerContainer())
}

// TestShouldSpawnPlannerContainer_FalseWhenNonClaudeProvider: operator
// override (e.g. sglang-direct) routes through the legacy runner path
// for rollback safety. Not the default, but must not be silently
// overridden.
func TestShouldSpawnPlannerContainer_FalseWhenNonClaudeProvider(t *testing.T) {
	o, _, _, _ := planRoutingRig(t, model.AgentCLIConfig{
		Provider: model.ProviderSGLangDirect,
		Model:    "gemma4-26b",
	})
	require.False(t, o.shouldSpawnPlannerContainer())
}

// TestSpawnPlannerContainer_CallsDispatchPlanAndStoresPlan asserts the
// container path parses plan.json into task.Plan and clears any stale
// agent assignment so the "plan already exists" branch at the top of
// processPlanning advances the task on the next tick.
func TestSpawnPlannerContainer_CallsDispatchPlanAndStoresPlan(t *testing.T) {
	o, fake, featureDir, task := planRoutingRig(t, model.AgentCLIConfig{
		Provider: model.ProviderClaude,
		Model:    "claude-opus-4-6",
		Effort:   "high",
	})
	writeValidPlanJSON(t, featureDir)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-routing")

	require.NoError(t, o.spawnPlannerContainer(&task, "prompt body"))

	// Spawner saw exactly one call with the right argv.
	require.Len(t, fake.spawnCalls, 1)
	require.Equal(t, "planner", fake.spawnCalls[0].AgentType)
	require.Equal(t, "sk-ant-routing", fake.spawnCalls[0].Env["ANTHROPIC_API_KEY"])

	// plan.json was written onto the task.
	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.NotNil(t, updated.Plan)
	require.Contains(t, updated.Plan, "subtasks")

	// Planner-spawn counter incremented.
	require.Equal(t, float64(1), updated.Context["total_planner_spawns"])
}

// TestSpawnPlannerContainer_FailClosedWithoutAPIKey: orch env has no
// ANTHROPIC_API_KEY → dispatchPlan errors → spawnPlannerContainer must
// emit an event and leave the task in PLANNING for the next tick rather
// than burning a spawn budget on a guaranteed-failure container.
func TestSpawnPlannerContainer_FailClosedWithoutAPIKey(t *testing.T) {
	o, fake, _, task := planRoutingRig(t, model.AgentCLIConfig{
		Provider: model.ProviderClaude,
		Model:    "claude-opus-4-6",
	})
	t.Setenv("ANTHROPIC_API_KEY", "")

	err := o.spawnPlannerContainer(&task, "prompt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
	require.Empty(t, fake.spawnCalls,
		"no spawner call must be issued when API key missing")

	// Task must remain in PLANNING with no plan.
	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPlanning, updated.Status)
	require.Nil(t, updated.Plan)
}

// TestSpawnPlannerContainer_ValidationFailureDoesNotStorePlan: planner
// container exits 0 but plan.json is malformed → task.Plan stays nil so
// processPlanning spawns again next tick (or fails once the spawn cap is
// hit). The attempt counts against total_planner_spawns.
func TestSpawnPlannerContainer_ValidationFailureDoesNotStorePlan(t *testing.T) {
	o, _, featureDir, task := planRoutingRig(t, model.AgentCLIConfig{
		Provider: model.ProviderClaude,
		Model:    "claude-opus-4-6",
	})
	// empty subtasks: fails validatePlanJSON
	require.NoError(t, os.WriteFile(filepath.Join(featureDir, "plan.json"),
		[]byte(`{"subtasks": []}`), 0o644))
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-routing")

	err := o.spawnPlannerContainer(&task, "prompt")
	require.NoError(t, err, "validation failure should not surface as a Go error")

	var updated model.Task
	require.NoError(t, o.db.First(&updated, "id = ?", task.ID).Error)
	require.Nil(t, updated.Plan, "no plan must be stored when validation failed")
	require.Equal(t, float64(1), updated.Context["total_planner_spawns"],
		"attempt still counts against the spawn budget")
}
