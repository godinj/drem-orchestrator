package orchestrator

import (
	"fmt"

	"github.com/godinj/drem-orchestrator/internal/experiment"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/google/uuid"
)

// handleExperimentVariantCompleted handles the completion of an experiment variant.
// If the variant is the default variant, it triggers a merge.
// If all variants are done, it transitions the experiment to "review".
func (o *Orchestrator) handleExperimentVariantCompleted(task *model.Task, variant *experiment.Variant) error {
	// Mark the variant as passed
	if err := experiment.PassVariant(o.db, variant.ID); err != nil {
		return fmt.Errorf("failed to mark variant as passed: %w", err)
	}

	// If this is the default variant, trigger the merge
	if variant.IsDefault {
		// In a real implementation, this would trigger a merge of the default variant's work
		// For now, we just log that the default variant completed
		o.logger.Info("default experiment variant completed", "variant_id", variant.ID, "task_id", task.ID)
	}

	// Check if all variants are done
	allDone, err := o.isAllExperimentVariantsDone(variant.ExperimentID)
	if err != nil {
		return fmt.Errorf("failed to check if all experiment variants are done: %w", err)
	}

	if allDone {
		// Transition the experiment to review
		if err := experiment.MoveToReview(o.db, variant.ExperimentID); err != nil {
			return fmt.Errorf("failed to move experiment to review: %w", err)
		}

		o.logger.Info("all experiment variants completed, moving experiment to review", "experiment_id", variant.ExperimentID)
	}

	return nil
}

// handleExperimentVariantFailed handles the failure of an experiment variant.
// If the default variant failed, check if any challenger passed and auto-promote winner.
// If all variants are done, transition the experiment to "review".
func (o *Orchestrator) handleExperimentVariantFailed(task *model.Task, variant *experiment.Variant) error {
	// Mark the variant as failed
	if err := experiment.FailVariant(o.db, variant.ID); err != nil {
		return fmt.Errorf("failed to mark variant as failed: %w", err)
	}

	// If this is the default variant, check if any challenger passed and auto-promote winner
	if variant.IsDefault {
		// Find all non-default variants and check if any passed
		variants, err := o.getExperimentVariants(variant.ExperimentID)
		if err != nil {
			return fmt.Errorf("failed to get experiment variants: %w", err)
		}

		var winner *experiment.Variant
		for _, v := range variants {
			if !v.IsDefault && v.Status == experiment.VariantPassed {
				winner = v
				break
			}
		}

		// If a challenger passed, promote it
		if winner != nil {
			if err := experiment.PromoteVariant(o.db, winner.ID); err != nil {
				return fmt.Errorf("failed to promote winner variant: %w", err)
			}
			o.logger.Info("auto-promoted winner variant", "variant_id", winner.ID, "experiment_id", variant.ExperimentID)
		}
	}

	// Check if all variants are done
	allDone, err := o.isAllExperimentVariantsDone(variant.ExperimentID)
	if err != nil {
		return fmt.Errorf("failed to check if all experiment variants are done: %w", err)
	}

	if allDone {
		// Transition the experiment to review
		if err := experiment.MoveToReview(o.db, variant.ExperimentID); err != nil {
			return fmt.Errorf("failed to move experiment to review: %w", err)
		}

		o.logger.Info("all experiment variants completed, moving experiment to review", "experiment_id", variant.ExperimentID)
	}

	return nil
}

// Helper function to check if all variants of an experiment are done
func (o *Orchestrator) isAllExperimentVariantsDone(experimentID uuid.UUID) (bool, error) {
	var variants []experiment.Variant
	if err := o.db.Where("experiment_id = ? AND status NOT IN (?)", experimentID, []experiment.VariantStatus{experiment.VariantPending, experiment.VariantRunning}).Find(&variants).Error; err != nil {
		return false, err
	}

	// Get all variants of this experiment
	var allVariants []experiment.Variant
	if err := o.db.Where("experiment_id = ?", experimentID).Find(&allVariants).Error; err != nil {
		return false, err
	}

	// If all variants have a terminal status, then all are done
	return len(allVariants) == len(variants), nil
}

// Helper function to get all variants of an experiment
func (o *Orchestrator) getExperimentVariants(experimentID uuid.UUID) ([]*experiment.Variant, error) {
	var variants []*experiment.Variant
	if err := o.db.Where("experiment_id = ?", experimentID).Find(&variants).Error; err != nil {
		return nil, err
	}
	return variants, nil
}
