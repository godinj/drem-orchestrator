package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
	dbpkg "github.com/godinj/drem-orchestrator/internal/db"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupContainerClassifyTest builds the narrow Orchestrator shape the
// container-dispatch helpers need — just a DB, logger, project, and the
// orchestrator fields that dispatchClassify / classifyViaContainer reach
// for. Reuses the in-memory DB pattern from classifying_test.go so tests
// stay fast.
func setupContainerClassifyTest(t *testing.T) (*Orchestrator, uuid.UUID) {
	t.Helper()

	gormDB, err := dbpkg.Init(":memory:")
	require.NoError(t, err)

	projectID := uuid.New()
	require.NoError(t, gormDB.Create(&model.Project{
		ID:            projectID,
		Name:          "container-classify-test",
		BareRepoPath:  "/tmp/unused",
		DefaultBranch: "main",
	}).Error)

	return &Orchestrator{
		db:        gormDB,
		projectID: projectID,
		events:    make(chan Event, 16),
		logger:    slog.Default().With("component", "container-classify-test"),
	}, projectID
}

// TestClassifyViaContainer_WritesFileAndReturnsTokens is the happy path:
// orch POSTs to the warm container, gets a 200 with a decision payload,
// writes classification-<taskID>.json into outputDir for the downstream
// onClassifierCompleted handler, and surfaces the returned token counts
// to its caller.
func TestClassifyViaContainer_WritesFileAndReturnsTokens(t *testing.T) {
	orch, projectID := setupContainerClassifyTest(t)

	taskID := uuid.New()
	task := &model.Task{
		ID:          taskID,
		ProjectID:   projectID,
		Title:       "refine typo",
		Description: "fix trailing whitespace",
		Status:      model.StatusClassifying,
	}
	require.NoError(t, orch.db.Create(task).Error)

	var lastReqBody []byte
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		lastReqBody, _ = io.ReadAll(r.Body)
		resp := classifyContainerResponse{
			TaskID:          taskID.String(),
			Category:        "quickfix",
			ComplexityScore: 2,
			Title:           "Fix typo (refined)",
			Description:     "One trailing space to drop",
			TargetFiles:     []string{"README.md"},
			Rationale:       "obvious mechanical fix",
			TokensIn:        812,
			TokensOut:       48,
			DurationMS:      947,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	orch.SetClassifierContainerEndpoint(srv.URL, "shhh-token")

	outDir := t.TempDir()
	tokensIn, tokensOut, err := orch.classifyViaContainer(
		agent.DefaultDirectClassifierConfig(), task, nil, outDir,
	)
	require.NoError(t, err)
	assert.Equal(t, 812, tokensIn)
	assert.Equal(t, 48, tokensOut)

	// Bearer token must be forwarded verbatim.
	assert.Equal(t, "Bearer shhh-token", gotAuth)

	// Request body must name the task being classified.
	var sentReq classifyContainerRequest
	require.NoError(t, json.Unmarshal(lastReqBody, &sentReq))
	assert.Equal(t, taskID.String(), sentReq.TaskID)
	assert.Equal(t, "refine typo", sentReq.Title)

	// The side-effect file onClassifierCompleted reads must exist and parse.
	outputPath := filepath.Join(outDir, fmt.Sprintf("classification-%s.json", taskID))
	data, err := os.ReadFile(outputPath)
	require.NoError(t, err, "classifyViaContainer must write classification-<taskID>.json")

	var fileOut ClassifierOutput
	require.NoError(t, json.Unmarshal(data, &fileOut))
	assert.Equal(t, "quickfix", fileOut.Category)
	assert.Equal(t, 2, fileOut.ComplexityScore)
	assert.Equal(t, "Fix typo (refined)", fileOut.Title)
	assert.Equal(t, []string{"README.md"}, fileOut.TargetFiles)
}

// TestClassifyViaContainer_PropagatesClarification verifies the
// clarification pathway round-trips so orch's handleClarificationRequest
// fires downstream.
func TestClassifyViaContainer_PropagatesClarification(t *testing.T) {
	orch, projectID := setupContainerClassifyTest(t)

	taskID := uuid.New()
	task := &model.Task{ID: taskID, ProjectID: projectID, Title: "ambiguous", Status: model.StatusClassifying}
	require.NoError(t, orch.db.Create(task).Error)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(classifyContainerResponse{
			TaskID:             taskID.String(),
			NeedsClarification: true,
			Questions:          []string{"Which module?", "Target branch?"},
			TokensIn:           100,
			TokensOut:          20,
		})
	}))
	defer srv.Close()

	orch.SetClassifierContainerEndpoint(srv.URL, "")

	outDir := t.TempDir()
	_, _, err := orch.classifyViaContainer(agent.DefaultDirectClassifierConfig(), task, nil, outDir)
	require.NoError(t, err)

	outputPath := filepath.Join(outDir, fmt.Sprintf("classification-%s.json", taskID))
	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	var fileOut ClassifierOutput
	require.NoError(t, json.Unmarshal(data, &fileOut))
	assert.True(t, fileOut.NeedsClarification)
	assert.Equal(t, []string{"Which module?", "Target branch?"}, fileOut.Questions)
}

