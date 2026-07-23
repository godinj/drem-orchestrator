package orchestrator

import (
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

const automatedReviewActor = "policy:sglang-safe-auto"

type ReviewPolicyConfig struct {
	Plan  model.ReviewGatePolicy
	Tests model.ReviewGatePolicy
}

func (o *Orchestrator) SetReviewPolicyConfig(cfg ReviewPolicyConfig) error {
	if _, err := model.ParseReviewGatePolicy(string(cfg.Plan)); err != nil {
		return fmt.Errorf("plan review policy: %w", err)
	}
	if _, err := model.ParseReviewGatePolicy(string(cfg.Tests)); err != nil {
		return fmt.Errorf("test review policy: %w", err)
	}
	o.reviewPolicy = cfg
	return nil
}

func (o *Orchestrator) processReviewGates() {
	for _, gate := range []struct {
		status model.TaskStatus
		policy model.ReviewGatePolicy
	}{
		{status: model.StatusPlanReview, policy: o.reviewPolicy.Plan},
		{status: model.StatusTestReview, policy: o.reviewPolicy.Tests},
	} {
		if gate.policy != model.ReviewGateSGLangSafeAuto {
			continue
		}
		var tasks []model.Task
		if err := o.db.Where("project_id = ? AND status = ? AND parent_task_id IS NULL", o.projectID, gate.status).Find(&tasks).Error; err != nil {
			o.logger.Error("query automated review gates", "status", gate.status, "error", err)
			continue
		}
		for i := range tasks {
			if err := o.processAutomatedReviewGate(&tasks[i]); err != nil {
				o.logger.Error("process automated review gate", "task_id", tasks[i].ID, "status", gate.status, "error", err)
			}
		}
	}
}

func (o *Orchestrator) processAutomatedReviewGate(task *model.Task) error {
	// Planner-backed tasks create their feature worktree while leaving
	// planning. Adapter-authored plans legitimately enter plan_review
	// directly, so establish the same invariant here before deduplicating a
	// review attempt. If an older process already parked this exact task for a
	// missing worktree, clear only that failed attempt after creation so the
	// safe-auto reviewer can retry on the same state version.
	if task.Status == model.StatusPlanReview && task.WorktreeBranch == "" {
		if err := o.ensureFeatureWorktree(task, "automated plan review"); err != nil {
			return fmt.Errorf("prepare automated plan review: %w", err)
		}
		if task.Context != nil && task.Context["automated_review_status"] == "reviewer_failed" {
			detail, _ := task.Context["automated_review_detail"].(string)
			if strings.Contains(detail, "no integration worktree") {
				delete(task.Context, "automated_review_state_version")
				delete(task.Context, "automated_review_status")
				delete(task.Context, "automated_review_detail")
				if err := o.db.Save(task).Error; err != nil {
					return fmt.Errorf("reset missing-worktree review attempt: %w", err)
				}
			}
		}
	}
	// Normalize the one bounded schema alias observed from structured SGLang
	// output. If an older process parked an otherwise-safe review because of
	// that alias (or because a no-exception plan reported false for the
	// irrelevant exception field), finish the same gate without another model
	// call.
	if task.Context != nil {
		if review, ok := task.Context["review"].(map[string]any); ok {
			changed := normalizePlanReview(review)
			changed = normalizeAffirmingTestReview(task, review) || changed
			if attemptedVersion, ok := numericUint64(task.Context["automated_review_state_version"]); ok && attemptedVersion == task.StateVersion && task.Context["automated_review_status"] == "attention_required" {
				if task.Status == model.StatusTestReview && automatedReviewRequestsRevision(review) {
					return o.HandleTestReviewRejectedBy(task.ID, automatedReviewFeedback(review), automatedReviewActor)
				}
				if reviewSafeToApprove(task, review) {
					task.Context["automated_review_status"] = "approved"
					delete(task.Context, "automated_review_detail")
					if err := o.db.Save(task).Error; err != nil {
						return fmt.Errorf("record recovered automated review approval: %w", err)
					}
					if task.Status == model.StatusTestReview {
						return o.HandleTestReviewApprovedBy(task.ID, automatedReviewActor)
					}
					return o.HandlePlanApprovedBy(task.ID, automatedReviewActor)
				}
			}
			if changed {
				if err := o.db.Save(task).Error; err != nil {
					return fmt.Errorf("normalize automated review: %w", err)
				}
			}
		}
	}
	if task.Context != nil {
		if attemptedVersion, ok := numericUint64(task.Context["automated_review_state_version"]); ok && attemptedVersion == task.StateVersion {
			return nil
		}
	}
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["automated_review_state_version"] = float64(task.StateVersion)
	task.Context["automated_review_status"] = "running"
	claim := o.db.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, task.Status, task.StateVersion).
		Update("context", task.Context)
	if claim.Error != nil {
		return fmt.Errorf("claim automated review: %w", claim.Error)
	}
	if claim.RowsAffected != 1 {
		return fmt.Errorf("%w: automated review claim lost for %s/%d", state.ErrStaleTransition, task.Status, task.StateVersion)
	}
	if err := o.db.First(task, "id = ?", task.ID).Error; err != nil {
		return fmt.Errorf("claim automated review: %w", err)
	}
	if o.directPlanReviewerCfg == nil {
		return o.parkAutomatedReview(task, "reviewer_unconfigured", "direct SGLang reviewer is not configured")
	}
	if _, err := o.SpawnReviewerSession(task.ID); err != nil {
		return o.parkAutomatedReview(task, "reviewer_failed", err.Error())
	}
	if err := o.db.First(task, "id = ?", task.ID).Error; err != nil {
		return fmt.Errorf("reload automated review: %w", err)
	}
	review, _ := task.Context["review"].(map[string]any)
	normalizePlanReview(review)
	normalizeAffirmingTestReview(task, review)
	if !reviewSafeToApprove(task, review) {
		if task.Status == model.StatusTestReview && automatedReviewRequestsRevision(review) {
			return o.HandleTestReviewRejectedBy(task.ID, automatedReviewFeedback(review), automatedReviewActor)
		}
		recommendation, _ := review["recommendation"].(string)
		if strings.TrimSpace(recommendation) == "" {
			recommendation = "invalid"
		}
		return o.parkAutomatedReview(task, "attention_required", "SGLang recommendation: "+recommendation)
	}
	task.Context["automated_review_status"] = "approved"
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("record automated review approval: %w", err)
	}
	if task.Status == model.StatusPlanReview {
		return o.HandlePlanApprovedBy(task.ID, automatedReviewActor)
	}
	return o.HandleTestReviewApprovedBy(task.ID, automatedReviewActor)
}

