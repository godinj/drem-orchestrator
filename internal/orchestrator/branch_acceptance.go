package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/branchpolicy"
	"github.com/godinj/drem-orchestrator/internal/model"
)

func (o *Orchestrator) acceptWorkerBranchCompletion(ctx context.Context, ag *model.Agent, task *model.Task, featureDir string) (bool, error) {
	if featureDir == "" || o.worktree == nil {
		return true, nil
	}
	res, err := branchpolicy.Accept(ctx, branchpolicy.AcceptanceRequest{
		RepoDir:       featureDir,
		BaseRef:       o.worktree.DefaultBranchName(),
		AllowedScopes: branchAcceptanceScopes(task),
	})
	if err != nil {
		res.Accepted = false
		res.Rejected = append(res.Rejected, branchpolicy.Rejection{Reason: "acceptance_check_failed", Path: featureDir, Status: err.Error()})
	}

	detail, convErr := acceptanceDetails(res, ag.ID)
	if convErr != nil {
		return false, convErr
	}
	if err != nil {
		detail["error"] = err.Error()
	}
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	task.Context["branch_acceptance"] = detail

	eventType := "branch_acceptance_accepted"
	if !res.Accepted {
		eventType = "branch_acceptance_rejected"
	}
	evt := &model.TaskEvent{
		ID:        uuid.New(),
		TaskID:    task.ID,
		EventType: eventType,
		Details:   detail,
		Actor:     "orchestrator",
		CreatedAt: time.Now(),
	}
	if err := o.db.Create(evt).Error; err != nil {
		return false, fmt.Errorf("record branch acceptance: %w", err)
	}
	if !res.Accepted {
		o.markAttemptFailedForBranchAcceptance(task.ID, ag.ID)
	}
	return res.Accepted, nil
}

func acceptanceDetails(res branchpolicy.AcceptanceResult, agentID uuid.UUID) (model.JSONField, error) {
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var detail model.JSONField
	if err := json.Unmarshal(b, &detail); err != nil {
		return nil, err
	}
	detail["agent_id"] = agentID.String()
	if res.Accepted {
		detail["reason"] = "accepted_worker_completion"
	} else {
		detail["reason"] = branchpolicy.ReasonBranchContaminate
	}
	return detail, nil
}

func (o *Orchestrator) markAttemptFailedForBranchAcceptance(taskID, agentID uuid.UUID) {
	now := time.Now()
	if err := o.db.Model(&model.WorkerAttempt{}).
		Where("task_id = ? AND agent_id = ? AND state IN ?", taskID, agentID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
		Updates(map[string]any{"state": model.WorkerAttemptFailed, "completed_at": &now}).Error; err != nil {
		o.logger.Error("mark attempt failed after branch acceptance rejection", "task_id", taskID, "agent_id", agentID, "error", err)
	}
}

func branchAcceptanceScopes(task *model.Task) []string {
	if task == nil || task.Context == nil {
		return nil
	}
	return extractFileList(task.Context["estimated_files"])
}
