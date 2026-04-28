package t2

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

type roundtripRig struct {
	db      *gorm.DB
	project model.Project
}

func roundtripRigFor(t *testing.T) roundtripRig {
	t.Helper()
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "t2-roundtrip", "/tmp/t2-roundtrip.git", "main")
	return roundtripRig{db: db, project: project}
}

// TestRoundtripFlow_HaltsAtFrozenPlanReviewGate specifies the full T2 happy
// path up to the human gate: direct-classifier classifies a request into a
// backlog entry, the warm Claude planner turns it into a reviewable TDD plan,
// and the frozen plan_review gate stops the flow before any execution agent is
// allowed to run.
func TestRoundtripFlow_HaltsAtFrozenPlanReviewGate(t *testing.T) {
	rig := roundtripRigFor(t)
	classification := Classification{
		Category:        model.CategoryStandard,
		ComplexityScore: 6,
		TargetFiles: []string{
			"workflows/t2/roundtrip_flow_integration_test.go",
			"workflows/t2/roundtrip_flow.go",
		},
		Rationale: "T2 roundtrip touches the classifier, planner, and gate boundary.",
	}
	var plannerReq PlannerRequest
	executions := 0

	flow := NewRoundtripFlow(rig.db, Config{
		PlanReviewGate: GateModeFrozen,
		Classifier: ClassifierFunc(func(ctx context.Context, in Input) (Classification, error) {
			assert.Equal(t, rig.project.ID, in.ProjectID)
			assert.Contains(t, in.Title, "roundtrip")
			return classification, nil
		}),
		Planner: PlannerFunc(func(ctx context.Context, req PlannerRequest) (Plan, error) {
			plannerReq = req
			assert.Equal(t, model.ProviderClaude, req.Provider)
			assert.Equal(t, classification, req.Classification)
			return Plan{
				Subtasks: []PlanSubtask{
					{
						Title:          "Write frozen-gate roundtrip test",
						Description:    "Assert the T2 flow stops at plan_review before execution.",
						AgentType:      model.AgentCoder,
						EstimatedFiles: []string{"workflows/t2/roundtrip_flow_integration_test.go"},
						Phase:          "test",
						TestsFor:       []int{1},
					},
					{
						Title:          "Implement frozen-gate roundtrip flow",
						Description:    "Persist classifier metadata, planner output, and gate halt state.",
						AgentType:      model.AgentCoder,
						EstimatedFiles: []string{"workflows/t2/roundtrip_flow.go"},
						Phase:          "implementation",
						Dependencies:   []int{0},
					},
				},
				Metadata: PlanMetadata{
					Provider:       model.ProviderClaude,
					Model:          "claude-opus-4-6",
					Effort:         "high",
					TokensIn:       2400,
					TokensOut:      810,
					DurationMillis: 9100,
					Classifier:     classification,
				},
			}, nil
		}),
		Executor: ExecutorFunc(func(ctx context.Context, taskID uuid.UUID, plan Plan) error {
			executions++
			return nil
		}),
	})

	result, err := flow.Run(context.Background(), Input{
		ProjectID:   rig.project.ID,
		Title:       "Write test: roundtrip flow terminates at plan_review frozen gate",
		Description: "Exercise direct-classifier -> backlog -> warm-planner (Claude) -> plan_review.",
	})
	require.NoError(t, err)

	assert.Equal(t, model.StatusPlanReview, result.Status)
	assert.Equal(t, HaltReasonPlanReviewFrozenGate, result.HaltReason)
	assert.True(t, result.GateFrozen)
	assert.False(t, result.DownstreamExecuted)
	assert.Equal(t, 0, executions, "frozen plan_review must not invoke downstream execution")
	assert.Equal(t, []Stage{
		StageDirectClassifier,
		StageBacklog,
		StageWarmPlanner,
		StagePlanReview,
	}, result.VisitedStages)

	require.NotEqual(t, uuid.Nil, result.TaskID)
	assert.Equal(t, result.TaskID, plannerReq.TaskID)
	assert.Equal(t, rig.project.ID, plannerReq.ProjectID)

	var task model.Task
	require.NoError(t, rig.db.First(&task, "id = ?", result.TaskID).Error)
	assert.Equal(t, model.StatusPlanReview, task.Status)
	assert.Equal(t, model.CategoryStandard, task.Category)
	assert.Equal(t, 6, task.ComplexityScore)
	require.NotNil(t, task.Plan, "plan_review must persist a reviewable plan")

	subtasks, ok := task.Plan["subtasks"].([]any)
	require.True(t, ok, "persisted plan must contain reviewable subtasks")
	require.Len(t, subtasks, 2)
	metadata, ok := task.Plan["metadata"].(map[string]any)
	require.True(t, ok, "persisted plan must include metadata")
	assert.Equal(t, string(model.ProviderClaude), metadata["provider"])
	assert.Equal(t, "claude-opus-4-6", metadata["model"])
	assert.Equal(t, "high", metadata["effort"])
	assert.Equal(t, float64(2400), metadata["tokens_in"])
	assert.Equal(t, float64(810), metadata["tokens_out"])
	assert.Equal(t, result.TaskID.String(), metadata["source_task_id"])

	var executionAgents int64
	require.NoError(t, rig.db.Model(&model.Agent{}).
		Where("current_task_id = ? AND agent_type IN ?", result.TaskID, []model.AgentType{model.AgentCoder, model.AgentReviewer, model.AgentFixer}).
		Count(&executionAgents).Error)
	assert.Equal(t, int64(0), executionAgents, "no coder/reviewer/fixer agents should be scheduled while plan_review is frozen")
}

