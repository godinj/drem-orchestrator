package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/branchpolicy"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

// ensureParentDeliveryAcceptance freezes deterministic whole-feature scope
// evidence after all child branches have merged. Child acceptances cannot
// authorize a parent artifact because they each cover only one worker delta.
func (o *Orchestrator) ensureParentDeliveryAcceptance(ctx context.Context, task *model.Task) error {
	if task == nil || task.ParentTaskID != nil {
		return errors.New("parent delivery acceptance requires a top-level task")
	}
	branch := strings.TrimSpace(task.WorktreeBranch)
	if branch == "" || strings.HasPrefix(branch, "refs/") || strings.Contains(branch, "..") {
		return errors.New("parent delivery acceptance requires a canonical feature branch")
	}

	// A prior typed acceptance remains authoritative when the synthesis
	// prerequisites are unavailable. The delivery gate will validate its exact
	// branch ref and SHA and will record a typed configuration failure if the
	// worktree manager or repository is unavailable.
	var prior model.BranchAcceptanceRecord
	priorErr := o.db.Where("task_id = ? AND accepted = ? AND branch = ?", task.ID, true, branch).
		Order("created_at DESC, id DESC").First(&prior).Error
	if priorErr != nil && !errors.Is(priorErr, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load prior parent branch acceptance: %w", priorErr)
	}
	hasPrior := priorErr == nil
	if o.worktree == nil {
		if hasPrior {
			return nil
		}
		return fmt.Errorf("typed branch acceptance is missing for canonical feature branch %s: worktree manager is unavailable", branch)
	}
	featureName := strings.TrimPrefix(branch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	if info, err := os.Stat(featureDir); err != nil || !info.IsDir() {
		if hasPrior {
			return nil
		}
		return fmt.Errorf("typed branch acceptance is missing for canonical feature branch %s", branch)
	}
	headSHA, err := gitexec.RunGit(ctx, featureDir, "rev-parse", branch)
	if err != nil {
		return fmt.Errorf("resolve parent branch head: %w", err)
	}
	headSHA = strings.TrimSpace(headSHA)
	var existing model.BranchAcceptanceRecord
	if err := o.db.Where("task_id = ? AND accepted = ? AND branch = ? AND head_sha = ?", task.ID, true, branch, headSHA).
		Order("created_at DESC, id DESC").First(&existing).Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load parent branch acceptance: %w", err)
	}

	scopes := branchAcceptanceScopes(task)
	if len(scopes) == 0 {
		var children []model.Task
		if err := o.db.Where("parent_task_id = ?", task.ID).Find(&children).Error; err != nil {
			return fmt.Errorf("load child scopes: %w", err)
		}
		seen := map[string]struct{}{}
		for i := range children {
			for _, path := range branchAcceptanceScopes(&children[i]) {
				if _, ok := seen[path]; ok {
					continue
				}
				seen[path] = struct{}{}
				scopes = append(scopes, path)
			}
		}
	}
	if len(scopes) == 0 {
		return errors.New("parent delivery acceptance has no declared child file scopes")
	}
	baseRef := strings.TrimSpace(task.WorktreeBaseSHA)
	if baseRef == "" {
		baseRef = o.worktree.DefaultBranchName()
	}
	result, err := branchpolicy.Accept(ctx, branchpolicy.AcceptanceRequest{
		RepoDir: featureDir, BaseRef: baseRef, HeadRef: branch, AllowedScopes: scopes,
	})
	if err != nil {
		return fmt.Errorf("accept parent delivery branch: %w", err)
	}
	details, err := acceptanceDetails(result, uuid.Nil)
	if err != nil {
		return err
	}
	details["reason"] = "accepted_parent_delivery_candidate"
	details["scope_source"] = "union_of_child_estimated_files"
	record := model.BranchAcceptanceRecord{
		ID: uuid.New(), TaskID: task.ID, AgentID: uuid.Nil, Branch: branch,
		Accepted: result.Accepted, BaseBranch: o.worktree.DefaultBranchName(),
		BaseSHA: result.BaseSHA, HeadSHA: result.HeadSHA, Details: details,
		Actor: "orchestrator", Source: "parent_delivery_acceptance", CreatedAt: time.Now(),
	}
	eventType := "branch_acceptance_accepted"
	if !result.Accepted {
		eventType = "branch_acceptance_rejected"
	}
	if err := o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		return tx.Create(&model.TaskEvent{
			ID: uuid.New(), TaskID: task.ID, EventType: eventType, Details: details,
			Actor: "orchestrator", CreatedAt: time.Now(),
		}).Error
	}); err != nil {
		return fmt.Errorf("record parent branch acceptance: %w", err)
	}
	if !result.Accepted {
		return fmt.Errorf("parent delivery branch violates declared file scope: %v", result.Rejected)
	}
	return nil
}

func (o *Orchestrator) acceptWorkerBranchCompletion(ctx context.Context, ag *model.Agent, task *model.Task, featureDir string) (bool, error) {
	if featureDir == "" || o.worktree == nil {
		return true, nil
	}
	baseRef := o.worktree.DefaultBranchName()
	var attempt model.WorkerAttempt
	if loadErr := o.db.Where("task_id = ? AND agent_id = ?", task.ID, ag.ID).
		Order("created_at DESC").First(&attempt).Error; loadErr == nil && attempt.BaseSHA != "" {
		// A subtask is merged into a cumulative parent feature branch. Scope
		// acceptance must inspect only this worker's delta, not inherited
		// changes from earlier siblings. The immutable spawn-base SHA is the
		// precise boundary and remains valid after the worker branch is merged.
		baseRef = attempt.BaseSHA
	}
	res, err := branchpolicy.Accept(ctx, branchpolicy.AcceptanceRequest{
		RepoDir:       featureDir,
		BaseRef:       baseRef,
		HeadRef:       ag.WorktreeBranch,
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
	record := &model.BranchAcceptanceRecord{
		ID: uuid.New(), TaskID: task.ID, AgentID: ag.ID,
		Branch: task.WorktreeBranch, Accepted: res.Accepted,
		BaseBranch: res.BaseRef, BaseSHA: res.BaseSHA, HeadSHA: res.HeadSHA,
		Details: detail, Actor: "orchestrator", Source: "worker_branch_acceptance",
		CreatedAt: time.Now(),
	}

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
	if err := o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return fmt.Errorf("create typed branch acceptance: %w", err)
		}
		return tx.Create(evt).Error
	}); err != nil {
		return false, fmt.Errorf("record branch acceptance: %w", err)
	}
	if !res.Accepted {
		o.markAttemptFailedForBranchAcceptance(task.ID, ag.ID)
	}
	return res.Accepted, nil
}

func (o *Orchestrator) rejectWorkerBranchCompletion(ag *model.Agent, task *model.Task) error {
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("save agent after branch rejection: %w", err)
	}
	task.AssignedAgentID = nil
	now := time.Now()
	summary := "worker branch contained changes outside the task's accepted file scope"
	budget := consumeRetryBudget(task, retryEdgeForTask(*task, string(ag.AgentType)), failureClassBranchContam, summary, now)
	if err := o.failTaskWithFailureEvidence(task,
		"worker branch rejected by deterministic scope acceptance", failureClassBranchContam, summary, now, budget); err != nil {
		return fmt.Errorf("fail task after branch rejection: %w", err)
	}
	o.logger.Warn("branch acceptance rejected worker completion", "task_id", task.ID, "agent_id", ag.ID)
	return nil
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
