package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/branchpolicy"
	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
)

var ErrCheckpointResumeConflict = errors.New("checkpoint resume conflict")

type checkpointJournalHeader struct {
	Version        int               `json:"version"`
	PromptHash     string            `json:"prompt_hash"`
	Messages       []json.RawMessage `json:"messages"`
	NextIteration  int               `json:"next_iteration"`
	TokensIn       int               `json:"tokens_in"`
	TotalToolCalls int               `json:"total_tool_calls"`
	Completed      bool              `json:"completed"`
}

func loadCheckpointJournalHeader(path string) (checkpointJournalHeader, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return checkpointJournalHeader{}, err
	}
	var journal checkpointJournalHeader
	if err := json.Unmarshal(raw, &journal); err != nil {
		return checkpointJournalHeader{}, err
	}
	return journal, nil
}

// ResumeFailedCheckpoint continues an interrupted worker from its exact
// branch head and durable turn journal. Unlike AdoptFailedChild, this does not
// merge the checkpoint or mark the child done: the checkpoint is partial work
// and must still complete the immutable execution contract and ordinary
// branch/native/delivery gates.
func (o *Orchestrator) ResumeFailedCheckpoint(taskID uuid.UUID, commitSHA, actor string) error {
	commitSHA = strings.TrimSpace(commitSHA)
	actor = strings.TrimSpace(actor)
	if commitSHA == "" || actor == "" {
		return fmt.Errorf("%w: commit SHA and actor are required", ErrCheckpointResumeConflict)
	}
	if o.worktree == nil {
		return fmt.Errorf("resume failed checkpoint: no worktree manager configured")
	}

	o.workerLifecycleMu.Lock()
	defer o.workerLifecycleMu.Unlock()

	var child model.Task
	if err := o.db.First(&child, "id = ?", taskID).Error; err != nil {
		return fmt.Errorf("resume failed checkpoint: load child: %w", err)
	}
	if child.Status != model.StatusFailed || child.ParentTaskID == nil {
		return fmt.Errorf("%w: task must be a failed child", ErrCheckpointResumeConflict)
	}
	resumeStatus := model.StatusInProgress
	if child.Phase == "test" {
		resumeStatus = model.StatusTestWriting
	} else if child.Phase != "implementation" && child.Phase != "integration" {
		return fmt.Errorf("%w: child phase %q is not resumable", ErrCheckpointResumeConflict, child.Phase)
	}
	// A continuation preserves the same child, immutable prompt, branch head,
	// declared file scope, and dependency position. Those invariants are what
	// make it safe; the execution lane only controls how sibling work is
	// decomposed. A partial decomposed child must never be *adopted as done*,
	// but it can safely continue its own durable journal.

	var parent model.Task
	if err := o.db.First(&parent, "id = ?", *child.ParentTaskID).Error; err != nil {
		return fmt.Errorf("resume failed checkpoint: load parent: %w", err)
	}
	if (parent.Status != model.StatusFailed && parent.Status != resumeStatus) || strings.TrimSpace(parent.WorktreeBranch) == "" {
		return fmt.Errorf("%w: parent must be failed or already %s with an integration branch", ErrCheckpointResumeConflict, resumeStatus)
	}

	var active int64
	if err := o.db.Model(&model.WorkerAttempt{}).
		Where("task_id = ? AND completed_at IS NULL AND state IN ?", child.ID,
			[]string{model.WorkerAttemptReserved, model.WorkerAttemptRunning}).Count(&active).Error; err != nil {
		return fmt.Errorf("resume failed checkpoint: inspect active attempts: %w", err)
	}
	if active != 0 {
		return fmt.Errorf("%w: child still has an active worker attempt", ErrCheckpointResumeConflict)
	}

	var attempt model.WorkerAttempt
	if err := o.db.Where("task_id = ?", child.ID).Order("created_at DESC, id DESC").First(&attempt).Error; err != nil {
		return fmt.Errorf("%w: latest worker attempt unavailable: %v", ErrCheckpointResumeConflict, err)
	}
	if attempt.BaseSHA == "" || attempt.Branch == "" || attempt.RenderedPromptHash == "" || attempt.RenderedPromptPath == "" {
		return fmt.Errorf("%w: latest worker attempt lacks immutable branch or prompt identity", ErrCheckpointResumeConflict)
	}
	resolvedHead, err := gitexec.RunGit(context.Background(), o.worktree.BareRepo(), "rev-parse", attempt.Branch)
	if err != nil {
		return fmt.Errorf("resume failed checkpoint: resolve branch head: %w", err)
	}
	resolvedHead = strings.TrimSpace(resolvedHead)
	if !strings.EqualFold(resolvedHead, commitSHA) {
		return fmt.Errorf("%w: requested commit %s is not current branch head %s", ErrCheckpointResumeConflict, commitSHA, resolvedHead)
	}

	promptBytes, err := os.ReadFile(attempt.RenderedPromptPath)
	if err != nil {
		return fmt.Errorf("%w: read immutable rendered prompt: %v", ErrCheckpointResumeConflict, err)
	}
	promptSum := sha256.Sum256(promptBytes)
	if got := hex.EncodeToString(promptSum[:]); !strings.EqualFold(got, attempt.RenderedPromptHash) {
		return fmt.Errorf("%w: rendered prompt hash changed: got %s want %s", ErrCheckpointResumeConflict, got, attempt.RenderedPromptHash)
	}

	journalPath := filepath.Join(resolveWorkerPromptRoot(o.projectName), "journals", child.ID.String(), "journal.json")
	journal, err := loadCheckpointJournalHeader(journalPath)
	if err != nil {
		return fmt.Errorf("%w: decode durable turn journal: %v", ErrCheckpointResumeConflict, err)
	}
	if journal.Version != 1 || journal.NextIteration <= 0 || len(journal.Messages) < 2 || strings.TrimSpace(journal.PromptHash) == "" {
		return fmt.Errorf("%w: durable turn journal is not a valid resumable checkpoint", ErrCheckpointResumeConflict)
	}

	acceptance, acceptErr := branchpolicy.Accept(context.Background(), branchpolicy.AcceptanceRequest{
		RepoDir: o.worktree.BareRepo(), BaseRef: attempt.BaseSHA, TestContractBaseRef: parent.WorktreeBaseSHA,
		HeadRef: attempt.Branch, AllowedScopes: branchAcceptanceScopes(&child),
		RejectDestructiveRewrite: true, TestContract: testContractForAcceptance(&child),
	})
	if acceptErr != nil {
		return fmt.Errorf("resume failed checkpoint: branch acceptance: %w", acceptErr)
	}
	if !acceptance.Accepted {
		return fmt.Errorf("%w: checkpoint violates declared file scope: %v", ErrCheckpointResumeConflict, acceptance.Rejected)
	}

	oldParentStatus := parent.Status
	refs := map[string]any{
		"attempt_id": attempt.ID.String(), "commit_sha": resolvedHead, "branch": attempt.Branch,
		"journal_path": journalPath, "next_iteration": journal.NextIteration,
		"rendered_prompt_hash": attempt.RenderedPromptHash,
		"journal_completed":    journal.Completed,
	}
	err = o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&child, "id = ?", child.ID).Error; err != nil {
			return err
		}
		if err := tx.First(&parent, "id = ?", parent.ID).Error; err != nil {
			return err
		}
		if child.Status != model.StatusFailed || (parent.Status != model.StatusFailed && parent.Status != resumeStatus) {
			return fmt.Errorf("%w: task state changed during checkpoint resume", ErrCheckpointResumeConflict)
		}
		clearCodexAdoptionFailureContext(&child)
		clearCodexAdoptionFailureContext(&parent)
		child.AssignedAgentID = nil
		// The worker checkpoint branch is execution state, not the child's
		// integration branch. Keeping it in WorktreeBranch causes completion to
		// compare the checkpoint to itself and bypass the ordinary merge into the
		// parent's feature branch. buildSpawnContext reads the resume branch from
		// checkpoint_resume instead.
		child.WorktreeBranch = ""
		child.Context["checkpoint_resume"] = refs
		parent.Context["checkpoint_resume"] = refs
		if err := casTaskTransition(tx, &child, model.StatusFailed, model.StatusInProgress, actor,
			"checkpoint_resume", "resuming incomplete worker from admitted checkpoint and durable journal", refs); err != nil {
			return err
		}
		if parent.Status == model.StatusFailed {
			return casTaskTransition(tx, &parent, model.StatusFailed, resumeStatus, actor,
				"checkpoint_resume", "incomplete child checkpoint resumed parent pipeline", refs)
		}
		oldVersion := parent.StateVersion
		parent.UpdatedAt = time.Now()
		res := tx.Model(&model.Task{}).
			Where("id = ? AND status = ? AND state_version = ?", parent.ID, resumeStatus, oldVersion).
			Updates(map[string]any{"context": parent.Context, "state_version": oldVersion + 1, "updated_at": parent.UpdatedAt})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: parent state changed during checkpoint resume", ErrCheckpointResumeConflict)
		}
		parent.StateVersion = oldVersion + 1
		return nil
	})
	if err != nil {
		return fmt.Errorf("resume failed checkpoint: persist state: %w", err)
	}

	if journal.Completed {
		if attempt.AgentID == nil {
			return fmt.Errorf("%w: completed checkpoint lacks its worker agent identity", ErrCheckpointResumeConflict)
		}
		var ag model.Agent
		if err := o.db.First(&ag, "id = ?", *attempt.AgentID).Error; err != nil {
			return fmt.Errorf("resume failed checkpoint: load completed worker agent: %w", err)
		}
		ag.WorktreeBranch = attempt.Branch
		ag.CurrentTaskID = &child.ID
		if err := o.onAgentCompleted(&ag, &child); err != nil {
			return fmt.Errorf("resume failed checkpoint: finalize completed checkpoint: %w", err)
		}
		o.logger.Info("completed worker checkpoint finalized without another inference call",
			"task_id", child.ID, "parent_task_id", parent.ID, "commit_sha", resolvedHead)
		return nil
	}

	o.emit("task_updated", &child)
	o.emit("task_updated", &parent)
	o.publishTaskTransition(child.ID.String(), string(model.StatusFailed), string(model.StatusInProgress), "partial worker checkpoint resumed")
	if oldParentStatus != parent.Status {
		o.publishTaskTransition(parent.ID.String(), string(oldParentStatus), string(parent.Status), "partial child checkpoint resumed parent pipeline")
	}
	o.logger.Info("worker checkpoint resumed", "task_id", child.ID, "parent_task_id", parent.ID, "commit_sha", resolvedHead, "next_iteration", journal.NextIteration)
	return nil
}
