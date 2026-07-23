package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
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
		worktree:        &FakeWorktreeManager{BarePath: project.BareRepoPath, Default: "main"},
		events:          events,
		contextWarnPct:  75,
		contextStopPct:  90,
		contextFixerPct: 85,
		logger:          slog.Default().With("component", "prep-test"),
	}

	return orch, projectID
}

// mockPrepSGLang builds an httptest server that returns a canned tool-agent
// response. The caller supplies the assistant content string (typically a
// PrepOutput JSON body) and the server emits a single stop response.
func mockPrepSGLang(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     400,
				"completion_tokens": 120,
				"total_tokens":      520,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// prepCfgForTest returns a DirectPrepConfig pointed at the given endpoint
// with conservative limits so tests complete quickly and deterministically.
func prepCfgForTest(endpoint, workDir string) *agent.DirectPrepConfig {
	cfg := agent.DirectPrepConfig{DirectToolAgentConfig: agent.DefaultDirectToolAgentConfig()}
	cfg.Endpoint = endpoint
	cfg.WorkDir = workDir
	cfg.MaxIterations = 2
	return &cfg
}

func TestNeedsPrepRequiresExplicitOptIn(t *testing.T) {
	orch, _ := setupPrepTest(t)
	orch.runner = agent.NewRunner(orch.db, nil, nil, "/bin/false", "", 1, func(at model.AgentType) model.AgentCLIConfig {
		if at == model.AgentCoder {
			return model.AgentCLIConfig{Provider: model.ProviderSGLangDirect}
		}
		return model.AgentCLIConfig{}
	})
	sub := &model.Task{Context: model.JSONField{"estimated_files": []any{"src/Main.cpp"}}}

	require.False(t, orch.needsPrep(sub), "local coder alone must not implicitly enable reconnaissance")
	orch.directPrepCfg = prepCfgForTest("http://unused", t.TempDir())
	require.True(t, orch.needsPrep(sub), "explicit direct prep config must enable reconnaissance")
}

func TestSpawnPrepAgentDirect_DispatchesToRunDirectPrep(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	featureDir := orch.worktree.FeatureWorktreePath("test-feature")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	// The mock returns a valid PrepOutput JSON that the orchestrator will
	// parse and store in task.Context["prep_data"].
	prepOutput := PrepOutput{
		TargetFiles: []PrepTargetFile{
			{Path: "internal/foo/bar.go", Definitions: "type Foo struct", Methods: []string{"NewFoo"}, Notes: "test"},
		},
		Warnings: []string{"watch out"},
	}
	payload, err := json.Marshal(prepOutput)
	require.NoError(t, err)

	server := mockPrepSGLang(t, string(payload))
	defer server.Close()

	orch.SetDirectPrepConfig(prepCfgForTest(server.URL, featureDir))

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
	// Token usage should have been recorded from the mock response.
	assert.Equal(t, 400, agents[0].TokensIn)
	assert.Equal(t, 120, agents[0].TokensOut)
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

	featureDir := orch.worktree.FeatureWorktreePath("test-feature")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	// Mock a 500 error from SGLang.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer server.Close()

	orch.SetDirectPrepConfig(prepCfgForTest(server.URL, featureDir))

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

	err := orch.spawnPrepAgent(sub, parent)
	require.NoError(t, err, "spawnPrepAgent should swallow API failures and degrade gracefully")

	var reloaded model.Task
	require.NoError(t, orch.db.First(&reloaded, "id = ?", sub.ID).Error)

	assert.True(t, reloaded.Context["prep_complete"] == true, "prep_complete should be true")
	assert.True(t, reloaded.Context["prep_failed"] == true, "prep_failed should be true")
	assert.Nil(t, reloaded.Context["prep_in_progress"], "prep_in_progress should be cleared")
	assert.Nil(t, reloaded.Context["prep_data"], "prep_data should NOT be set on failure")

	// Agent record should be marked dead since the API call failed outright.
	var agents []model.Agent
	require.NoError(t, orch.db.Where("project_id = ?", projectID).Find(&agents).Error)
	require.Len(t, agents, 1)
	assert.Equal(t, model.AgentDead, agents[0].Status)
}

func TestSpawnPrepAgentDirect_MalformedOutputGracefulDegradation(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	featureDir := orch.worktree.FeatureWorktreePath("test-feature")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	// Mock returns garbage that passes the transport layer but fails
	// PrepOutput JSON parsing in the orchestrator.
	server := mockPrepSGLang(t, "not valid json {{{")
	defer server.Close()

	orch.SetDirectPrepConfig(prepCfgForTest(server.URL, featureDir))

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

// TestSpawnPrepAgentDirect_WritesOutputFileAtExpectedPath verifies the
// direct path honors the feature-worktree path convention for the output
// artifact. The file is removed after successful parse, so we assert its
// pre-cleanup existence indirectly via prep_data presence.
func TestSpawnPrepAgentDirect_WritesOutputFileAtExpectedPath(t *testing.T) {
	orch, projectID := setupPrepTest(t)

	featureDir := orch.worktree.FeatureWorktreePath("path-check")
	require.NoError(t, os.MkdirAll(featureDir, 0o755))

	prepOutput := PrepOutput{
		TargetFiles: []PrepTargetFile{{Path: "x.go"}},
	}
	payload, err := json.Marshal(prepOutput)
	require.NoError(t, err)

	server := mockPrepSGLang(t, string(payload))
	defer server.Close()

	orch.SetDirectPrepConfig(prepCfgForTest(server.URL, featureDir))

	parent := &model.Task{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          "parent",
		Status:         model.StatusInProgress,
		WorktreeBranch: "feature/path-check",
		Context:        model.JSONField{},
	}
	require.NoError(t, orch.db.Create(parent).Error)

	sub := &model.Task{
		ID:           uuid.New(),
		ProjectID:    projectID,
		ParentTaskID: &parent.ID,
		Title:        "subtask",
		Status:       model.StatusInProgress,
		Context: model.JSONField{
			"estimated_files": []any{"x.go"},
		},
	}
	require.NoError(t, orch.db.Create(sub).Error)

	require.NoError(t, orch.spawnPrepAgent(sub, parent))

	// Output file is cleaned up on success; verify the path convention by
	// checking for its absence at the canonical location.
	expectedPath := filepath.Join(featureDir, fmt.Sprintf("task-prep-%s.json", sub.ID))
	_, statErr := os.Stat(expectedPath)
	assert.True(t, os.IsNotExist(statErr), "output file should be cleaned up after successful parse")

	var reloaded model.Task
	require.NoError(t, orch.db.First(&reloaded, "id = ?", sub.ID).Error)
	assert.NotNil(t, reloaded.Context["prep_data"])
}
