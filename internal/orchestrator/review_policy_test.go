package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/agent"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

func directReviewServer(t *testing.T, review map[string]any) *httptest.Server {
	t.Helper()
	raw, err := json.Marshal(review)
	require.NoError(t, err)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(raw)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
		})
	}))
}

func TestAutomatedPlanReviewApprovesOnlySafeRecommendation(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-plan-review"
	createFeatureWorktree(t, bareRepo, featureName)
	server := directReviewServer(t, map[string]any{
		"coverage": "full", "file_overlap_risk": "low", "integration_gap": false,
		"tdd_assessment": map[string]any{"test_coverage_adequate": true, "exceptions_justified": true, "issues": []any{}},
		"issues":         []any{}, "recommendation": "approve",
	})
	defer server.Close()
	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)
	require.NoError(t, orch.SetReviewPolicyConfig(ReviewPolicyConfig{
		Plan: model.ReviewGateSGLangSafeAuto, Tests: model.ReviewGateManual,
	}))
	task := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "safe plan", Description: "review it",
		Status: model.StatusPlanReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 1,
	}
	require.NoError(t, orch.db.Create(&task).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&task))
	require.NoError(t, orch.db.First(&task, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, task.Status)

	var agents []model.Agent
	require.NoError(t, orch.db.Where("agent_type = ?", model.AgentReviewer).Find(&agents).Error)
	require.Len(t, agents, 1)
	require.Equal(t, "sglang-direct", agents[0].Provider)
}

func TestAutomatedPlanReviewCreatesWorktreeForAdapterPlan(t *testing.T) {
	orch, _ := setupDirectPlanReviewerTest(t)
	server := directReviewServer(t, map[string]any{
		"coverage": "full", "file_overlap_risk": "low", "integration_gap": false,
		"tdd_assessment": map[string]any{"test_coverage_adequate": true, "exceptions_justified": true, "issues": []any{}},
		"issues":         []any{}, "recommendation": "approve",
	})
	defer server.Close()
	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)
	task := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "adapter plan", Description: "review it",
		Status: model.StatusPlanReview, Plan: makePlan(1), StateVersion: 1,
		Context: model.JSONField{
			"automated_review_state_version": float64(1),
			"automated_review_status":        "reviewer_failed",
			"automated_review_detail":        "spawn reviewer: no integration worktree found for task",
		},
	}
	require.NoError(t, orch.db.Create(&task).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&task))
	require.NoError(t, orch.db.First(&task, "id = ?", task.ID).Error)
	require.NotEmpty(t, task.WorktreeBranch)
	require.NotEmpty(t, orch.resolveIntegrationWorktree(&task))
	require.Equal(t, model.StatusInProgress, task.Status)
}

func TestAutomatedPlanReviewRecoversSafeStructuredAliasWithoutAnotherInferenceCall(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-plan-review-alias"
	createFeatureWorktree(t, bareRepo, featureName)
	task := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "adapter plan alias", Description: "review it",
		Status: model.StatusPlanReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 1,
		Context: model.JSONField{
			"automated_review_state_version": float64(1),
			"automated_review_status":        "attention_required",
			"automated_review_detail":        "SGLang recommendation: approve",
			"review": map[string]any{
				"coverage": "full", "file_overlap_risk": "low", "integration_gap": false,
				"tdd_structure": map[string]any{"test_coverage_adequate": true, "exceptions_justified": false, "issues": []any{}},
				"issues":        []any{}, "recommendation": "approve",
			},
		},
	}
	require.NoError(t, orch.db.Create(&task).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&task))
	require.NoError(t, orch.db.First(&task, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusInProgress, task.Status)
	require.Equal(t, "approved", task.Context["automated_review_status"])
	require.NotContains(t, task.Context, "automated_review_detail")
	review := task.Context["review"].(map[string]any)
	require.Contains(t, review, "tdd_assessment")
	require.NotContains(t, review, "tdd_structure")

	var reviewers int64
	require.NoError(t, orch.db.Model(&model.Agent{}).Where("agent_type = ?", model.AgentReviewer).Count(&reviewers).Error)
	require.Zero(t, reviewers)
}

func TestAutomatedReviewParksAmbiguousRecommendationWithoutRepeating(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-plan-revise"
	createFeatureWorktree(t, bareRepo, featureName)
	server := directReviewServer(t, map[string]any{
		"coverage": "partial", "issues": []any{"missing negative case"}, "recommendation": "revise",
	})
	defer server.Close()
	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)
	task := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "ambiguous plan", Description: "review it",
		Status: model.StatusPlanReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 3,
	}
	require.NoError(t, orch.db.Create(&task).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&task))
	require.NoError(t, orch.db.First(&task, "id = ?", task.ID).Error)
	require.Equal(t, model.StatusPlanReview, task.Status)
	require.Equal(t, "attention_required", task.Context["automated_review_status"])
	require.NoError(t, orch.processAutomatedReviewGate(&task))
	var count int64
	require.NoError(t, orch.db.Model(&model.Agent{}).Where("agent_type = ?", model.AgentReviewer).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestAutomatedReviewClaimCannotOverwriteNewerTaskVersion(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-stale-review-claim"
	createFeatureWorktree(t, bareRepo, featureName)
	stale := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "stale reviewer", Description: "do not overwrite",
		Status: model.StatusPlanReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 1,
	}
	require.NoError(t, orch.db.Create(&stale).Error)
	require.NoError(t, orch.db.Model(&model.Task{}).Where("id = ?", stale.ID).Updates(map[string]any{
		"state_version": uint64(2), "plan_feedback": "newer mutation",
	}).Error)

	err := orch.processAutomatedReviewGate(&stale)
	require.ErrorIs(t, err, state.ErrStaleTransition)

	var current model.Task
	require.NoError(t, orch.db.First(&current, "id = ?", stale.ID).Error)
	require.Equal(t, uint64(2), current.StateVersion)
	require.Equal(t, "newer mutation", current.PlanFeedback)
	require.NotEqual(t, "running", current.Context["automated_review_status"])
}

