package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/gitexec"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/state"
)

// preflightMergeArtifact is the last in-process guard before any merger can
// mutate the default branch. It consumes only typed, exact-SHA evidence.
func (o *Orchestrator) preflightMergeArtifact(task *model.Task) (bool, error) {
	artifact, err := currentArtifact(o.db, task.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, o.rewindMerging(task.ID, model.StatusTestingReady, uuid.Nil, "missing_delivery_artifact")
	}
	if err != nil {
		return false, err
	}

	var verification model.VerificationRecord
	if err := o.db.Where("delivery_artifact_id = ? AND result = ?", artifact.ID, model.VerificationPassed).
		Order("created_at DESC").First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, o.rewindMerging(task.ID, model.StatusTestingReady, artifact.ID, "missing_passing_verification")
		}
		return false, err
	}
	var authorization model.IntegrationAuthorization
	if err := o.db.Where("delivery_artifact_id = ? AND verification_record_id = ?", artifact.ID, verification.ID).
		Order("created_at DESC").First(&authorization).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, o.rewindMerging(task.ID, model.StatusIntegrationReady, artifact.ID, "missing_integration_authorization")
		}
		return false, err
	}
	if authorization.CommitSHA != artifact.CommitSHA || authorization.BaseSHA != artifact.BaseSHA || verification.CommitSHA != artifact.CommitSHA {
		return false, o.rewindMerging(task.ID, model.StatusInProgress, artifact.ID, "delivery_evidence_mismatch")
	}
	if artifact.Branch != task.WorktreeBranch {
		return false, o.rewindMerging(task.ID, model.StatusInProgress, artifact.ID, "delivery_branch_mismatch")
	}

	driftReason, err := o.deliveryArtifactDrift(task, &artifact)
	if err != nil {
		return false, err
	}
	if driftReason != "" {
		return false, o.rewindMerging(task.ID, model.StatusInProgress, artifact.ID, driftReason)
	}
	return true, nil
}

// deliveryArtifactDrift resolves both refs from the local integration
// worktree. It is used before authorization and again immediately before the
// merger is dispatched; the second check closes the Git TOCTOU window.
func (o *Orchestrator) deliveryArtifactDrift(task *model.Task, artifact *model.DeliveryArtifact) (string, error) {
	if task == nil || artifact == nil {
		return "", errors.New("delivery preflight: task and artifact are required")
	}
	worktreePath := o.resolveIntegrationWorktree(task)
	if worktreePath == "" {
		return "", errors.New("delivery preflight: integration worktree is unavailable")
	}
	branchSHA, err := gitexec.RunGit(context.Background(), worktreePath, "rev-parse", artifact.Branch)
	if err != nil {
		return "", fmt.Errorf("delivery preflight: resolve branch %s: %w", artifact.Branch, err)
	}
	if strings.TrimSpace(branchSHA) != strings.TrimSpace(artifact.CommitSHA) {
		return "delivery_commit_drift", nil
	}
	baseSHA, err := gitexec.RunGit(context.Background(), worktreePath, "rev-parse", artifact.BaseBranch)
	if err != nil {
		return "", fmt.Errorf("delivery preflight: resolve base %s: %w", artifact.BaseBranch, err)
	}
	if strings.TrimSpace(baseSHA) != strings.TrimSpace(artifact.BaseSHA) {
		return "target_drift_requires_reverification", nil
	}
	return "", nil
}

func (o *Orchestrator) rewindMerging(taskID uuid.UUID, target model.TaskStatus, artifactID uuid.UUID, reason string) error {
	return o.db.Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return err
		}
		if artifactID != uuid.Nil {
			now := time.Now()
			if err := tx.Model(&model.DeliveryArtifact{}).Where("id = ? AND invalidated_at IS NULL", artifactID).
				Updates(map[string]any{"invalidated_at": now, "invalidation_reason": reason}).Error; err != nil {
				return err
			}
		}
		return casTaskTransition(tx, &task, model.StatusMerging, target, "orchestrator", "merge_preflight", reason, map[string]any{
			"delivery_artifact_id": artifactID.String(),
		})
	})
}

func (o *Orchestrator) completeAuthorizedMerge(taskID uuid.UUID, mergeCommitSHA string) (*model.MergeCompletion, error) {
	if !validObjectID(mergeCommitSHA) {
		return nil, errors.New("merge completion: full merge commit SHA is required")
	}
	var completion model.MergeCompletion
	err := o.db.Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return err
		}
		if task.Status != model.StatusMerging {
			return fmt.Errorf("%w: task in %s, expected merging", state.ErrStaleTransition, task.Status)
		}
		artifact, err := currentArtifact(tx, task.ID)
		if err != nil {
			return err
		}
		var verification model.VerificationRecord
		if err := tx.Where("delivery_artifact_id = ? AND result = ?", artifact.ID, model.VerificationPassed).
			Order("created_at DESC").First(&verification).Error; err != nil {
			return fmt.Errorf("merge completion: passing verification: %w", err)
		}
		var authorization model.IntegrationAuthorization
		if err := tx.Where("delivery_artifact_id = ? AND verification_record_id = ?", artifact.ID, verification.ID).
			Order("created_at DESC").First(&authorization).Error; err != nil {
			return fmt.Errorf("merge completion: integration authorization: %w", err)
		}
		if verification.CommitSHA != artifact.CommitSHA || authorization.CommitSHA != artifact.CommitSHA || authorization.BaseSHA != artifact.BaseSHA {
			return ErrStaleArtifact
		}
		completion = model.MergeCompletion{
			ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifact.ID,
			VerificationRecordID: verification.ID, IntegrationAuthorizationID: authorization.ID,
			ArtifactCommitSHA: artifact.CommitSHA, VerifiedBaseSHA: artifact.BaseSHA,
			MergeCommitSHA: mergeCommitSHA, Actor: "orchestrator", Source: "merger_result",
		}
		if err := tx.Create(&completion).Error; err != nil {
			return fmt.Errorf("merge completion: create record: %w", err)
		}
		return casTaskTransition(tx, &task, model.StatusMerging, model.StatusDone, completion.Actor, completion.Source,
			"authorized artifact merged", map[string]any{
				"merge_completion_id": completion.ID.String(), "delivery_artifact_id": artifact.ID.String(),
				"verification_record_id": verification.ID.String(), "integration_authorization_id": authorization.ID.String(),
				"artifact_commit_sha": artifact.CommitSHA, "verified_base_sha": artifact.BaseSHA,
				"merge_commit_sha": mergeCommitSHA,
			})
	})
	if err != nil {
		return nil, err
	}
	return &completion, nil
}