// TestClassifyViaContainer_Upstream5xxReturnsError proves a bad-gateway from
// the classifier container surfaces as a non-nil error — the outer caller
// parks the task via onClassifierFailed.
func TestClassifyViaContainer_Upstream5xxReturnsError(t *testing.T) {
	orch, projectID := setupContainerClassifyTest(t)

	taskID := uuid.New()
	task := &model.Task{ID: taskID, ProjectID: projectID, Title: "x", Status: model.StatusClassifying}
	require.NoError(t, orch.db.Create(task).Error)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream"}`))
	}))
	defer srv.Close()

	orch.SetClassifierContainerEndpoint(srv.URL, "")

	_, _, err := orch.classifyViaContainer(
		agent.DefaultDirectClassifierConfig(), task, nil, t.TempDir(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

// TestDispatchClassify_FallsBackToInlineWhenEndpointUnset verifies the
// rollback-safety invariant: if SetClassifierContainerEndpoint was never
// called, dispatchClassify must still produce a classification file via the
// inline agent.RunDirectClassifier path. We stand up a fake SGLang server
// and point the DirectClassifierConfig.Endpoint at it so the inline call
// succeeds end-to-end without touching a live model.
func TestDispatchClassify_FallsBackToInlineWhenEndpointUnset(t *testing.T) {
	orch, projectID := setupContainerClassifyTest(t)

	taskID := uuid.New()
	task := &model.Task{
		ID:          taskID,
		ProjectID:   projectID,
		Title:       "fix a typo",
		Description: "one char off",
		Status:      model.StatusClassifying,
	}
	require.NoError(t, orch.db.Create(task).Error)

	// Fake SGLang chat completion — the inline path calls agent.Classify
	// which POSTs to this URL. No container endpoint set.
	sglang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": `{"category":"quickfix","complexity_score":1,"title":"t","description":"d","target_files":[],"rationale":"r"}`},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 77, "completion_tokens": 12},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer sglang.Close()

	cfg := agent.DirectClassifierConfig{
		Endpoint:    sglang.URL,
		Model:       "gemma4-26b",
		MaxTokens:   256,
		Temperature: 0.1,
		Timeout:     5 * time.Second,
	}

	outDir := t.TempDir()
	tokensIn, tokensOut, err := orch.dispatchClassify(cfg, task, nil, outDir)
	require.NoError(t, err)
	assert.Equal(t, 77, tokensIn)
	assert.Equal(t, 12, tokensOut)

	// File must be present so downstream handler can pick it up.
	_, err = os.Stat(filepath.Join(outDir, fmt.Sprintf("classification-%s.json", taskID)))
	require.NoError(t, err)
}

// TestDispatchClassify_RoutesToContainerWhenEndpointSet proves the branch:
// when SetClassifierContainerEndpoint is configured, dispatchClassify must
// POST to *that* URL, not to the SGLang endpoint in DirectClassifierConfig.
func TestDispatchClassify_RoutesToContainerWhenEndpointSet(t *testing.T) {
	orch, projectID := setupContainerClassifyTest(t)

	taskID := uuid.New()
	task := &model.Task{ID: taskID, ProjectID: projectID, Title: "x", Status: model.StatusClassifying}
	require.NoError(t, orch.db.Create(task).Error)

	containerHit := false
	classifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		containerHit = true
		_ = json.NewEncoder(w).Encode(classifyContainerResponse{
			TaskID: taskID.String(), Category: "quickfix", ComplexityScore: 2, Title: "t",
			Description: "d", Rationale: "r", TokensIn: 10, TokensOut: 5,
		})
	}))
	defer classifier.Close()

	// Separate fake server whose job is to prove it's *never* hit — if orch
	// accidentally picked the inline path this handler would fire.
	sglangHit := false
	sglang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sglangHit = true
	}))
	defer sglang.Close()

	orch.SetClassifierContainerEndpoint(classifier.URL, "")

	cfg := agent.DirectClassifierConfig{
		Endpoint: sglang.URL,
		Model:    "gemma4-26b",
		Timeout:  5 * time.Second,
	}
	tokensIn, tokensOut, err := orch.dispatchClassify(cfg, task, nil, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, 10, tokensIn)
	assert.Equal(t, 5, tokensOut)
	assert.True(t, containerHit, "classifier container must be hit when endpoint is set")
	assert.False(t, sglangHit, "SGLang endpoint must NOT be hit when container is routed")
}

// TestProcessClassifyingTasksDirect_UsesContainerPath exercises the full
// loop end to end: a task in CLASSIFYING, a configured container endpoint,
// a 200 response, and the task transitions to BACKLOG with the container's
// decision fields persisted.
func TestProcessClassifyingTasksDirect_UsesContainerPath(t *testing.T) {
	orch, projectID := setupContainerClassifyTest(t)
	// Give the fake worktree a real main-worktree dir so the classify
	// output file has somewhere to land.
	bareDir := t.TempDir()
	mainWT := filepath.Join(bareDir, "main")
	require.NoError(t, os.MkdirAll(mainWT, 0o755))
	orch.worktree = &FakeWorktreeManager{BarePath: bareDir, Default: "main"}

	task := testutil.CreateTask(t, orch.db, projectID, "trivial bug", model.StatusClassifying)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(classifyContainerResponse{
			TaskID:          task.ID.String(),
			Category:        "quickfix",
			ComplexityScore: 3,
			Title:           "Refined bug title",
			Description:     "enriched description",
			TargetFiles:     []string{"a.go"},
			Rationale:       "because",
			TokensIn:        200,
			TokensOut:       40,
		})
	}))
	defer srv.Close()

	cfg := agent.DefaultDirectClassifierConfig()
	orch.directClassifierCfg = &cfg
	orch.SetClassifierContainerEndpoint(srv.URL, "")

	orch.processClassifyingTasksDirect()

	var reloaded model.Task
	require.NoError(t, orch.db.First(&reloaded, "id = ?", task.ID).Error)
	assert.Equal(t, model.StatusBacklog, reloaded.Status)
	assert.Equal(t, "Refined bug title", reloaded.Title)
	assert.Equal(t, 3, reloaded.ComplexityScore)
	assert.Equal(t, model.CategoryQuickFix, reloaded.Category)
}
