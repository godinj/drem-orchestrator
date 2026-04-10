// Package experiment provides the Experiment and Variant GORM models along
// with creation logic for A/B-style profile experiments. An experiment runs
// the same task description across 2-3 agent profiles so the user can compare
// outputs.
package experiment

import (
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// FromTaskOpts holds options for creating an experiment from an existing task.
type FromTaskOpts struct {
	// Title is the title for the new experiment.
	Title string

	// Description is the description for the new experiment.
	Description string

	// Profiles are the agent profiles to use for the experiment.
	Profiles []string

	// DefaultProfile is the default profile to use.
	DefaultProfile string

	// ReusePlan specifies whether to reuse the source task's plan.
	ReusePlan bool
}

// CreateFromTask creates an experiment derived from an existing done task.
// The source task must be in StatusDone. Each variant gets a new task that
// either copies the source plan (reusesPlan=true, tasks start at
// StatusPlanReview) or starts fresh (reusesPlan=false, tasks start at
// StatusBacklog). The experiment's SourceTaskID is set to sourceTaskID.
func CreateFromTask(db *gorm.DB, projectID uuid.UUID, sourceTaskID uuid.UUID, opts FromTaskOpts) (*Experiment, error) {
	if len(opts.Profiles) < minProfiles {
		return nil, fmt.Errorf("experiment requires at least %d profiles, got %d", minProfiles, len(opts.Profiles))
	}
	if len(opts.Profiles) > maxProfiles {
		return nil, fmt.Errorf("experiment allows at most %d profiles, got %d", maxProfiles, len(opts.Profiles))
	}
	if !containsProfile(opts.Profiles, opts.DefaultProfile) {
		return nil, fmt.Errorf("default profile %q not found in profiles %v", opts.DefaultProfile, opts.Profiles)
	}

	var src model.Task
	if err := db.First(&src, "id = ?", sourceTaskID).Error; err != nil {
		return nil, fmt.Errorf("load source task: %w", err)
	}
	if src.Status != model.StatusDone {
		return nil, fmt.Errorf("source task must be in status done, got %q", src.Status)
	}

	// Use the source task's title and description if not provided
	title := opts.Title
	if title == "" {
		title = src.Title
	}

	description := opts.Description
	if description == "" {
		description = src.Description
	}

	exp := Experiment{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          title,
		Description:    description,
		Status:         StatusPending,
		DefaultVariant: opts.DefaultProfile,
		SourceTaskID:   &sourceTaskID,
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&exp).Error; err != nil {
			return fmt.Errorf("create experiment: %w", err)
		}

		for _, profile := range opts.Profiles {
			taskStatus := model.StatusBacklog
			var plan model.JSONField
			if opts.ReusePlan {
				taskStatus = model.StatusPlanReview
				plan = src.Plan
			}

			task := model.Task{
				ID:          uuid.New(),
				ProjectID:   projectID,
				Title:       fmt.Sprintf("[%s] %s", profile, title),
				Description: description,
				Status:      taskStatus,
				Category:    model.CategoryStandard,
				Plan:        plan,
			}
			if err := tx.Create(&task).Error; err != nil {
				return fmt.Errorf("create task for profile %q: %w", profile, err)
			}

			variant := Variant{
				ID:           uuid.New(),
				ExperimentID: exp.ID,
				ProfileName:  profile,
				TaskID:       task.ID,
				Status:       VariantPending,
				IsDefault:    profile == opts.DefaultProfile,
				ReusesPlan:   opts.ReusePlan,
			}
			if err := tx.Create(&variant).Error; err != nil {
				return fmt.Errorf("create variant for profile %q: %w", profile, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := db.Preload("Variants").First(&exp, "id = ?", exp.ID).Error; err != nil {
		return nil, fmt.Errorf("reload experiment: %w", err)
	}
	return &exp, nil
}
