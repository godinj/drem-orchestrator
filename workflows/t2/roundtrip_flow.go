// Package t2 defines the second-generation roundtrip workflow contract.
package t2

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ErrNotImplemented marks the current TDD stub. The real workflow should
// remove this sentinel by implementing Run.
var ErrNotImplemented = errors.New("t2 roundtrip flow is not implemented")

type Stage string

const (
	StageDirectClassifier Stage = "direct_classifier"
	StageBacklog          Stage = "backlog"
	StageWarmPlanner      Stage = "warm_planner"
	StagePlanReview       Stage = "plan_review"
	StageExecution        Stage = "execution"
)

type HaltReason string

const (
	HaltReasonPlanReviewFrozenGate HaltReason = "plan_review_frozen_gate"
)

type GateMode string

const (
	GateModeFrozen GateMode = "frozen"
	GateModeOpen   GateMode = "open"
)

type Config struct {
	PlanReviewGate GateMode
	Classifier     Classifier
	Planner        Planner
	Executor       Executor
}

type Input struct {
	ProjectID   uuid.UUID
	Title       string
	Description string
}

type Classification struct {
	Category        model.TaskCategory
	ComplexityScore int
	TargetFiles     []string
	Rationale       string
}

type PlannerRequest struct {
	TaskID         uuid.UUID
	ProjectID      uuid.UUID
	Title          string
	Description    string
	Classification Classification
	Provider       model.ProviderType
}

type Plan struct {
	Subtasks []PlanSubtask
	Metadata PlanMetadata
}

type PlanSubtask struct {
	Title          string
	Description    string
	AgentType      model.AgentType
	EstimatedFiles []string
	Phase          string
	TestsFor       []int
	Dependencies   []int
}

type PlanMetadata struct {
	Provider       model.ProviderType
	Model          string
	Effort         string
	TokensIn       int
	TokensOut      int
	DurationMillis int
	SourceTaskID   uuid.UUID
	Classifier     Classification
}

type Result struct {
	TaskID             uuid.UUID
	Status             model.TaskStatus
	HaltReason         HaltReason
	GateFrozen         bool
	ReviewablePlan     Plan
	VisitedStages      []Stage
	DownstreamExecuted bool
}

type Classifier interface {
	Classify(context.Context, Input) (Classification, error)
}

type Planner interface {
	Plan(context.Context, PlannerRequest) (Plan, error)
}

type Executor interface {
	Execute(context.Context, uuid.UUID, Plan) error
}

type ClassifierFunc func(context.Context, Input) (Classification, error)

func (f ClassifierFunc) Classify(ctx context.Context, in Input) (Classification, error) {
	return f(ctx, in)
}

type PlannerFunc func(context.Context, PlannerRequest) (Plan, error)

func (f PlannerFunc) Plan(ctx context.Context, req PlannerRequest) (Plan, error) {
	return f(ctx, req)
}

type ExecutorFunc func(context.Context, uuid.UUID, Plan) error

func (f ExecutorFunc) Execute(ctx context.Context, taskID uuid.UUID, plan Plan) error {
	return f(ctx, taskID, plan)
}

type RoundtripFlow struct {
	db  *gorm.DB
	cfg Config
}

func NewRoundtripFlow(db *gorm.DB, cfg Config) *RoundtripFlow {
	return &RoundtripFlow{db: db, cfg: cfg}
}

