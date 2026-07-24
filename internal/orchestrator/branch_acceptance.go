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
	testContractBaseRef := strings.TrimSpace(task.WorktreeBaseSHA)
	if testContractBaseRef == "" && task.ParentTaskID != nil {
		var parent model.Task
		if loadErr := o.db.Select("worktree_base_sha").First(&parent, "id = ?", *task.ParentTaskID).Error; loadErr == nil {
			testContractBaseRef = strings.TrimSpace(parent.WorktreeBaseSHA)
		}
	}
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
		RepoDir:                  featureDir,
		BaseRef:                  baseRef,
		TestContractBaseRef:      testContractBaseRef,
		HeadRef:                  ag.WorktreeBranch,
		AllowedScopes:            branchAcceptanceScopes(task),
		RejectDestructiveRewrite: true,
		TestContract:             testContractForAcceptance(task),
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
		class := failureClassBranchContam
		if task.Phase == "test" && testContractRejectionsOnly(res.Rejected) {
			class = failureClassTestContract
		}
		o.markAttemptFailedForBranchAcceptance(task.ID, ag.ID, class, res.Rejected)
	}
	return res.Accepted, nil
}

func testContractForAcceptance(task *model.Task) string {
	if task == nil || task.Phase != "test" || task.Context == nil {
		return ""
	}
	contract, _ := task.Context["planned_interface_contract"].(string)
	return contract
}

func (o *Orchestrator) rejectWorkerBranchCompletion(ctx context.Context, ag *model.Agent, task *model.Task) error {
	ag.Status = model.AgentIdle
	ag.CurrentTaskID = nil
	if err := o.db.Save(ag).Error; err != nil {
		return fmt.Errorf("save agent after branch rejection: %w", err)
	}
	task.AssignedAgentID = nil
	now := time.Now()
	if rejections, ok := testContractOnlyRejections(task); ok {
		return o.retryTestContractRejection(ctx, ag, task, rejections, now)
	}
	summary := "worker branch contained changes outside the task's accepted file scope"
	budget := consumeRetryBudget(task, retryEdgeForTask(*task, string(ag.AgentType)), failureClassBranchContam, summary, now)
	if err := o.failTaskWithFailureEvidence(task,
		"worker branch rejected by deterministic scope acceptance", failureClassBranchContam, summary, now, budget); err != nil {
		return fmt.Errorf("fail task after branch rejection: %w", err)
	}
	o.logger.Warn("branch acceptance rejected worker completion", "task_id", task.ID, "agent_id", ag.ID)
	return nil
}

func testContractOnlyRejections(task *model.Task) ([]branchpolicy.Rejection, bool) {
	if task == nil || task.Phase != "test" || task.Context == nil {
		return nil, false
	}
	detail, ok := task.Context["branch_acceptance"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := detail["rejected"].([]any)
	if !ok || len(raw) == 0 {
		return nil, false
	}
	rejections := make([]branchpolicy.Rejection, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		rejection := branchpolicy.Rejection{
			Path: stringFromAny(entry["path"]), Status: stringFromAny(entry["status"]), Reason: stringFromAny(entry["reason"]),
		}
		rejections = append(rejections, rejection)
	}
	return rejections, testContractRejectionsOnly(rejections)
}

func testContractRejectionsOnly(rejections []branchpolicy.Rejection) bool {
	if len(rejections) == 0 {
		return false
	}
	for _, rejection := range rejections {
		switch rejection.Reason {
		case "missing_active_contract_assertion", "missing_active_runtime_assertion", "invalid_test_checkpoint":
		default:
			return false
		}
	}
	return true
}

func (o *Orchestrator) retryTestContractRejection(ctx context.Context, ag *model.Agent, task *model.Task, rejections []branchpolicy.Rejection, now time.Time) error {
	parts := make([]string, 0, len(rejections))
	for _, rejection := range rejections {
		parts = append(parts, fmt.Sprintf("%s (%s: %s)", rejection.Path, rejection.Reason, rejection.Status))
	}
	summary := "deterministic test admission rejected the checkpoint: " + strings.Join(parts, ", ")
	budget := consumeRetryBudget(task, retryEdgeForTask(*task, string(ag.AgentType)), failureClassTestContract, summary, now)
	if budget.Exhausted {
		return o.failTaskWithFailureEvidence(task,
			"test checkpoint did not satisfy the planned interface contract", failureClassTestContract, summary, now, budget)
	}

	delete(task.Context, "branch_acceptance")
	task.Context["prompt_adjustment"] = "Preserve the existing in-scope checkpoint. Make the smallest test-only edit needed to satisfy deterministic admission. The active added test code must address: " + strings.Join(parts, "; ") + ". Do not edit files outside writable_files and do not replace the existing test."
	task.Context["test_contract_rework"] = map[string]any{
		"previous_agent_id": ag.ID.String(), "reason": summary, "attempt": float64(budget.Attempts), "max_retries": float64(budget.MaxRetries),
	}
	if err := o.db.Save(task).Error; err != nil {
		return fmt.Errorf("save bounded test-contract rework: %w", err)
	}
	launch, err := o.workerLaunchService().Launch(ctx, task, model.AgentCoder)
	if err != nil {
		return o.failTaskWithFailureEvidence(task,
			"bounded test-contract rework could not launch", failureClassTestContract, err.Error(), now, budget)
	}
	o.logger.Warn("test checkpoint rejected; dispatched bounded contract rework",
		"task_id", task.ID, "prior_agent_id", ag.ID, "replacement_agent_id", launch.AgentID, "rejections", parts)
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

func (o *Orchestrator) markAttemptFailedForBranchAcceptance(taskID, agentID uuid.UUID, class string, rejections []branchpolicy.Rejection) {
	now := time.Now()
	summary := fmt.Sprintf("deterministic branch acceptance rejected checkpoint: %v", rejections)
	if err := o.db.Model(&model.WorkerAttempt{}).
		Where("task_id = ? AND agent_id = ? AND state IN ?", taskID, agentID, []string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).
		Updates(map[string]any{
			"state": model.WorkerAttemptFailed, "completed_at": &now, "failed_at": &now,
			"failure_classification": class, "first_error": truncate(summary, maxErrorSnippetLen),
		}).Error; err != nil {
		o.logger.Error("mark attempt failed after branch acceptance rejection", "task_id", taskID, "agent_id", agentID, "error", err)
	}
}

func branchAcceptanceScopes(task *model.Task) []string {
	if task == nil || task.Context == nil {
		return nil
	}
	if files := extractFileList(task.Context["writable_files"]); len(files) > 0 {
		return files
	}
	return extractFileList(task.Context["estimated_files"])
}
