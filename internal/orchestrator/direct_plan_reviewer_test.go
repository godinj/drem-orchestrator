package orchestrator

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// setupDirectPlanReviewerTest creates an orchestrator backed by a real DB,
// bare repo, and worktree manager, suitable for testing the direct plan
// reviewer path end-to-end against an httptest server.
func setupDirectPlanReviewerTest(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	db := testutil.NewTestDB(t)
	bareRepo := setupTestRepoWithMainBranch(t)

	projectID := uuid.New()
	project := model.Project{
		ID:            projectID,
		Name:          "direct-plan-reviewer-test",
		BareRepoPath:  bareRepo,
		DefaultBranch: "main",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	host := NewHostManager(bareRepo, "main")
	events := make(chan Event, 100)
	orch := &Orchestrator{
		db:        db,
		projectID: projectID,
		worktree:  host.AsInterface(),
		events:    events,
		logger:    slog.Default().With("component", "direct-plan-reviewer-test"),
	}
	// A runner is required so that the subprocess path fails cleanly instead
	// of panicking when the fall-through test exercises it.
	orch.runner = agent.NewRunner(db, nil, host.AsAgentWorktreeManager(), "/nonexistent/claude", "", 0, nil)
	return orch, bareRepo
}

// ---------------------------------------------------------------------------
// Setter stores + logs
// ---------------------------------------------------------------------------

func TestSetDirectPlanReviewerConfig(t *testing.T) {
	orch, _ := setupDirectPlanReviewerTest(t)

	// Initially nil.
	if orch.directPlanReviewerCfg != nil {
		t.Fatalf("expected nil initial config, got %+v", orch.directPlanReviewerCfg)
	}

	cfg := agent.DefaultDirectPlanReviewerConfig()
	orch.SetDirectPlanReviewerConfig(&cfg)
	if orch.directPlanReviewerCfg == nil {
		t.Fatal("expected config to be stored, got nil")
	}
	if orch.directPlanReviewerCfg.Model != cfg.Model {
		t.Errorf("expected model %q, got %q", cfg.Model, orch.directPlanReviewerCfg.Model)
	}

	// Disable.
	orch.SetDirectPlanReviewerConfig(nil)
	if orch.directPlanReviewerCfg != nil {
		t.Fatalf("expected nil after disable, got %+v", orch.directPlanReviewerCfg)
	}
}

// ---------------------------------------------------------------------------
// Direct path happy case: plan_review + directPlanReviewerCfg set
// ---------------------------------------------------------------------------

func TestSpawnReviewerSession_DirectPath(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)

	featureName := "direct-plan-review-ok"
	worktreePath := createFeatureWorktree(t, bareRepo, featureName)

	reviewJSON := `{
		"coverage": "full",
		"uncovered_criteria": [],
		"file_overlap_risk": "low",
		"overlapping_pairs": [],
		"integration_gap": false,
		"tdd_assessment": {"test_coverage_adequate": true, "exceptions_justified": true, "issues": []},
		"issues": [],
		"recommendation": "approve"
	}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message":       map[string]any{"content": reviewJSON},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     1000,
				"completion_tokens": 200,
				"total_tokens":      1200,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)

	// Plan body — the reviewer only needs a parseable object.
	plan := model.JSONField{"subtasks": []any{}}
	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "direct-plan-review-ok",
		Description:    "A task with an approved plan.",
		Status:         model.StatusPlanReview,
		WorktreeBranch: "feature/" + featureName,
		Plan:           plan,
	}
	if err := orch.db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	session, err := orch.SpawnReviewerSession(taskID)
	if err != nil {
		t.Fatalf("SpawnReviewerSession returned error: %v", err)
	}
	if session != "" {
		t.Errorf("expected empty tmux session for direct path, got %q", session)
	}

	// Verify an agent record was created with the sglang-direct provider.
	var ag model.Agent
	if err := orch.db.Where("current_task_id = ? OR worktree_path = ?", taskID, worktreePath).
		Order("created_at DESC").
		First(&ag).Error; err != nil {
		// CurrentTaskID may have been cleared by onReviewerCompleted; fall back to worktree path alone.
		if err2 := orch.db.Where("worktree_path = ?", worktreePath).Order("created_at DESC").First(&ag).Error; err2 != nil {
			t.Fatalf("expected agent record, got err: %v / %v", err, err2)
		}
	}
	if ag.Provider != "sglang-direct" {
		t.Errorf("expected provider sglang-direct, got %q", ag.Provider)
	}
	if ag.AgentType != model.AgentReviewer {
		t.Errorf("expected AgentReviewer, got %s", ag.AgentType)
	}
	// onReviewerCompleted marks the agent idle.
	if ag.Status != model.AgentIdle {
		t.Errorf("expected AgentIdle post-success, got %s", ag.Status)
	}

	// Verify task.Context["review"] is populated with the parsed review.
	var saved model.Task
	if err := orch.db.First(&saved, "id = ?", taskID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if saved.Context == nil {
		t.Fatal("expected task.Context to be populated, got nil")
	}
	reviewAny, ok := saved.Context["review"]
	if !ok {
		t.Fatalf("expected task.Context[\"review\"], got keys: %v", keysOf(saved.Context))
	}
	reviewMap, ok := reviewAny.(map[string]any)
	if !ok {
		t.Fatalf("expected review to be map, got %T", reviewAny)
	}
	if reviewMap["recommendation"] != "approve" {
		t.Errorf("expected recommendation=approve, got %v", reviewMap["recommendation"])
	}

	// review.json should have been cleaned up by onReviewerCompleted.
	if _, err := os.Stat(filepath.Join(worktreePath, "review.json")); !os.IsNotExist(err) {
		t.Errorf("expected review.json to be removed after processing, got err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Direct path applies only for plan review, not feature review
// ---------------------------------------------------------------------------

func TestSpawnReviewerSession_DirectPathOnlyForPlanReview(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)

	featureName := "direct-plan-review-testing-ready"
	createFeatureWorktree(t, bareRepo, featureName)

	// Configure direct reviewer. Server should not be called.
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "testing-ready-task",
		Description:    "A task past plan review.",
		Status:         model.StatusTestingReady,
		WorktreeBranch: "feature/" + featureName,
	}
	if err := orch.db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Call should fall through to the subprocess path (runner nil) and fail
	// there — but the failure must NOT come from the direct path.
	_, err := orch.SpawnReviewerSession(taskID)
	if err == nil {
		t.Fatal("expected error (runner not wired), got nil")
	}
	if strings.Contains(err.Error(), "direct") {
		t.Errorf("direct path must not activate for testing_ready, got err: %v", err)
	}
	if called {
		t.Error("SGLang endpoint was called for a feature review — direct path must only engage for plan review")
	}
}

// ---------------------------------------------------------------------------
// Direct path propagates API errors
// ---------------------------------------------------------------------------

func TestSpawnReviewerSession_DirectPathPersistsMeasuredFailure(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)

	featureName := "direct-plan-review-api-error"
	createFeatureWorktree(t, bareRepo, featureName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": nil}, "finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 5406, "completion_tokens": 1024, "total_tokens": 6430},
		}
		_ = json.NewEncoder(w).Encode(writeJSON)
	}))
	defer server.Close()

	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)

	taskID := uuid.New()
	task := model.Task{
		ID:             taskID,
		ProjectID:      orch.projectID,
		Title:          "measured-review-failure",
		Description:    "empty visible completion",
		Status:         model.StatusPlanReview,
		WorktreeBranch: "feature/" + featureName,
		Plan:           model.JSONField{"subtasks": []any{}},
	}
	if err := orch.db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err := orch.SpawnReviewerSession(taskID)
	if err == nil {
		t.Fatal("expected API error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty response content") {
		t.Errorf("expected empty-completion error, got: %v", err)
	}
	var usage model.TaskEvent
	if err := orch.db.Where("task_id = ? AND event_type = ?", taskID, inferenceUsageEventType).First(&usage).Error; err != nil {
		t.Fatalf("expected durable failed inference usage: %v", err)
	}
	if usage.Details["outcome"] != "failed" || usage.Details["failure_code"] != "empty_visible_completion" {
		t.Fatalf("unexpected failed usage details: %#v", usage.Details)
	}
	if got := usage.Details["tokens_in"]; got != float64(5406) {
		t.Fatalf("tokens_in = %#v, want 5406", got)
	}
	if got := usage.Details["tokens_out"]; got != float64(1024) {
		t.Fatalf("tokens_out = %#v, want 1024", got)
	}
	var failure model.TaskEvent
	if err := orch.db.Where("task_id = ? AND event_type = ?", taskID, "review_attempt_failed").First(&failure).Error; err != nil {
		t.Fatalf("expected durable review failure diagnostic: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func keysOf(m model.JSONField) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