func TestAutomatedTestReviewFailsClosedForCodexRepairWithoutAnotherInferenceCall(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-test-review-revision"
	createFeatureWorktree(t, bareRepo, featureName)
	parent := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "revise tests", Description: "review tests",
		Status: model.StatusTestReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 3,
		Context: model.JSONField{
			"automated_review_state_version": float64(3),
			"automated_review_status":        "attention_required",
			"automated_review_detail":        "SGLang recommendation: revise",
			"review": map[string]any{
				"coverage": "partial", "issues": []any{"missing branch case"}, "recommendation": "revise",
			},
		},
	}
	require.NoError(t, orch.db.Create(&parent).Error)
	testTask := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, ParentTaskID: &parent.ID, Title: "window title tests",
		Description: "assert branch titles", Status: model.StatusDone, Phase: "test", TestsFor: model.JSONArray{uuid.New().String()},
	}
	require.NoError(t, orch.db.Create(&testTask).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&parent))
	require.NoError(t, orch.db.First(&parent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusFailed, parent.Status)
	require.Equal(t, float64(1), parent.Context["test_rejection_count"])
	require.Equal(t, "test_review_rejected", parent.Context["latest_failure_type"])

	require.NoError(t, orch.db.First(&testTask, "id = ?", testTask.ID).Error)
	require.Equal(t, model.StatusFailed, testTask.Status)
	require.Equal(t, "test_review_rejected", testTask.Context["latest_failure_type"])
	var replacements int64
	require.NoError(t, orch.db.Model(&model.Task{}).Where("parent_task_id = ? AND status = ?", parent.ID, model.StatusBacklog).Count(&replacements).Error)
	require.Zero(t, replacements)

	var reviewers int64
	require.NoError(t, orch.db.Model(&model.Agent{}).Where("agent_type = ?", model.AgentReviewer).Count(&reviewers).Error)
	require.Zero(t, reviewers)
}

func TestAutomatedTestReviewRecoversAffirmingIssuesWithoutAnotherInferenceCall(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-test-review-affirming"
	createFeatureWorktree(t, bareRepo, featureName)
	parent := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "approved tests", Description: "review tests",
		Status: model.StatusTestReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 4,
		Context: model.JSONField{
			"automated_review_state_version": float64(4),
			"automated_review_status":        "attention_required",
			"automated_review_detail":        "SGLang recommendation: approve",
			"review": map[string]any{
				"coverage": "full",
				"issues": []any{
					"The test correctly matches the requested title.",
					"The test covers all required branch scenarios.",
					"The file path is consistent with the manifest.",
				},
				"recommendation": "approve",
			},
		},
	}
	require.NoError(t, orch.db.Create(&parent).Error)
	testTask := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, ParentTaskID: &parent.ID, Title: "regression test",
		Description: "assert the requested behavior", Status: model.StatusDone, Phase: "test",
	}
	require.NoError(t, orch.db.Create(&testTask).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&parent))
	require.NoError(t, orch.db.First(&parent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusInProgress, parent.Status)
	review := parent.Context["review"].(map[string]any)
	require.Empty(t, review["issues"].([]any))

	var reviewers int64
	require.NoError(t, orch.db.Model(&model.Agent{}).Where("agent_type = ?", model.AgentReviewer).Count(&reviewers).Error)
	require.Zero(t, reviewers)
}

func TestAutomatedTestReviewUsesCompletedTestEvidence(t *testing.T) {
	orch, bareRepo := setupDirectPlanReviewerTest(t)
	featureName := "policy-test-review"
	createFeatureWorktree(t, bareRepo, featureName)
	server := directReviewServer(t, map[string]any{
		"coverage": "full", "issues": []any{}, "recommendation": "approve",
	})
	defer server.Close()
	cfg := agent.DefaultDirectPlanReviewerConfig()
	cfg.Endpoint = server.URL
	orch.SetDirectPlanReviewerConfig(&cfg)
	parent := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, Title: "safe tests", Description: "review tests",
		Status: model.StatusTestReview, WorktreeBranch: "feature/" + featureName, Plan: makePlan(1), StateVersion: 2,
	}
	require.NoError(t, orch.db.Create(&parent).Error)
	testTask := model.Task{
		ID: uuid.New(), ProjectID: orch.projectID, ParentTaskID: &parent.ID, Title: "regression test",
		Description: "assert the requested behavior", Status: model.StatusDone, Phase: "test",
	}
	require.NoError(t, orch.db.Create(&testTask).Error)

	require.NoError(t, orch.processAutomatedReviewGate(&parent))
	require.NoError(t, orch.db.First(&parent, "id = ?", parent.ID).Error)
	require.Equal(t, model.StatusInProgress, parent.Status)
}
