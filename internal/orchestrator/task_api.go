package orchestrator

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

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

	evt, err := state.TransitionTask(&task, model.StatusPaused, "user", map[string]any{"action": "pause"})
	if err != nil {
		return fmt.Errorf("pause task: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("pause task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("pause task: save event: %w", err)
	}

	o.emit("task_updated", &task)
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

	// Validate the resume transition is allowed from PAUSED.
	evt, err := state.TransitionTask(&task, resumeTo, "user", map[string]any{"action": "resume"})
	if err != nil {
		// If the original status isn't reachable from PAUSED, fall back to BACKLOG.
		evt, err = state.TransitionTask(&task, model.StatusBacklog, "user", map[string]any{"action": "resume", "fallback": true})
		if err != nil {
			return fmt.Errorf("resume task: transition: %w", err)
		}
	}

	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("resume task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("resume task: save event: %w", err)
	}

	o.emit("task_updated", &task)
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

	evt, err := state.TransitionTask(&task, model.StatusBacklog, "user", map[string]any{"action": "retry"})
	if err != nil {
		return fmt.Errorf("retry task: transition: %w", err)
	}
	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("retry task: save task: %w", err)
	}
	if err := o.db.Create(evt).Error; err != nil {
		return fmt.Errorf("retry task: save event: %w", err)
	}

	o.emit("task_updated", &task)
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

	task.Status = model.StatusBacklog
	task.Category = category
	task.ComplexityScore = complexityScore

	if err := o.db.Save(&task).Error; err != nil {
		return fmt.Errorf("override classification: save task: %w", err)
	}

	o.emit("task_updated", &task)
	o.logger.Info("classification overridden", "task_id", taskID, "category", category, "complexity", complexityScore)
	return nil
}
