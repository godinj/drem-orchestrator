package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/branchpolicy"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

var ErrCodexAdoptionConflict = errors.New("Codex adoption conflict")

// AdoptFailedChild admits a host-authored correction to an isolated failed
// worker branch. The immutable worker attempt remains failed history; this
// operation validates the exact requested head, re-runs file-scope admission,
// merges only accepted work, completes the child, and reopens its parent.
func (o *Orchestrator) AdoptFailedChild(taskID uuid.UUID, commitSHA, actor string) error {
	commitSHA = strings.TrimSpace(commitSHA)
	actor = strings.TrimSpace(actor)
	if commitSHA == "" || actor == "" {
		return fmt.Errorf("%w: commit SHA and actor are required", ErrCodexAdoptionConflict)
	}
	if o.worktree == nil {
		return fmt.Errorf("adopt failed child: no worktree manager configured")
	}

	o.workerLifecycleMu.Lock()
	defer o.workerLifecycleMu.Unlock()

	var child model.Task
	if err := o.db.First(&child, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("adopt failed child: load child: %w", err)
	}
	if child.Status != model.StatusFailed || child.ParentTaskID == nil {
		return fmt.Errorf("%w: task must be a failed child", ErrCodexAdoptionConflict)
	}
	resumeStatus := model.StatusInProgress
	if child.Phase == "test" {
		resumeStatus = model.StatusTestWriting
	} else if child.Phase != "implementation" && child.Phase != "integration" {
		return fmt.Errorf("%w: child phase %q is not adoptable", ErrCodexAdoptionConflict, child.Phase)
	}
	allowedScopes := branchAcceptanceScopes(&child)
	if len(allowedScopes) == 0 {
		return fmt.Errorf("%w: child has no declared estimated_files scope", ErrCodexAdoptionConflict)
	}

	var parent model.Task
	if err := o.db.First(&parent, "id = ?", *child.ParentTaskID).Error; err != nil {
		return fmt.Errorf("adopt failed child: load parent: %w", err)
	}
	if (parent.Status != model.StatusFailed && parent.Status != resumeStatus) || strings.TrimSpace(parent.WorktreeBranch) == "" {
		return fmt.Errorf("%w: parent must be failed or already %s with an integration branch", ErrCodexAdoptionConflict, resumeStatus)
	}

	var active int64
	if err := o.db.Model(&model.WorkerAttempt{}).
		Where("task_id = ? AND completed_at IS NULL AND state IN ?", child.ID,
			[]string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).Count(&active).Error; err != nil {
		return fmt.Errorf("adopt failed child: inspect active attempts: %w", err)
	}
	if active != 0 {
		return fmt.Errorf("%w: child still has an active worker attempt", ErrCodexAdoptionConflict)
	}

	var attempt model.WorkerAttempt
	if err := o.db.Where("task_id = ?", child.ID).Order("created_at DESC, id DESC").First(&attempt).Error; err != nil {
		return fmt.Errorf("%w: latest worker attempt unavailable: %v", ErrCodexAdoptionConflict, err)
	}
	if attempt.BaseSHA == "" || attempt.Branch == "" {
		return fmt.Errorf("%w: latest worker attempt lacks immutable base or branch", ErrCodexAdoptionConflict)
	}

	featureName := strings.TrimPrefix(parent.WorktreeBranch, "feature/")
	featureDir := o.worktree.FeatureWorktreePath(featureName)
	resolvedHead, err := gitexec.RunGit(context.Background(), featureDir, "rev-parse", attempt.Branch)
	if err != nil {
		return fmt.Errorf("adopt failed child: resolve branch head: %w", err)
	}
	resolvedHead = strings.TrimSpace(resolvedHead)
	if !strings.EqualFold(resolvedHead, commitSHA) {
		return fmt.Errorf("%w: requested commit %s is not current branch head %s", ErrCodexAdoptionConflict, commitSHA, resolvedHead)
	}

	acceptance, acceptErr := branchpolicy.Accept(context.Background(), branchpolicy.AcceptanceRequest{
		RepoDir: featureDir, BaseRef: attempt.BaseSHA, TestContractBaseRef: parent.WorktreeBaseSHA,
		HeadRef: attempt.Branch, AllowedScopes: allowedScopes, TestContract: testContractForAcceptance(&child),
	})
	if acceptErr != nil {
		return fmt.Errorf("adopt failed child: branch acceptance: %w", acceptErr)
	}
	if !acceptance.Accepted {
		return fmt.Errorf("%w: repaired branch still violates declared file scope: %v", ErrCodexAdoptionConflict, acceptance.Rejected)
	}

	mergeResult, err := o.mergeAgentBranchIntoFeature(context.Background(), attempt.Branch, featureDir)
	if err != nil {
		return fmt.Errorf("adopt failed child: merge corrected branch: %w", err)
	}
	if mergeResult == nil || !mergeResult.Success {
		return fmt.Errorf("%w: corrected branch could not merge cleanly: %v", ErrCodexAdoptionConflict, mergeResult)
	}

	agentID := uuid.Nil
	if attempt.AgentID != nil {
		agentID = *attempt.AgentID
	}
	details, err := acceptanceDetails(acceptance, agentID)
	if err != nil {
		return fmt.Errorf("adopt failed child: encode acceptance: %w", err)
	}
	details["reason"] = "accepted_codex_adoption"
	details["commit_sha"] = resolvedHead
	details["actor"] = actor
	details["attempt_id"] = attempt.ID.String()
	details["merge_commit"] = mergeResult.MergeCommit

	oldParentStatus := parent.Status
	err = o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&child, "id = ?", child.ID).Error; err != nil {
			return err
		}
		if err := tx.First(&parent, "id = ?", parent.ID).Error; err != nil {
			return err
		}
		if child.Status != model.StatusFailed || (parent.Status != model.StatusFailed && parent.Status != resumeStatus) {
			return fmt.Errorf("%w: task state changed during adoption", ErrCodexAdoptionConflict)
		}
		parentWasFailed := parent.Status == model.StatusFailed
		clearCodexAdoptionFailureContext(&child)
		clearCodexAdoptionFailureContext(&parent)
		child.Context["branch_acceptance"] = details
		child.AssignedAgentID = nil

		record := model.BranchAcceptanceRecord{
			ID: uuid.New(), TaskID: child.ID, AgentID: agentID, Branch: attempt.Branch,
			Accepted: true, BaseBranch: acceptance.BaseRef, BaseSHA: acceptance.BaseSHA,
			HeadSHA: acceptance.HeadSHA, Details: details, Actor: actor,
			Source: "codex_adapter_adoption", CreatedAt: time.Now(),
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("record Codex branch acceptance: %w", err)
		}
		if err := casCodexAdoptedSubtask(tx, &child, actor, map[string]any{
			"attempt_id": attempt.ID.String(), "commit_sha": resolvedHead,
			"branch_acceptance_id": record.ID.String(),
		}); err != nil {
			return err
		}
		if parentWasFailed {
			return GuardedTaskTransitionTx(tx, &parent, resumeStatus, actor, "codex_adapter_adoption",
				"accepted host correction resumed parent pipeline", map[string]any{
					"child_task_id": child.ID.String(), "commit_sha": resolvedHead,
				})
		}
		// A failed implementation/integration child does not necessarily fail its
		// parent immediately. In that case the parent is already in the exact
		// resume state, so persist the cleared failure context with a CAS rather
		// than attempting an invalid same-state transition.
		oldVersion := parent.StateVersion
		parent.UpdatedAt = time.Now()
		res := tx.Model(&model.Task{}).
			Where("id = ? AND status = ? AND state_version = ?", parent.ID, resumeStatus, oldVersion).
			Updates(map[string]any{"context": parent.Context, "state_version": oldVersion + 1, "updated_at": parent.UpdatedAt})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: parent state changed during adoption", state.ErrStaleTransition)
		}
		parent.StateVersion = oldVersion + 1
		return nil
	})
	if err != nil {
		return fmt.Errorf("adopt failed child: persist state: %w", err)
	}

	if err := o.cleanupTaskWorkerBranch(context.Background(), &child, attempt.Branch); err != nil {
		o.logger.Warn("cleanup adopted child worktree failed", "task_id", child.ID, "branch", attempt.Branch, "error", err)
	}
	o.emit("task_updated", &child)
	o.emit("task_updated", &parent)
	o.publishTaskTransition(child.ID.String(), string(model.StatusFailed), string(model.StatusDone), "Codex branch correction adopted")
	if oldParentStatus != parent.Status {
		o.publishTaskTransition(parent.ID.String(), string(oldParentStatus), string(parent.Status), "Codex child correction resumed pipeline")
	}
	o.logger.Info("Codex correction adopted", "task_id", child.ID, "parent_task_id", parent.ID, "commit_sha", resolvedHead)
	return nil
}

