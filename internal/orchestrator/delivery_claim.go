package orchestrator

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

// casClaimInProgressSubtask attaches a worker to a recovered, unassigned
// in-progress child without inventing an in_progress -> in_progress status
// transition. Assignment, state version, and audit event remain one CAS
// transaction so competing dispatchers cannot both claim the child.
func casClaimInProgressSubtask(tx *gorm.DB, task *model.Task, agentID uuid.UUID, actor, source string) error {
	if task == nil || task.ParentTaskID == nil || task.Status != model.StatusInProgress {
		return errors.New("only an in-progress subtask may be claimed without a status transition")
	}
	if task.AssignedAgentID != nil {
		return fmt.Errorf("%w: subtask is already assigned", state.ErrStaleTransition)
	}
	originalVersion := task.StateVersion
	originalUpdatedAt := task.UpdatedAt
	now := time.Now()
	task.AssignedAgentID = &agentID
	task.StateVersion = originalVersion + 1
	task.UpdatedAt = now
	persisted := false
	defer func() {
		if !persisted {
			task.AssignedAgentID = nil
			task.StateVersion = originalVersion
			task.UpdatedAt = originalUpdatedAt
		}
	}()

	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ? AND assigned_agent_id IS NULL",
			task.ID, model.StatusInProgress, originalVersion).
		Updates(map[string]any{"assigned_agent_id": agentID, "state_version": task.StateVersion, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: in-progress subtask claim was already taken", state.ErrStaleTransition)
	}
	event := &model.TaskEvent{
		ID: uuid.New(), TaskID: task.ID, EventType: "subtask_claimed",
		OldValue: string(model.StatusInProgress), NewValue: string(model.StatusInProgress),
		Details: model.JSONField{"evidence": map[string]any{
			"task_id": task.ID.String(), "actor": actor, "source": source,
			"reason": "unassigned in-progress subtask claimed", "timestamp": now.Format(time.RFC3339Nano),
			"references": map[string]any{"agent_id": agentID.String()},
		}},
		Actor: actor, CreatedAt: now,
	}
	if err := tx.Create(event).Error; err != nil {
		return fmt.Errorf("persist in-progress subtask claim event: %w", err)
	}
	persisted = true
	return nil
}

func currentArtifact(tx *gorm.DB, taskID uuid.UUID) (model.DeliveryArtifact, error) {
	var artifact model.DeliveryArtifact
	if err := tx.Where("task_id = ? AND invalidated_at IS NULL", taskID).First(&artifact).Error; err != nil {
		return artifact, fmt.Errorf("current delivery artifact: %w", err)
	}
	return artifact, nil
}
