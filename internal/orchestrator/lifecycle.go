package orchestrator

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// DeleteComment deletes a comment by ID.
func (o *Orchestrator) DeleteComment(commentID uuid.UUID) error {
	if err := o.db.Delete(&model.TaskComment{}, "id = ?", commentID).Error; err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}
	o.logger.Info("comment deleted", "comment_id", commentID)
	return nil
}

// DeletePlanStep removes a single step from a task's plan by index.
// Only valid for tasks in plan_review state.
func (o *Orchestrator) DeletePlanStep(taskID uuid.UUID, stepIndex int) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("delete plan step: load task: %w", err)
	}
	if task.Status != model.StatusPlanReview {
		return fmt.Errorf("delete plan step: task %s is in %s, expected plan_review", taskID, task.Status)
	}
	if task.Plan == nil {
		return fmt.Errorf("delete plan step: task %s has no plan", taskID)
	}
	subtasksRaw, ok := task.Plan["subtasks"]
	if !ok {
		return fmt.Errorf("delete plan step: no subtasks key in plan")
	}
	items, ok := subtasksRaw.([]any)
	if !ok || stepIndex < 0 || stepIndex >= len(items) {
		return fmt.Errorf("delete plan step: index %d out of range", stepIndex)
	}

	// Remove the step.
	items = append(items[:stepIndex], items[stepIndex+1:]...)
	task.Plan["subtasks"] = items

	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("delete plan step: save task: %w", err)
	}
	o.emit("task_updated", &task)
	o.logger.Info("plan step deleted", "task_id", taskID, "step_index", stepIndex)
	return nil
}

// DeleteSubtask removes a subtask and stops its agent if one is running.
func (o *Orchestrator) DeleteSubtask(subtaskID uuid.UUID) error {
	var sub model.Task
	if err := o.db.First(&sub, "id = ?", subtaskID).Error; err != nil {
		return fmt.Errorf("delete subtask: load: %w", err)
	}

	// Stop the assigned agent if it's running.
	if sub.AssignedAgentID != nil {
		agentID := *sub.AssignedAgentID
		if o.runner != nil {
			// StopAgent is best-effort — the agent may already be dead.
			if err := o.runner.StopAgent(agentID); err != nil {
				o.logger.Debug("stop agent during subtask delete (may be already stopped)", "agent_id", agentID, "error", err)
				// StopAgent failed (agent not in running map) — kill stale process
				// directly for idle/dead agents that still have one.
				var ag model.Agent
				if dbErr := o.db.First(&ag, "id = ?", agentID).Error; dbErr == nil {
					o.runner.KillStaleProcess(&ag)
				}
			}
		}
		// Mark agent as dead in DB regardless.
		o.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", model.AgentDead)
	}

	// Delete associated comments and events.
	o.db.Where("task_id = ?", subtaskID).Delete(&model.TaskComment{})
	o.db.Where("task_id = ?", subtaskID).Delete(&model.TaskEvent{})

	// Delete the subtask itself.
	if err := o.db.Delete(&sub).Error; err != nil {
		return fmt.Errorf("delete subtask: %w", err)
	}

	o.emit("task_updated", nil)
	o.logger.Info("subtask deleted", "subtask_id", subtaskID, "agent_id", sub.AssignedAgentID)
	return nil
}

// DeleteTask deletes a task and all of its subtasks. For parent tasks this
// cascades through every child subtask, stopping agents and cleaning up
// associated records. For subtasks (tasks with a ParentTaskID) it delegates
// to DeleteSubtask.
func (o *Orchestrator) DeleteTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("delete task: load: %w", err)
	}

	// If this is a subtask, delegate to DeleteSubtask.
	if task.ParentTaskID != nil {
		return o.DeleteSubtask(taskID)
	}

	// Delete all child subtasks first.
	var subtasks []model.Task
	if err := o.db.Where("parent_task_id = ?", taskID).Find(&subtasks).Error; err != nil {
		return fmt.Errorf("delete task: load subtasks: %w", err)
	}
	for _, sub := range subtasks {
		if err := o.DeleteSubtask(sub.ID); err != nil {
			o.logger.Warn("delete task: failed to delete subtask", "subtask_id", sub.ID, "error", err)
			// Continue deleting remaining subtasks.
		}
	}

	// Stop the parent task's own agent if assigned.
	if task.AssignedAgentID != nil {
		agentID := *task.AssignedAgentID
		if o.runner != nil {
			if err := o.runner.StopAgent(agentID); err != nil {
				o.logger.Debug("stop agent during task delete (may be already stopped)", "agent_id", agentID, "error", err)
				var ag model.Agent
				if dbErr := o.db.First(&ag, "id = ?", agentID).Error; dbErr == nil {
					o.runner.KillStaleProcess(&ag)
				}
			}
		}
		o.db.Model(&model.Agent{}).Where("id = ?", agentID).Update("status", model.AgentDead)
	}

	// Delete associated comments and events for the parent task.
	o.db.Where("task_id = ?", taskID).Delete(&model.TaskComment{})
	o.db.Where("task_id = ?", taskID).Delete(&model.TaskEvent{})

	// Delete the task itself.
	if err := o.db.Delete(&task).Error; err != nil {
		return fmt.Errorf("delete task: %w", err)
	}

	o.emit("task_updated", nil)
	o.logger.Info("task deleted", "task_id", taskID, "subtasks_deleted", len(subtasks))
	return nil
}