func (f *RoundtripFlow) Run(ctx context.Context, in Input) (Result, error) {
	var result Result
	if f.db == nil {
		return result, errors.New("roundtrip flow requires a database")
	}
	if f.cfg.Classifier == nil {
		return result, errors.New("roundtrip flow requires a classifier")
	}
	if f.cfg.Planner == nil {
		return result, errors.New("roundtrip flow requires a planner")
	}

	classification, err := f.cfg.Classifier.Classify(ctx, in)
	if err != nil {
		return result, err
	}
	result.VisitedStages = append(result.VisitedStages, StageDirectClassifier)

	task := model.Task{
		ID:              uuid.New(),
		ProjectID:       in.ProjectID,
		Title:           in.Title,
		Description:     in.Description,
		Status:          model.StatusBacklog,
		Category:        classification.Category,
		ComplexityScore: classification.ComplexityScore,
		Context: model.JSONField{
			"classifier": model.JSONField{
				"target_files": classification.TargetFiles,
				"rationale":    classification.Rationale,
			},
		},
	}
	if task.Category == "" {
		task.Category = model.CategoryStandard
	}
	if err := f.db.WithContext(ctx).Create(&task).Error; err != nil {
		return result, fmt.Errorf("create backlog task: %w", err)
	}
	result.TaskID = task.ID
	result.Status = task.Status
	result.VisitedStages = append(result.VisitedStages, StageBacklog)

	plannerReq := PlannerRequest{
		TaskID:         task.ID,
		ProjectID:      in.ProjectID,
		Title:          in.Title,
		Description:    in.Description,
		Classification: classification,
		Provider:       model.ProviderClaude,
	}
	plan, err := f.cfg.Planner.Plan(ctx, plannerReq)
	if err != nil {
		return result, err
	}
	result.VisitedStages = append(result.VisitedStages, StageWarmPlanner)
	result.ReviewablePlan = plan

	if err := validateReviewablePlan(plan); err != nil {
		return result, err
	}

	plan.Metadata.SourceTaskID = task.ID
	if plan.Metadata.Provider == "" {
		plan.Metadata.Provider = model.ProviderClaude
	}
	if plan.Metadata.Classifier.Category == "" && plan.Metadata.Classifier.ComplexityScore == 0 {
		plan.Metadata.Classifier = classification
	}

	task.Plan = planJSON(plan)
	task.Status = model.StatusPlanReview
	if task.Context == nil {
		task.Context = model.JSONField{}
	}
	task.Context["plan_review_gate"] = string(f.planReviewGate())
	if f.planReviewGate() == GateModeFrozen {
		task.Context["halt_reason"] = string(HaltReasonPlanReviewFrozenGate)
	}
	if err := f.db.WithContext(ctx).Save(&task).Error; err != nil {
		return result, fmt.Errorf("persist reviewable plan: %w", err)
	}
	result.Status = task.Status
	result.VisitedStages = append(result.VisitedStages, StagePlanReview)

	if f.planReviewGate() == GateModeFrozen {
		result.HaltReason = HaltReasonPlanReviewFrozenGate
		result.GateFrozen = true
		return result, nil
	}

	if f.cfg.Executor == nil {
		return result, errors.New("roundtrip flow requires an executor when plan_review gate is open")
	}
	if err := f.cfg.Executor.Execute(ctx, task.ID, plan); err != nil {
		return result, err
	}
	result.DownstreamExecuted = true
	result.VisitedStages = append(result.VisitedStages, StageExecution)
	return result, nil
}

func (f *RoundtripFlow) Teardown(ctx context.Context, result Result) error {
	if f.db == nil || result.TaskID == uuid.Nil {
		return nil
	}
	return f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("current_task_id = ?", result.TaskID).Delete(&model.Agent{}).Error; err != nil {
			return fmt.Errorf("delete smoke agents: %w", err)
		}
		if err := tx.Where("task_id = ?", result.TaskID).Delete(&model.TaskComment{}).Error; err != nil {
			return fmt.Errorf("delete smoke comments: %w", err)
		}
		if err := tx.Where("task_id = ?", result.TaskID).Delete(&model.TaskEvent{}).Error; err != nil {
			return fmt.Errorf("delete smoke events: %w", err)
		}
		if err := tx.Where("parent_task_id = ? OR id = ?", result.TaskID, result.TaskID).Delete(&model.Task{}).Error; err != nil {
			return fmt.Errorf("delete smoke tasks: %w", err)
		}
		return nil
	})
}

func (f *RoundtripFlow) planReviewGate() GateMode {
	if f.cfg.PlanReviewGate == "" {
		return GateModeFrozen
	}
	return f.cfg.PlanReviewGate
}

func validateReviewablePlan(plan Plan) error {
	if len(plan.Subtasks) == 0 {
		return errors.New("reviewable plan requires at least one subtask")
	}
	for i, subtask := range plan.Subtasks {
		if subtask.Title == "" || subtask.Description == "" {
			return fmt.Errorf("reviewable plan subtask %d requires title and description", i)
		}
	}
	return nil
}

func planJSON(plan Plan) model.JSONField {
	subtasks := make([]any, 0, len(plan.Subtasks))
	for _, subtask := range plan.Subtasks {
		subtaskMap := model.JSONField{
			"title":           subtask.Title,
			"description":     subtask.Description,
			"agent_type":      string(subtask.AgentType),
			"estimated_files": subtask.EstimatedFiles,
			"phase":           subtask.Phase,
		}
		if len(subtask.TestsFor) > 0 {
			subtaskMap["tests_for"] = subtask.TestsFor
		}
		if len(subtask.Dependencies) > 0 {
			subtaskMap["dependencies"] = subtask.Dependencies
		}
		subtasks = append(subtasks, subtaskMap)
	}

	return model.JSONField{
		"subtasks": subtasks,
		"metadata": model.JSONField{
			"provider":        string(plan.Metadata.Provider),
			"model":           plan.Metadata.Model,
			"effort":          plan.Metadata.Effort,
			"tokens_in":       plan.Metadata.TokensIn,
			"tokens_out":      plan.Metadata.TokensOut,
			"duration_millis": plan.Metadata.DurationMillis,
			"source_task_id":  plan.Metadata.SourceTaskID.String(),
			"classifier": model.JSONField{
				"category":         string(plan.Metadata.Classifier.Category),
				"complexity_score": plan.Metadata.Classifier.ComplexityScore,
				"target_files":     plan.Metadata.Classifier.TargetFiles,
				"rationale":        plan.Metadata.Classifier.Rationale,
			},
		},
	}
}
