package orchhttp

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchestrator"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func (s *Server) applyStaleAssignmentRecovery(ctx context.Context, task model.Task, result orchdto.StaleAssignmentRecoveryDTO, actor, observedStatus string, observedUpdatedAt *time.Time) error {
	now := time.Now()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ?", task.ID).First(&current).Error; err != nil {
			return err
		}
		if current.StateVersion != task.StateVersion {
			return errRecoveryConflict
		}
		if guardConflict(current, observedStatus, observedUpdatedAt) {
			return errRecoveryConflict
		}
		currentResult, err := (&Server{DB: tx}).classifyStaleAssignment(ctx, current)
		if err != nil {
			return err
		}
		if !currentResult.Safe || currentResult.Classification != result.Classification || currentResult.AssignedWorker != result.AssignedWorker {
			return errRecoveryConflict
		}
		if task.AssignedAgentID != nil {
			if err := tx.Model(&model.Agent{}).Where("id = ? AND current_task_id = ?", *current.AssignedAgentID, current.ID).
				Updates(map[string]any{"current_task_id": nil, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		res := tx.Model(&model.Task{}).Where("id = ? AND state_version = ?", current.ID, current.StateVersion).
			Updates(map[string]any{"assigned_agent_id": nil, "state_version": current.StateVersion + 1, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errRecoveryConflict
		}
		if err := tx.Create(&model.TaskEvent{
			ID: uuid.New(), TaskID: task.ID, EventType: staleAssignmentEvent,
			OldValue: result.AssignedWorker, NewValue: "", Actor: actor, CreatedAt: now,
			Details: model.JSONField{
				"actor": actor, "policy": "stale_assignment_guard", "evidence": result.Classification,
				"action": "clear_assignment", "result": "applied", "classification": result.Classification,
				"worker_status": result.WorkerStatus, "message": result.Message,
			},
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.TaskComment{
			ID: uuid.New(), TaskID: task.ID, Author: actor,
			Body:      fmt.Sprintf("Recovery applied: actor=%s policy=stale_assignment_guard evidence=%s action=clear_assignment result=applied. Previous worker: %s", actor, result.Classification, result.AssignedWorker),
			CreatedAt: now,
		}).Error
	})
}

func (s *Server) applyTaskRecovery(ctx context.Context, task model.Task, result orchdto.TaskRecoveryDTO, actor, observedStatus string, observedUpdatedAt *time.Time) error {
	now := time.Now()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ?", task.ID).First(&current).Error; err != nil {
			return err
		}
		if current.StateVersion != task.StateVersion {
			return errRecoveryConflict
		}
		if guardConflict(current, observedStatus, observedUpdatedAt) {
			return errRecoveryConflict
		}
		currentResult, err := (&Server{DB: tx}).classifyTaskRecovery(ctx, current, result.Action)
		if err != nil {
			return err
		}
		if !currentResult.Safe || currentResult.AffectedCount != result.AffectedCount {
			return errRecoveryConflict
		}
		switch result.Action {
		case "duplicate-active-attempts":
			attempts, err := (&Server{DB: tx}).activeAttempts(ctx, current.ID)
			if err != nil {
				return err
			}
			ids := duplicateActiveAttemptIDs(attempts)
			if len(ids) == 0 {
				return errRecoveryConflict
			}
			if err := tx.Model(&model.WorkerAttempt{}).Where("id IN ?", ids).
				Updates(map[string]any{"state": model.WorkerAttemptSuperseded, "completed_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			res := tx.Model(&model.Task{}).Where("id = ? AND state_version = ?", current.ID, current.StateVersion).
				Updates(map[string]any{"state_version": current.StateVersion + 1, "updated_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return errRecoveryConflict
			}
		case "contaminated-branch-fail-gate":
			if current.Context == nil {
				current.Context = make(model.JSONField)
			}
			current.Context["failure_reason"] = result.Evidence
			if err := orchestrator.GuardedTaskTransitionTx(tx, &current, model.StatusFailed, actor,
				"operator_recovery", "contaminated branch gate failed", map[string]any{
					"policy": result.Policy, "action": result.Action, "evidence": result.Evidence,
				}); err != nil {
				return err
			}
		default:
			return errRecoveryConflict
		}
		return s.recordTaskRecoveryAudit(tx, current.ID, actor, result, now)
	})
}

func (s *Server) recordTaskRecoveryAudit(tx *gorm.DB, taskID uuid.UUID, actor string, result orchdto.TaskRecoveryDTO, now time.Time) error {
	details := model.JSONField{"actor": actor, "policy": result.Policy, "evidence": result.Evidence, "action": result.Action, "result": "applied"}
	if err := tx.Create(&model.TaskEvent{
		ID: uuid.New(), TaskID: taskID, EventType: taskRecoveryEvent, NewValue: result.Action,
		Details: details, Actor: actor, CreatedAt: now,
	}).Error; err != nil {
		return err
	}
	return tx.Create(&model.TaskComment{
		ID: uuid.New(), TaskID: taskID, Author: actor,
		Body:      fmt.Sprintf("Recovery applied: actor=%s policy=%s evidence=%s action=%s result=applied", actor, result.Policy, result.Evidence, result.Action),
		CreatedAt: now,
	}).Error
}

func (s *Server) activeAttempts(ctx context.Context, taskID uuid.UUID) ([]model.WorkerAttempt, error) {
	var attempts []model.WorkerAttempt
	err := s.DB.WithContext(ctx).Where("task_id = ? AND state IN ?", taskID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
		Order("created_at DESC").Find(&attempts).Error
	return attempts, err
}

func guardConflict(task model.Task, observedStatus string, observedUpdatedAt *time.Time) bool {
	return (observedStatus != "" && observedStatus != string(task.Status)) ||
		(observedUpdatedAt != nil && !task.UpdatedAt.Equal(*observedUpdatedAt))
}

func dryRunResult(safe bool) string {
	if safe {
		return "would_apply"
	}
	return "refused"
}

func duplicateActiveAttemptIDs(attempts []model.WorkerAttempt) []uuid.UUID {
	seen := map[string]bool{}
	ids := make([]uuid.UUID, 0)
	for _, attempt := range attempts {
		key := attempt.AgentType + "\x00" + attempt.Branch
		if seen[key] {
			ids = append(ids, attempt.ID)
			continue
		}
		seen[key] = true
	}
	return ids
}