func (o *Orchestrator) parkAutomatedReview(task *model.Task, reason, detail string) error {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["automated_review_status"] = reason
	task.Context["automated_review_detail"] = truncate(detail, 1000)
	if err := o.db.Save(task).Error; err != nil {
		return err
	}
	o.emit("review_attention_required", map[string]any{
		"task_id": task.ID, "status": task.Status, "reason": reason, "detail": truncate(detail, 240),
	})
	return nil
}

func reviewSafeToApprove(task *model.Task, review map[string]any) bool {
	if review == nil || review["recommendation"] != "approve" || review["coverage"] != "full" {
		return false
	}
	if issues, ok := review["issues"].([]any); ok && len(issues) > 0 {
		return false
	}
	if task.Status == model.StatusTestReview {
		return true
	}
	if gap, ok := review["integration_gap"].(bool); !ok || gap {
		return false
	}
	if risk, _ := review["file_overlap_risk"].(string); risk == "" || risk == "high" {
		return false
	}
	tdd, _ := review["tdd_assessment"].(map[string]any)
	adequate, _ := tdd["test_coverage_adequate"].(bool)
	exceptions, _ := tdd["exceptions_justified"].(bool)
	if issues, ok := tdd["issues"].([]any); ok && len(issues) > 0 {
		return false
	}
	return adequate && (!planHasTDDExceptions(task.Plan) || exceptions)
}

func normalizePlanReview(review map[string]any) bool {
	if review == nil || review["tdd_assessment"] != nil {
		return false
	}
	legacy, ok := review["tdd_structure"].(map[string]any)
	if !ok {
		return false
	}
	review["tdd_assessment"] = legacy
	delete(review, "tdd_structure")
	return true
}

func planHasTDDExceptions(plan model.JSONField) bool {
	if plan == nil {
		return false
	}
	exceptions, ok := plan["tdd_exceptions"].([]any)
	return ok && len(exceptions) > 0
}

func automatedReviewRequestsRevision(review map[string]any) bool {
	recommendation, _ := review["recommendation"].(string)
	return recommendation == "revise" || recommendation == "reject"
}

func automatedReviewFeedback(review map[string]any) string {
	recommendation, _ := review["recommendation"].(string)
	parts := []string{"SGLang test review recommendation: " + recommendation + "."}
	for _, key := range []string{"issues", "uncovered_criteria"} {
		values, _ := review[key].([]any)
		for _, value := range values {
			if issue, ok := value.(string); ok && strings.TrimSpace(issue) != "" {
				parts = append(parts, "- "+strings.TrimSpace(issue))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func normalizeAffirmingTestReview(task *model.Task, review map[string]any) bool {
	if task == nil || task.Status != model.StatusTestReview || review == nil || review["recommendation"] != "approve" || review["coverage"] != "full" {
		return false
	}
	issues, ok := review["issues"].([]any)
	if !ok || len(issues) == 0 {
		return false
	}
	positiveMarkers := []string{"correct", "cover", "consistent", "matches", "match the requirements"}
	negativeMarkers := []string{"ambiguous", "fail", "incorrect", "incomplete", "mismatch", "missing", "not match", "outside", "uncovered", "wrong"}
	for _, value := range issues {
		issue, ok := value.(string)
		if !ok {
			return false
		}
		lower := strings.ToLower(strings.TrimSpace(issue))
		if lower == "" {
			return false
		}
		for _, marker := range negativeMarkers {
			if strings.Contains(lower, marker) {
				return false
			}
		}
		positive := false
		for _, marker := range positiveMarkers {
			if strings.Contains(lower, marker) {
				positive = true
				break
			}
		}
		if !positive {
			return false
		}
	}
	review["issues"] = []any{}
	return true
}

func numericUint64(value any) (uint64, bool) {
	switch v := value.(type) {
	case float64:
		return uint64(v), v >= 0 && float64(uint64(v)) == v
	case uint64:
		return v, true
	case int:
		return uint64(v), v >= 0
	default:
		return 0, false
	}
}
