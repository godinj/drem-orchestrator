package orchestrator

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ScheduleSummary provides an overview of the scheduling state for a project.
type ScheduleSummary struct {
	TasksByStatus  map[model.TaskStatus]int
	AgentsByStatus map[model.AgentStatus]int
	BlockedTasks   []BlockedTask
	QueueDepth     int // assignable tasks waiting for agents
}

// BlockedTask describes a task waiting on unfinished dependencies.
type BlockedTask struct {
	TaskID      uuid.UUID
	BlockingIDs []uuid.UUID
}

// GetScheduleSummary returns an overview of the project's scheduling state.
func GetScheduleSummary(db *gorm.DB, projectID uuid.UUID) (*ScheduleSummary, error) {
	summary := &ScheduleSummary{
		TasksByStatus:  make(map[model.TaskStatus]int),
		AgentsByStatus: make(map[model.AgentStatus]int),
	}

	// Count tasks by status.
	var taskCounts []struct {
		Status model.TaskStatus
		Count  int
	}
	if err := db.Model(&model.Task{}).
		Select("status, count(*) as count").
		Where("project_id = ?", projectID).
		Group("status").
		Find(&taskCounts).Error; err != nil {
		return nil, fmt.Errorf("get schedule summary: count tasks: %w", err)
	}
	for _, tc := range taskCounts {
		summary.TasksByStatus[tc.Status] = tc.Count
	}

	// Count agents by status.
	var agentCounts []struct {
		Status model.AgentStatus
		Count  int
	}
	if err := db.Model(&model.Agent{}).
		Select("status, count(*) as count").
		Where("project_id = ?", projectID).
		Group("status").
		Find(&agentCounts).Error; err != nil {
		return nil, fmt.Errorf("get schedule summary: count agents: %w", err)
	}
	for _, ac := range agentCounts {
		summary.AgentsByStatus[ac.Status] = ac.Count
	}

	// Find assignable tasks and blocked tasks.
	assignable, err := GetAssignableTasks(db, projectID)
	if err != nil {
		return nil, fmt.Errorf("get schedule summary: assignable: %w", err)
	}
	summary.QueueDepth = len(assignable)

	// Find blocked tasks: BACKLOG tasks (root or subtask) with unmet dependencies.
	var backlogSubtasks []model.Task
	if err := db.Where(
		"project_id = ? AND status = ? AND dependency_ids IS NOT NULL",
		projectID, model.StatusBacklog,
	).Find(&backlogSubtasks).Error; err != nil {
		return nil, fmt.Errorf("get schedule summary: blocked: %w", err)
	}

	for _, sub := range backlogSubtasks {
		if len(sub.DependencyIDs) == 0 {
			continue
		}
		blocking, err := GetBlockingTasks(db, sub.DependencyIDs)
		if err != nil {
			continue
		}
		if len(blocking) > 0 {
			summary.BlockedTasks = append(summary.BlockedTasks, BlockedTask{
				TaskID:      sub.ID,
				BlockingIDs: blocking,
			})
		}
	}

	return summary, nil
}

// GetAssignableTasks returns BACKLOG subtasks whose dependencies are all met
// and whose parent task is IN_PROGRESS. These are ready to be picked up by
// agents.
func GetAssignableTasks(db *gorm.DB, projectID uuid.UUID) ([]model.Task, error) {
	// Get BACKLOG subtasks that have an IN_PROGRESS parent.
	var candidates []model.Task
	if err := db.Where(
		"project_id = ? AND status = ? AND parent_task_id IS NOT NULL",
		projectID, model.StatusBacklog,
	).Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("get assignable tasks: query: %w", err)
	}

	// Filter to those with IN_PROGRESS parents.
	parentIDs := make(map[uuid.UUID]bool)
	for _, c := range candidates {
		if c.ParentTaskID != nil {
			parentIDs[*c.ParentTaskID] = false // placeholder
		}
	}

	if len(parentIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(parentIDs))
		for id := range parentIDs {
			ids = append(ids, id)
		}

		var inProgressParents []model.Task
		if err := db.Where("id IN ? AND status = ?", ids, model.StatusInProgress).
			Find(&inProgressParents).Error; err != nil {
			return nil, fmt.Errorf("get assignable tasks: check parents: %w", err)
		}
		for _, p := range inProgressParents {
			parentIDs[p.ID] = true
		}
	}

	var assignable []model.Task
	for _, c := range candidates {
		// Parent must be IN_PROGRESS.
		if c.ParentTaskID == nil || !parentIDs[*c.ParentTaskID] {
			continue
		}

		// Dependencies must be met.
		if len(c.DependencyIDs) > 0 {
			met, err := DependenciesMet(db, c.DependencyIDs)
			if err != nil || !met {
				continue
			}
		}

		assignable = append(assignable, c)
	}

	return assignable, nil
}

// DependenciesMet checks if all tasks in dependencyIDs have status DONE.
func DependenciesMet(db *gorm.DB, dependencyIDs []string) (bool, error) {
	if len(dependencyIDs) == 0 {
		return true, nil
	}

	var doneCount int64
	if err := db.Model(&model.Task{}).
		Where("id IN ? AND status = ?", dependencyIDs, model.StatusDone).
		Count(&doneCount).Error; err != nil {
		return false, fmt.Errorf("dependencies met: %w", err)
	}

	return int(doneCount) == len(dependencyIDs), nil
}

// GetBlockingTasks returns the subset of dependencyIDs that are not DONE.
func GetBlockingTasks(db *gorm.DB, dependencyIDs []string) ([]uuid.UUID, error) {
	if len(dependencyIDs) == 0 {
		return nil, nil
	}

	var notDone []model.Task
	if err := db.Where("id IN ? AND status != ?", dependencyIDs, model.StatusDone).
		Select("id").
		Find(&notDone).Error; err != nil {
		return nil, fmt.Errorf("get blocking tasks: %w", err)
	}

	blocking := make([]uuid.UUID, 0, len(notDone))
	for _, t := range notDone {
		blocking = append(blocking, t.ID)
	}
	return blocking, nil
}
