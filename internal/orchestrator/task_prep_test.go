package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

func setupPrepTest(t *testing.T) (*Orchestrator, uuid.UUID) {
	t.Helper()

	gormDB, err := dbpkg.Init(":memory:")
	if err != nil {
		t.Fatalf("init test db: %v", err)
	}

	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "prep-test-project",
		BareRepoPath:  t.TempDir(),
		DefaultBranch: "main",
	}
	require.NoError(t, gormDB.Create(&project).Error)

	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:              gormDB,
		projectID:       projectID,
		worktree:        &worktree.Manager{BareRepoPath: project.BareRepoPath, DefaultBranch: "main"},
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "prep-test"),
	}

	return orch, projectID
}

func TestSpawnPrepAgentDirect_DispatchesToRunDirectPrep(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	cfg := agent.DirectPrepConfig{
		DirectToolAgentConfig: agent.DefaultDirectToolAgentConfig(),
	}
	orch.SetDirectPrepConfig(&cfg)

	featureDir := orch.worktree.FeatureWorktreePath("test-feature")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	parent := &model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "parent task",
		Description:    "parent desc",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-feature",
		Context:        model.JSONField{},
	}
	require.NoError(t, orch.db.Create(parent).Error)

	sub := &model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parent.ID,
		Title:        "subtask",
		Description:  "subtask desc",
		Status:       model.StatusInProgress,
		Context: model.JSONField{
			"estimated_files": []any{"internal/foo/bar.go"},
		},
	}
	require.NoError(t, orch.db.Create(sub).Error)

	// Write a valid PrepOutput JSON to the expected output path so the
	// direct path can parse it after RunDirectPrep returns (stub returns nil, nil).
	prepOutput := PrepOutput{
		TargetFiles: []PrepTargetFile{
			{Path: "internal/foo/bar.go", Definitions: "type Foo struct", Methods: []string{"NewFoo"}, Notes: "test"},
		},
		Warnings: []string{"watch out"},
	}
	outputPath := filepath.Join(featureDir, fmt.Sprintf("task-prep-%s.json", sub.ID))
	data, err := json.Marshal(prepOutput)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outputPath, data, 0o644))

	err = orch.spawnPrepAgent(sub, parent)
	require.NoError(t, err)

	// Reload from DB.
	var reloaded model.Task
	require.NoError(t, orch.db.First(&reloaded, "id = ?", sub.ID).Error)

	assert.True(t, reloaded.Context["prep_complete"] == true, "prep_complete should be true")
	assert.Nil(t, reloaded.Context["prep_in_progress"], "prep_in_progress should be cleared")
	assert.NotNil(t, reloaded.Context["prep_data"], "prep_data should be set")

	// Verify an agent record was created with sglang-direct provider.
	var agents []model.Agent
	require.NoError(t, orch.db.Where("project_id = ?", projectID).Find(&agents).Error)
	require.Len(t, agents, 1)
	assert.Equal(t, "sglang-direct", agents[0].Provider)
	assert.Equal(t, model.AgentPrep, agents[0].AgentType)
	assert.Equal(t, model.AgentIdle, agents[0].Status)
}

func TestSpawnPrepAgent_FallsBackToSubprocessWhenNilConfig(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	// directPrepCfg is nil by default — should NOT take the direct path.
	assert.Nil(t, orch.directPrepCfg)

	parent := &model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "parent task",
		Description:    "parent desc",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-feature",
		Context:        model.JSONField{},
	}
	require.NoError(t, orch.db.Create(parent).Error)

	sub := &model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parent.ID,
		Title:        "subtask",
		Description:  "subtask desc",
		Status:       model.StatusInProgress,
		Context: model.JSONField{
			"estimated_files": []any{"internal/foo/bar.go"},
		},
	}
	require.NoError(t, orch.db.Create(sub).Error)

	// With nil runner AND nil directPrepCfg, the subprocess path will panic
	// on o.runner.SpawnAgent(). The panic confirms the subprocess path was
	// taken, not the direct path.
	assert.Panics(t, func() {
		_ = orch.spawnPrepAgent(sub, parent)
	}, "should panic calling runner.SpawnAgent on nil runner")

	// No direct agent record should exist.
	var agents []model.Agent
	require.NoError(t, orch.db.Where("project_id = ? AND provider = ?", projectID, "sglang-direct").Find(&agents).Error)
	assert.Empty(t, agents, "no direct agent should be created when config is nil")
}

