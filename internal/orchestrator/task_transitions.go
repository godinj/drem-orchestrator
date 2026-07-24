package orchestrator

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

func casTaskTransition(tx *gorm.DB, task *model.Task, expected, target model.TaskStatus, actor, source, reason string, references map[string]any) error {
	originalStatus := task.Status
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	transitionPersisted := false
	defer func() {
		if !transitionPersisted {
			task.Status = originalStatus
			task.StateVersion = originalVersion
			task.UpdatedAt = originalUpdatedAt
		}
	}()

	event, err := state.GuardedTransitionTask(task, state.TransitionRequest{
		Target:         target,
		Actor:          actor,
		ExpectedStatus: expected,
		Evidence: state.Evidence{
			TaskID:     task.ID,
			Actor:      actor,
			Source:     source,
			Reason:     reason,
			References: references,
		},
	})
	if err != nil {
		return err
	}
	oldVersion := originalVersion
	task.StateVersion = oldVersion + 1
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, expected, oldVersion).
		Updates(taskTransitionColumns(task))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: transition %s/%d was already claimed", state.ErrStaleTransition, expected, oldVersion)
	}
	if err := tx.Create(event).Error; err != nil {
		return fmt.Errorf("persist transition event: %w", err)
	}
	transitionPersisted = true
	return nil
}

// transitionTaskAtomic is the persistence boundary for ordinary task-state
// changes. It gives legacy lifecycle paths the same compare-and-swap and
// task/event transaction semantics as delivery transitions without requiring
// them to manufacture delivery artifacts.
func (o *Orchestrator) transitionTaskAtomic(task *model.Task, target model.TaskStatus, actor, source, reason string, references map[string]any) error {
	if task == nil {
		return errors.New("transition task: task is nil")
	}
	expected := task.Status
	originalStatus := task.Status
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	err := o.db.Transaction(func(tx *gorm.DB) error {
		return casTaskTransition(tx, task, expected, target, actor, source, reason, references)
	})
	if err != nil {
		task.Status = originalStatus
		task.StateVersion = originalVersion
		task.UpdatedAt = originalUpdatedAt
	}
	return err
}

// GuardedTaskTransitionTx exposes the single task-state persistence boundary
// to the in-process HTTP recovery surface. The caller owns the surrounding
// transaction so companion recovery/audit rows commit atomically with the
// task row and status-change event.
func GuardedTaskTransitionTx(tx *gorm.DB, task *model.Task, target model.TaskStatus, actor, source, reason string, references map[string]any) error {
	if tx == nil || task == nil {
		return errors.New("guarded task transition: transaction and task are required")
	}
	return casTaskTransition(tx, task, task.Status, target, actor, source, reason, references)
}

func taskTransitionColumns(task *model.Task) map[string]any {
	return map[string]any{
		"parent_task_id": task.ParentTaskID, "title": task.Title, "description": task.Description,
		"status": task.Status, "state_version": task.StateVersion, "category": task.Category,
		"priority": task.Priority, "complexity_score": task.ComplexityScore, "labels": task.Labels,
		"dependency_ids": task.DependencyIDs, "assigned_agent_id": task.AssignedAgentID, "plan": task.Plan,
		"plan_feedback": task.PlanFeedback, "test_plan": task.TestPlan, "test_feedback": task.TestFeedback,
		"worktree_branch": task.WorktreeBranch, "worktree_base_sha": task.WorktreeBaseSHA,
		"pr_url": task.PRUrl, "phase": task.Phase,
		"tests_for": task.TestsFor, "tdd_exceptions": task.TDDExceptions,
		"needs_human_review": task.NeedsHumanReview, "context": task.Context, "updated_at": task.UpdatedAt,
	}
}

func casSubtaskCompletion(tx *gorm.DB, task *model.Task, actor string, references map[string]any) error {
	event, err := state.GuardedCompleteSubtask(task, state.TransitionRequest{
		Target: model.StatusDone, Actor: actor, ExpectedStatus: task.Status,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: actor, Source: "agent_completion",
			Reason: "worker completion accepted", NormalizedReason: "accepted_worker_completion",
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	oldStatus := model.TaskStatus(event.OldValue)
	oldVersion := task.StateVersion
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, oldStatus, oldVersion).
		Updates(map[string]any{"status": model.StatusDone, "state_version": oldVersion + 1,
			"assigned_agent_id": nil, "context": task.Context, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: subtask completion %s/%d was already claimed", state.ErrStaleTransition, oldStatus, oldVersion)
	}
	task.StateVersion = oldVersion + 1
	task.AssignedAgentID = nil
	return tx.Create(event).Error
}

func casAcceptedExistingSubtask(tx *gorm.DB, task *model.Task, references map[string]any) error {
	event, err := state.GuardedAcceptExistingSubtask(task, state.TransitionRequest{
		Target: model.StatusDone, Actor: "orchestrator", ExpectedStatus: task.Status,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: "orchestrator", Source: "dedup_existing_work",
			Reason: "estimated files and commit evidence already present", NormalizedReason: "accepted_existing_work",
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	oldStatus := model.TaskStatus(event.OldValue)
	oldVersion := task.StateVersion
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, oldStatus, oldVersion).
		Updates(map[string]any{"status": model.StatusDone, "state_version": oldVersion + 1,
			"assigned_agent_id": nil, "context": task.Context, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: existing-work acceptance %s/%d was already claimed", state.ErrStaleTransition, oldStatus, oldVersion)
	}
	task.StateVersion = oldVersion + 1
	task.AssignedAgentID = nil
	return tx.Create(event).Error
}

func casSupersedeCompletedTestSubtask(tx *gorm.DB, task *model.Task, actor string, references map[string]any) error {
	return casInvalidateCompletedTestSubtask(tx, task, model.StatusRejected, actor,
		"completed test subtask superseded after review rejection", references)
}

func casFailCompletedTestSubtask(tx *gorm.DB, task *model.Task, actor string, references map[string]any) error {
	return casInvalidateCompletedTestSubtask(tx, task, model.StatusFailed, actor,
		"automated review rejected test subtask; Codex correction required", references)
}

func casInvalidateCompletedTestSubtask(tx *gorm.DB, task *model.Task, target model.TaskStatus, actor, reason string, references map[string]any) error {
	originalStatus := task.Status
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	originalAssignment := task.AssignedAgentID
	persisted := false
	defer func() {
		if !persisted {
			task.Status = originalStatus
			task.StateVersion = originalVersion
			task.UpdatedAt = originalUpdatedAt
			task.AssignedAgentID = originalAssignment
		}
	}()

	event, err := state.GuardedInvalidateCompletedTestSubtask(task, state.TransitionRequest{
		Target: target, Actor: actor, ExpectedStatus: model.StatusDone,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: actor, Source: "review_gate",
			Reason:    reason,
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	task.StateVersion = originalVersion + 1
	task.AssignedAgentID = nil
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, model.StatusDone, originalVersion).
		Updates(map[string]any{"status": task.Status, "state_version": task.StateVersion,
			"assigned_agent_id": nil, "context": task.Context, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: test subtask supersession done/%d was already claimed", state.ErrStaleTransition, originalVersion)
	}
	if err := tx.Create(event).Error; err != nil {
		return fmt.Errorf("persist test subtask supersession event: %w", err)
	}
	persisted = true
	return nil
}
