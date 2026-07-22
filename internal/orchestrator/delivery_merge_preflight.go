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
	if _, err := o.ensureMergeIntent(task, artifact, verification, authorization); err != nil {
		return false, fmt.Errorf("delivery preflight: merge intent: %w", err)
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

func (o *Orchestrator) ensureMergeIntent(task *model.Task, artifact model.DeliveryArtifact, verification model.VerificationRecord, authorization model.IntegrationAuthorization) (model.MergeIntent, error) {
	var intent model.MergeIntent
	err := o.db.Where("integration_authorization_id = ?", authorization.ID).First(&intent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		intent = model.MergeIntent{
			ID: uuid.New(), TaskID: task.ID, DeliveryArtifactID: artifact.ID,
			VerificationRecordID: verification.ID, IntegrationAuthorizationID: authorization.ID,
			ArtifactCommitSHA: artifact.CommitSHA, FeatureBranch: artifact.Branch,
			TargetBranch: artifact.BaseBranch, TargetBaseSHA: artifact.BaseSHA,
			Actor: "orchestrator", Source: "legacy_merging_backfill",
		}
		if createErr := o.db.Create(&intent).Error; createErr != nil {
			// A concurrent reconciliation may have created the unique intent.
			if reloadErr := o.db.Where("integration_authorization_id = ?", authorization.ID).First(&intent).Error; reloadErr != nil {
				return model.MergeIntent{}, fmt.Errorf("create merge intent: %w", createErr)
			}
		}
	} else if err != nil {
		return model.MergeIntent{}, err
	}
	if intent.TaskID != task.ID || intent.DeliveryArtifactID != artifact.ID ||
		intent.VerificationRecordID != verification.ID || intent.IntegrationAuthorizationID != authorization.ID ||
		intent.ArtifactCommitSHA != artifact.CommitSHA || intent.FeatureBranch != artifact.Branch ||
		intent.TargetBranch != artifact.BaseBranch || intent.TargetBaseSHA != artifact.BaseSHA {
		return model.MergeIntent{}, ErrStaleArtifact
	}
	return intent, nil
}

// recoverAuthorizedMerge treats the target ref as authoritative. It completes
// a task only when the immutable intent's exact artifact commit is an ancestor
// of the current target ref; Agentmon/container reports are not consulted.
func (o *Orchestrator) recoverAuthorizedMerge(task *model.Task) (*model.MergeCompletion, bool, error) {
	if task == nil || task.Status != model.StatusMerging {
		return nil, false, nil
	}
	artifact, err := currentArtifact(o.db, task.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var verification model.VerificationRecord
	if err := o.db.Where("delivery_artifact_id = ? AND result = ?", artifact.ID, model.VerificationPassed).
		Order("created_at DESC").First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var authorization model.IntegrationAuthorization
	if err := o.db.Where("delivery_artifact_id = ? AND verification_record_id = ?", artifact.ID, verification.ID).
		Order("created_at DESC").First(&authorization).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	intent, err := o.ensureMergeIntent(task, artifact, verification, authorization)
	if err != nil {
		return nil, false, err
	}
	bareRepo := ""
	if o.worktree != nil {
		bareRepo = o.worktree.BareRepo()
	}
	if strings.TrimSpace(bareRepo) == "" {
		return nil, false, errors.New("merge recovery: authoritative bare repository is unavailable")
	}
	targetRef := "refs/heads/" + strings.TrimPrefix(intent.TargetBranch, "refs/heads/")
	targetSHA, err := gitexec.RunGit(context.Background(), bareRepo, "rev-parse", "--verify", targetRef+"^{commit}")
	if err != nil {
		return nil, false, fmt.Errorf("merge recovery: resolve target ref: %w", err)
	}
	if strings.TrimSpace(targetSHA) == strings.TrimSpace(intent.TargetBaseSHA) {
		return nil, false, nil
	}
	if _, err := gitexec.RunGit(context.Background(), bareRepo, "merge-base", "--is-ancestor", intent.ArtifactCommitSHA, targetSHA); err != nil {
		var gitErr *gitexec.GitError
		if errors.As(err, &gitErr) && gitErr.ReturnCode == 1 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("merge recovery: prove artifact ancestry: %w", err)
	}
	completion, err := o.completeAuthorizedMerge(task.ID, intent.ID, targetSHA, "authoritative_target_ref")
	if err != nil {
		return nil, false, err
	}
	return completion, true, nil
}

func (o *Orchestrator) completeAuthorizedMerge(taskID, intentID uuid.UUID, mergeCommitSHA, source string) (*model.MergeCompletion, error) {
	if !validObjectID(mergeCommitSHA) {
		return nil, errors.New("merge completion: full merge commit SHA is required")
	}
	if intentID == uuid.Nil {
		return nil, errors.New("merge completion: merge intent is required")
	}
	var completion model.MergeCompletion
	err := o.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ?", taskID).First(&completion).Error; err == nil {
			if completion.MergeIntentID != intentID || completion.MergeCommitSHA != mergeCommitSHA {
				return ErrIdempotencyConflict
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var task model.Task
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return err
		}
		if task.Status != model.StatusMerging {
			return fmt.Errorf("%w: task in %s, expected merging", state.ErrStaleTransition, task.Status)
		}
		var intent model.MergeIntent
		if err := tx.First(&intent, "id = ? AND task_id = ?", intentID, task.ID).Error; err != nil {
			return fmt.Errorf("merge completion: intent: %w", err)
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
		if verification.CommitSHA != artifact.CommitSHA || authorization.CommitSHA != artifact.CommitSHA || authorization.BaseSHA != artifact.BaseSHA ||
			intent.DeliveryArtifactID != artifact.ID || intent.VerificationRecordID != verification.ID ||
			intent.IntegrationAuthorizationID != authorization.ID || intent.ArtifactCommitSHA != artifact.CommitSHA ||
			intent.FeatureBranch != artifact.Branch || intent.TargetBranch != artifact.BaseBranch || intent.TargetBaseSHA != artifact.BaseSHA {
			return ErrStaleArtifact
		}
		completion = model.MergeCompletion{
			ID: uuid.New(), TaskID: task.ID, MergeIntentID: intent.ID, DeliveryArtifactID: artifact.ID,
			VerificationRecordID: verification.ID, IntegrationAuthorizationID: authorization.ID,
			ArtifactCommitSHA: artifact.CommitSHA, VerifiedBaseSHA: artifact.BaseSHA,
			MergeCommitSHA: mergeCommitSHA, Actor: "orchestrator", Source: source,
		}
		if err := tx.Create(&completion).Error; err != nil {
			return fmt.Errorf("merge completion: create record: %w", err)
		}
		return casTaskTransition(tx, &task, model.StatusMerging, model.StatusDone, completion.Actor, completion.Source,
			"authorized artifact merged", map[string]any{
				"merge_completion_id": completion.ID.String(), "merge_intent_id": intent.ID.String(),
				"delivery_artifact_id":   artifact.ID.String(),
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