// TestRoundtripFlow_FrozenGateRequiresReviewablePlan covers the edge case that
// reaching plan_review without a complete plan is invalid. The flow should
// fail before freezing the gate because there is nothing meaningful for a human
// to review.
func TestRoundtripFlow_FrozenGateRequiresReviewablePlan(t *testing.T) {
	rig := roundtripRigFor(t)
	flow := NewRoundtripFlow(rig.db, Config{
		PlanReviewGate: GateModeFrozen,
		Classifier: ClassifierFunc(func(ctx context.Context, in Input) (Classification, error) {
			return Classification{Category: model.CategoryStandard, ComplexityScore: 3}, nil
		}),
		Planner: PlannerFunc(func(ctx context.Context, req PlannerRequest) (Plan, error) {
			return Plan{
				Metadata: PlanMetadata{
					Provider:     model.ProviderClaude,
					Model:        "claude-opus-4-6",
					Effort:       "high",
					SourceTaskID: req.TaskID,
				},
			}, nil
		}),
	})

	result, err := flow.Run(context.Background(), Input{
		ProjectID:   rig.project.ID,
		Title:       "missing subtasks",
		Description: "planner returned metadata but no reviewable work items",
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "reviewable plan")
	assert.NotEqual(t, model.StatusPlanReview, result.Status)
	assert.False(t, result.GateFrozen)
}

// TestRoundtripFlow_ClassifierFailureStopsBeforeBacklog verifies that a
// classifier error does not create a misleading backlog/planning record and
// does not call the warm planner.
func TestRoundtripFlow_ClassifierFailureStopsBeforeBacklog(t *testing.T) {
	rig := roundtripRigFor(t)
	plannerCalls := 0
	flow := NewRoundtripFlow(rig.db, Config{
		PlanReviewGate: GateModeFrozen,
		Classifier: ClassifierFunc(func(ctx context.Context, in Input) (Classification, error) {
			return Classification{}, errors.New("classifier unavailable")
		}),
		Planner: PlannerFunc(func(ctx context.Context, req PlannerRequest) (Plan, error) {
			plannerCalls++
			return Plan{}, nil
		}),
	})

	_, err := flow.Run(context.Background(), Input{
		ProjectID:   rig.project.ID,
		Title:       "classifier fails",
		Description: "the flow cannot enter backlog without classifier output",
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "classifier unavailable")
	assert.Equal(t, 0, plannerCalls)

	var tasks int64
	require.NoError(t, rig.db.Model(&model.Task{}).Where("project_id = ?", rig.project.ID).Count(&tasks).Error)
	assert.Equal(t, int64(0), tasks, "classifier failure must not enqueue a backlog task")
}