func TestSpawnPrepAgentDirect_APIFailureGracefulDegradation(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	cfg := agent.DirectPrepConfig{
		DirectToolAgentConfig: agent.DefaultDirectToolAgentConfig(),
	}
	orch.SetDirectPrepConfig(&cfg)

	featureDir := orch.worktree.FeatureWorktreePath("test-feature")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	parent := &model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "parent task",
		Description:    "parent desc",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-feature",
		Context:        model.JSONField{},
	}
	require.NoError(t, orch.db.Create(parent).Error)

	sub := &model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parent.ID,
		Title:        "subtask",
		Description:  "subtask desc",
		Status:       model.StatusInProgress,
		Context: model.JSONField{
			"estimated_files": []any{"internal/foo/bar.go"},
		},
	}
	require.NoError(t, orch.db.Create(sub).Error)

	// RunDirectPrep stub returns (nil, nil). The direct path will then try to
	// read the output file, which won't exist — triggering the read-failure
	// graceful degradation path.
	err := orch.spawnPrepAgent(sub, parent)
	require.NoError(t, err)

	var reloaded model.Task
	require.NoError(t, orch.db.First(&reloaded, "id = ?", sub.ID).Error)

	assert.True(t, reloaded.Context["prep_complete"] == true, "prep_complete should be true")
	assert.True(t, reloaded.Context["prep_failed"] == true, "prep_failed should be true")
	assert.Nil(t, reloaded.Context["prep_in_progress"], "prep_in_progress should be cleared")
	assert.Nil(t, reloaded.Context["prep_data"], "prep_data should NOT be set on failure")
}

func TestSpawnPrepAgentDirect_MalformedOutputGracefulDegradation(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	cfg := agent.DirectPrepConfig{
		DirectToolAgentConfig: agent.DefaultDirectToolAgentConfig(),
	}
	orch.SetDirectPrepConfig(&cfg)

	featureDir := orch.worktree.FeatureWorktreePath("test-feature")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	parent := &model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "parent task",
		Description:    "parent desc",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/test-feature",
		Context:        model.JSONField{},
	}
	require.NoError(t, orch.db.Create(parent).Error)

	sub := &model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parent.ID,
		Title:        "subtask",
		Description:  "subtask desc",
		Status:       model.StatusInProgress,
		Context: model.JSONField{
			"estimated_files": []any{"internal/foo/bar.go"},
		},
	}
	require.NoError(t, orch.db.Create(sub).Error)

	// Write malformed JSON to the output path.
	outputPath := filepath.Join(featureDir, fmt.Sprintf("task-prep-%s.json", sub.ID))
	require.NoError(t, os.WriteFile(outputPath, []byte("not valid json {{{"), 0o644))

	err := orch.spawnPrepAgent(sub, parent)
	require.NoError(t, err)

	var reloaded model.Task
	require.NoError(t, orch.db.First(&reloaded, "id = ?", sub.ID).Error)

	assert.True(t, reloaded.Context["prep_complete"] == true, "prep_complete should be true")
	assert.True(t, reloaded.Context["prep_failed"] == true, "prep_failed should be true")
	assert.Nil(t, reloaded.Context["prep_in_progress"], "prep_in_progress should be cleared")
	assert.Nil(t, reloaded.Context["prep_data"], "prep_data should NOT be set on malformed output")

	// Agent should be marked idle (it completed, just output was bad).
	var agents []model.Agent
	require.NoError(t, orch.db.Where("project_id = ? AND provider = ?", projectID, "sglang-direct").Find(&agents).Error)
	require.Len(t, agents, 1)
	assert.Equal(t, model.AgentIdle, agents[0].Status)
}
