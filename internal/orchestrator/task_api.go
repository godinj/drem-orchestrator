package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

var ErrRetryParentHasChildren = errors.New("retry task: refusing parent retry with existing child history")

// AddComment creates a new comment on a task. Allowed for tasks in any status.
func (o *Orchestrator) AddComment(taskID uuid.UUID, author, body string) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("add comment: load task: %w", err)
	}
	comment := model.TaskComment{
		ID:     uuid.New(),
		TaskID: taskID,
		Author: author,
		Body:   body,
	}
	if err := o.db.Create(&comment).Error; err != nil {
		return fmt.Errorf("add comment: %w", err)
	}
	o.logger.Info("comment added", "task_id", taskID, "author", author)
	return nil
}

// GetComments returns all comments for a task ordered by creation time.
func (o *Orchestrator) GetComments(taskID uuid.UUID) ([]model.TaskComment, error) {
	var comments []model.TaskComment
	if err := o.db.Where("task_id = ?", taskID).Order("created_at asc").Find(&comments).Error; err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}
	return comments, nil
}

// PauseTask pauses a task and stops its agents.
func (o *Orchestrator) PauseTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("pause task: load task: %w", err)
	}

	// Store previous status so we can resume later.
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["paused_from"] = string(task.Status)

	oldStatus := task.Status
	if err := o.transitionTaskAtomic(&task, model.StatusPaused, "operator", "task_api", "task paused", nil); err != nil {
		return fmt.Errorf("pause task: transition: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), string(oldStatus), string(task.Status), "user paused task")
	o.logger.Info("task paused", "task_id", task.ID)
	return nil
}

// ResumeTask resumes a paused task to its previous status.
func (o *Orchestrator) ResumeTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("resume task: load task: %w", err)
	}

	if task.Status != model.StatusPaused {
		return fmt.Errorf("resume task: task %s is in %s, expected paused", taskID, task.Status)
	}

	// Determine the status to resume to.
	resumeTo := model.StatusBacklog
	if task.Context != nil {
		if prev, ok := task.Context["paused_from"].(string); ok {
			parsed, err := model.ParseTaskStatus(prev)
			if err == nil {
				resumeTo = parsed
			}
		}
	}

	oldStatus := task.Status
	reason := "task resumed"
	if state.ValidateTransition(task.Status, resumeTo) != nil {
		resumeTo = model.StatusBacklog
		reason = "task resumed with backlog fallback"
	}
	if err := o.transitionTaskAtomic(&task, resumeTo, "operator", "task_api", reason, nil); err != nil {
		return fmt.Errorf("resume task: transition: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), string(oldStatus), string(task.Status), "user resumed task")
	o.logger.Info("task resumed", "task_id", task.ID, "status", task.Status)
	return nil
}

// RetryTask transitions a FAILED task back to BACKLOG.
func (o *Orchestrator) RetryTask(taskID uuid.UUID) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("retry task: load task: %w", err)
	}

	if task.Status != model.StatusFailed {
		return fmt.Errorf("retry task: task %s is in %s, expected failed", taskID, task.Status)
	}
	if task.ParentTaskID == nil {
		var childCount int64
		if err := o.db.Model(&model.Task{}).Where("parent_task_id = ?", task.ID).Count(&childCount).Error; err != nil {
			return fmt.Errorf("retry task: count children: %w", err)
		}
		if childCount > 0 {
			return fmt.Errorf("%w for task %s", ErrRetryParentHasChildren, taskID)
		}
	}

	// Reset retry count.
	if task.Context != nil {
		delete(task.Context, "retry_count")
		delete(task.Context, "last_error")
	}

	// Clear additional failure context keys.
	if task.Context != nil {
		delete(task.Context, "failure_diagnosis")
		delete(task.Context, "failure_category")
		delete(task.Context, "prompt_adjustment")
		delete(task.Context, "empty_work")
		delete(task.Context, "constraint_violations")
		delete(task.Context, "schedule")
		delete(task.Context, "test_rejection_count")
		for key := range task.Context {
			if strings.HasPrefix(key, "test_rejection_feedback_") {
				delete(task.Context, key)
			}
		}
	}

	// Unlink stale agents that still reference this task.
	var staleAgents []model.Agent
	if err := o.db.Where("current_task_id = ?", taskID).Find(&staleAgents).Error; err == nil {
		for i := range staleAgents {
			staleAgents[i].CurrentTaskID = nil
			if staleAgents[i].Status == model.AgentDead {
				staleAgents[i].Status = model.AgentIdle
			}
			o.db.Save(&staleAgents[i])
		}
	}
	task.AssignedAgentID = nil

	if task.ParentTaskID == nil {
		if task.WorktreeBranch != "" && o.worktree != nil {
			featureName := strings.TrimPrefix(task.WorktreeBranch, "feature/")
			if err := o.worktree.RemoveFeature(featureName); err != nil {
				return fmt.Errorf("retry task: remove stale feature %s: %w", task.WorktreeBranch, err)
			}
			task.WorktreeBranch = ""
			task.WorktreeBaseSHA = ""
		}

		var staleChildren []model.Task
		if err := o.db.Where("parent_task_id = ?", task.ID).Find(&staleChildren).Error; err != nil {
			return fmt.Errorf("retry task: load stale children: %w", err)
		}
		for i := range staleChildren {
			staleChildren[i].AssignedAgentID = nil
			staleChildren[i].ParentTaskID = nil
			if err := o.transitionTaskAtomic(&staleChildren[i], model.StatusCancelled, "operator", "parent_retry",
				"stale child detached during parent retry", map[string]any{"parent_task_id": task.ID.String()}); err != nil {
				return fmt.Errorf("retry task: cancel stale child %s: %w", staleChildren[i].ID, err)
			}
		}
	}

	oldStatus := task.Status
	if err := o.transitionTaskAtomic(&task, model.StatusBacklog, "operator", "task_retry", "failed task retried", nil); err != nil {
		return fmt.Errorf("retry task: transition: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), string(oldStatus), string(task.Status), "user retried task")
	o.logger.Info("task retried", "task_id", task.ID)
	return nil
}

// CreateTask creates a new task in CLASSIFYING. The classifier agent will
// determine the actual category and complexity score.
func (o *Orchestrator) CreateTask(title, description string, priority int) (*model.Task, error) {
	task := &model.Task{
		ID:          uuid.New(),
		ProjectID:   o.projectID,
		Title:       title,
		Description: description,
		Status:      model.StatusClassifying,
		Priority:    priority,
		Category:    model.CategoryStandard,
	}

	if err := o.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	o.emit("task_created", task)
	o.publishTaskTransition(task.ID.String(), "", string(model.StatusClassifying), "task created")
	o.logger.Info("task created", "task_id", task.ID, "title", title)
	return task, nil
}

// OverrideClassification transitions a CLASSIFYING task to BACKLOG with the
// specified category and complexity score. This powers the human override
// from the TUI.
func (o *Orchestrator) OverrideClassification(taskID uuid.UUID, category model.TaskCategory, complexityScore int) error {
	var task model.Task
	if err := o.db.First(&task, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("override classification: find task: %w", err)
	}
	if task.Status != model.StatusClassifying {
		return fmt.Errorf("override classification: task %s is in %s, not classifying", taskID, task.Status)
	}

	task.Category = category
	task.ComplexityScore = complexityScore

	if err := o.transitionTaskAtomic(&task, model.StatusBacklog, "operator", "classification_override",
		"classification overridden", map[string]any{"category": string(category), "complexity_score": complexityScore}); err != nil {
		return fmt.Errorf("override classification: transition: %w", err)
	}

	o.emit("task_updated", &task)
	o.publishTaskTransition(task.ID.String(), string(model.StatusClassifying), string(model.StatusBacklog), "classification overridden by user")
	o.logger.Info("classification overridden", "task_id", taskID, "category", category, "complexity", complexityScore)
	return nil
}