func casCodexAdoptedSubtask(tx *gorm.DB, task *model.Task, actor string, references map[string]any) error {
	event, err := state.GuardedAdoptFailedSubtask(task, state.TransitionRequest{
		Target: model.StatusDone, Actor: actor, ExpectedStatus: model.StatusFailed,
		Evidence: state.Evidence{
			TaskID: task.ID, Actor: actor, Source: "codex_adapter_adoption",
			Reason: "deterministically admitted host correction", NormalizedReason: "accepted_codex_adoption",
			Timestamp: time.Now(), References: references,
		},
	})
	if err != nil {
		return err
	}
	oldVersion := task.StateVersion
	res := tx.Model(&model.Task{}).
		Where("id = ? AND status = ? AND state_version = ?", task.ID, model.StatusFailed, oldVersion).
		Updates(map[string]any{"status": model.StatusDone, "state_version": oldVersion + 1,
			"assigned_agent_id": nil, "context": task.Context, "updated_at": task.UpdatedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("%w: Codex adoption was already claimed", state.ErrStaleTransition)
	}
	task.StateVersion = oldVersion + 1
	return tx.Create(event).Error
}

func clearCodexAdoptionFailureContext(task *model.Task) {
	if task.Context == nil {
		task.Context = make(model.JSONField)
	}
	for _, key := range []string{
		"failure_reason", "failure_class", "failure_diagnosis", "failure_category",
		"latest_failure_reason", "latest_failure_class", "latest_failure_type",
		"latest_failure_summary", "latest_failure_at", "latest_failure_current",
		"latest_failure_retry_edge", "latest_failure_retry_attempts",
		"latest_failure_retry_max", "latest_failure_retry_exhausted",
		"last_error", "prompt_adjustment", "empty_work", "constraint_violations",
		"retry_budgets", "schedule",
	} {
		delete(task.Context, key)
	}
}
