package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
	"github.com/godinj/drem-orchestrator/internal/worktree"
)

// depthScoreThreshold is the minimum depth score (0.0–1.0) a plan must achieve
// to proceed to human review without supervisor diagnosis. Plans scoring below
// this threshold trigger a supervisor plan depth review. Initial value: 0.5.
const depthScoreThreshold = 0.5

// checkPlanDepthGate evaluates the plan's depth score and, if below threshold,
// requests a supervisor diagnosis. The diagnosis is stored as a system comment
// and in the task context. This is advisory only — the plan always proceeds to
// human review regardless of the supervisor outcome.
func (o *Orchestrator) checkPlanDepthGate(task *model.Task, scores map[string]any) {
	depthScore, ok := scores["depth"].(float64)
	if !ok {
		return // no depth score available
	}

	if depthScore >= depthScoreThreshold {
		return // depth is acceptable
	}

	if o.supervisor == nil {
		o.logger.Warn("plan depth score below threshold but no supervisor configured",
			"task_id", task.ID, "depth_score", depthScore)
		return
	}

	// Serialize plan for the supervisor prompt.
	planJSON := ""
	if task.Plan != nil {
		if data, err := json.MarshalIndent(task.Plan, "", "  "); err == nil {
			planJSON = string(data)
		}
	}

	var review supervisor.PlanDepthReview
	prompt := supervisor.PlanDepthReviewPrompt(task.Title, task.Description, planJSON, depthScore)

	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &review); err != nil {
		o.logger.Warn("supervisor plan depth review failed, continuing",
			"task_id", task.ID, "error", err)
		return
	}

	// Store the review in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["depth_review"] = map[string]any{
		"assessment":       review.Assessment,
		"shallow_areas":    review.ShallowAreas,
		"recommendations":  review.Recommendations,
		"rejection_reason": review.RejectionReason,
	}

	// Add supervisor diagnosis as a system comment on the task.
	if review.RejectionReason != "" {
		comment := model.TaskComment{
			TaskID: task.ID,
			Author: "system",
			Body:   fmt.Sprintf("[Depth Review] Score: %.0f%% — %s", depthScore*100, review.RejectionReason),
		}
		if err := o.db.Create(&comment).Error; err != nil {
			o.logger.Warn("failed to create depth review comment", "task_id", task.ID, "error", err)
		}
	}

	o.logger.Info("plan depth review completed",
		"task_id", task.ID, "depth_score", depthScore, "assessment", review.Assessment)
}

// checkDepthConstraintFailures inspects constraint evaluation results for
// depth-specific failures and requests a supervisor diagnosis if any are found.
// The diagnosis is stored as a system comment and in the task context. This is
// advisory only — normal constraint failure handling continues regardless.
func (o *Orchestrator) checkDepthConstraintFailures(task *model.Task, report *constraints.Report, featureDir string) {
	if report == nil || o.supervisor == nil {
		return
	}

	// Check for depth-type constraint failures.
	hasDepthFailure := false
	for _, r := range report.Results {
		if !r.Passed && r.Type == "depth" {
			hasDepthFailure = true
			break
		}
	}

	if !hasDepthFailure {
		return
	}

	constraintReport := constraints.FormatReport(report)

	// Get the diff for context.
	diff := ""
	if o.worktree != nil {
		diffOutput, err := getChangedFilesDiff(featureDir, o.worktree.DefaultBranch)
		if err == nil {
			diff = diffOutput
		}
	}

	var diagnosis supervisor.DepthConstraintDiagnosis
	prompt := supervisor.DepthConstraintDiagnosisPrompt(task.Title, constraintReport, diff)

	if err := o.supervisor.EvaluateJSON(context.Background(), prompt, &diagnosis); err != nil {
		o.logger.Warn("supervisor depth constraint diagnosis failed, continuing",
			"task_id", task.ID, "error", err)
		return
	}

	// Store the diagnosis in task context.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["depth_diagnosis"] = map[string]any{
		"violations":       diagnosis.Violations,
		"root_cause":       diagnosis.RootCause,
		"recommendation":   diagnosis.Recommendation,
		"rejection_reason": diagnosis.RejectionReason,
	}

	// Add supervisor diagnosis as a system comment on the task.
	if diagnosis.RejectionReason != "" {
		comment := model.TaskComment{
			TaskID: task.ID,
			Author: "system",
			Body:   fmt.Sprintf("[Depth Constraint Failure] %s", diagnosis.RejectionReason),
		}
		if err := o.db.Create(&comment).Error; err != nil {
			o.logger.Warn("failed to create depth diagnosis comment", "task_id", task.ID, "error", err)
		}
	}

	o.logger.Info("depth constraint diagnosis completed",
		"task_id", task.ID, "rejection_reason", diagnosis.RejectionReason)
}

// getChangedFilesDiff returns the diff of changed files between the worktree
// HEAD and the given base branch. Returns empty string on error.
func getChangedFilesDiff(worktreeDir, baseBranch string) (string, error) {
	output, err := worktree.RunGit([]string{"diff", baseBranch + "...HEAD"}, worktreeDir)
	if err != nil {
		return "", fmt.Errorf("get changed files diff: %w", err)
	}
	// Truncate to avoid overly large diffs in prompts.
	if len(output) > maxGitDiffLen {
		output = output[:maxGitDiffLen] + "\n... (truncated)"
	}
	return output, nil
}
