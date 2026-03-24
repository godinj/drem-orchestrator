package orchestrator

import (
	"context"
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/constraints"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/supervisor"
)

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
