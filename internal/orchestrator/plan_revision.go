package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

// RevisePlan atomically replaces an adapter-authored plan while leaving the
// task at plan_review. Incrementing StateVersion and clearing the prior
// automated-review context causes exactly one fresh review on the next tick.
func (o *Orchestrator) RevisePlan(taskID uuid.UUID, observedVersion uint64, plan model.JSONField, actor, reason string) error {
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if taskID == uuid.Nil || observedVersion == 0 || plan == nil || actor == "" || reason == "" {
		return fmt.Errorf("revise plan: task, observed version, plan, actor, and reason are required")
	}

	var revised model.Task
	err := o.db.Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return fmt.Errorf("revise plan: load task: %w", err)
		}
		if task.Status != model.StatusPlanReview || task.StateVersion != observedVersion {
			return fmt.Errorf("%w: task status/version is %s/%d, observed plan_review/%d",
				state.ErrStaleTransition, task.Status, task.StateVersion, observedVersion)
		}
		if task.Context != nil && task.Context["automated_review_status"] == "running" {
			return fmt.Errorf("%w: automated plan review is currently running", state.ErrStaleTransition)
		}
		var children int64
		if err := tx.Model(&model.Task{}).Where("parent_task_id = ?", task.ID).Count(&children).Error; err != nil {
			return fmt.Errorf("revise plan: count subtasks: %w", err)
		}
		if children != 0 {
			return fmt.Errorf("%w: task already has %d materialized subtasks", state.ErrStaleTransition, children)
		}

		oldHash, err := planDigest(task.Plan)
		if err != nil {
			return fmt.Errorf("revise plan: hash existing plan: %w", err)
		}
		newHash, err := planDigest(plan)
		if err != nil {
			return fmt.Errorf("revise plan: hash revised plan: %w", err)
		}
		context := task.Context
		if context == nil {
			context = make(model.JSONField)
		}
		clearAutomatedReviewContext(context)
		now := time.Now().UTC()
		res := tx.Model(&model.Task{}).
			Where("id = ? AND status = ? AND state_version = ?", task.ID, model.StatusPlanReview, observedVersion).
			Updates(map[string]any{
				"plan": plan, "plan_feedback": "", "tdd_exceptions": nil,
				"assigned_agent_id": nil, "context": context,
				"state_version": observedVersion + 1, "updated_at": now,
			})
		if res.Error != nil {
			return fmt.Errorf("revise plan: update task: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: plan_review/%d was already claimed", state.ErrStaleTransition, observedVersion)
		}
		if err := tx.Create(&model.TaskEvent{
			TaskID: task.ID, EventType: "plan_revised",
			OldValue: string(model.StatusPlanReview), NewValue: string(model.StatusPlanReview),
			Actor: actor, CreatedAt: now,
			Details: model.JSONField{
				"reason": reason, "source": "codex_adapter",
				"old_state_version": observedVersion, "new_state_version": observedVersion + 1,
				"old_plan_sha256": oldHash, "new_plan_sha256": newHash,
			},
		}).Error; err != nil {
			return fmt.Errorf("revise plan: create event: %w", err)
		}
		if err := tx.First(&revised, "id = ?", task.ID).Error; err != nil {
			return fmt.Errorf("revise plan: reload task: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	o.emit("task_updated", &revised)
	o.logger.Info("plan revised", "task_id", revised.ID, "actor", actor, "state_version", revised.StateVersion)
	return nil
}

func planDigest(plan model.JSONField) (string, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
