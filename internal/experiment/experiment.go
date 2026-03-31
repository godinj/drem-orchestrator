// Package experiment provides the Experiment and Variant GORM models along
// with creation logic for A/B-style profile experiments. An experiment runs
// the same task description across 2-3 agent profiles so the user can compare
// outputs.
package experiment

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// ExperimentStatus represents the lifecycle state of an experiment.
type ExperimentStatus string

const (
	StatusPending   ExperimentStatus = "pending"
	StatusRunning   ExperimentStatus = "running"
	StatusReview    ExperimentStatus = "review"
	StatusCompleted ExperimentStatus = "completed"
)

// VariantStatus represents the lifecycle state of a variant.
type VariantStatus string

const (
	VariantPending VariantStatus = "pending"
)

// Experiment represents an A/B comparison of agent profiles on a shared task
// description.
type Experiment struct {
	ID             uuid.UUID `gorm:"type:text;primaryKey"`
	ProjectID      uuid.UUID `gorm:"type:text;not null;index"`
	Title          string    `gorm:"not null"`
	Description    string
	Status         ExperimentStatus `gorm:"not null;default:pending"`
	DefaultVariant string           // profile name of the default variant
	SourceTaskID   *uuid.UUID       `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Variants       []Variant `gorm:"foreignKey:ExperimentID"`
}

// Variant represents a single profile arm within an experiment. Each variant
// owns a Task that runs under the given profile.
type Variant struct {
	ID           uuid.UUID     `gorm:"type:text;primaryKey"`
	ExperimentID uuid.UUID     `gorm:"type:text;not null;index"`
	ProfileName  string        `gorm:"not null"`
	TaskID       uuid.UUID     `gorm:"type:text"`
	Status       VariantStatus `gorm:"not null;default:pending"`
	IsDefault    bool          `gorm:"default:false"`
	ReusesPlan   bool          `gorm:"default:false"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// minProfiles is the minimum number of profiles required for an experiment.
const minProfiles = 2

// maxProfiles is the maximum number of profiles allowed for an experiment.
const maxProfiles = 3

// CreateExperiment validates inputs, creates an Experiment with one Variant
// and one Task per profile, and returns the experiment with variants loaded.
// It requires 2-3 profiles and the defaultProfile must be in the list.
func CreateExperiment(db *gorm.DB, projectID uuid.UUID, title, description string, profiles []string, defaultProfile string) (*Experiment, error) {
	if len(profiles) < minProfiles {
		return nil, fmt.Errorf("experiment requires at least %d profiles, got %d", minProfiles, len(profiles))
	}
	if len(profiles) > maxProfiles {
		return nil, fmt.Errorf("experiment allows at most %d profiles, got %d", maxProfiles, len(profiles))
	}
	if !containsProfile(profiles, defaultProfile) {
		return nil, fmt.Errorf("default profile %q not found in profiles %v", defaultProfile, profiles)
	}

	exp := Experiment{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          title,
		Description:    description,
		Status:         StatusPending,
		DefaultVariant: defaultProfile,
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&exp).Error; err != nil {
			return fmt.Errorf("create experiment: %w", err)
		}

		for _, profile := range profiles {
			task := model.Task{
				ID:          uuid.New(),
				ProjectID:   projectID,
				Title:       fmt.Sprintf("[%s] %s", profile, title),
				Description: description,
				Status:      model.StatusBacklog,
				Category:    model.CategoryStandard,
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
				IsDefault:    profile == defaultProfile,
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

	// Reload with variants.
	if err := db.Preload("Variants").First(&exp, "id = ?", exp.ID).Error; err != nil {
		return nil, fmt.Errorf("reload experiment: %w", err)
	}
	return &exp, nil
}

// CreateFromTask creates an experiment derived from an existing done task.
// The source task must be in StatusDone. Each variant gets a new task that
// either copies the source plan (reusesPlan=true, tasks start at
// StatusPlanReview) or starts fresh (reusesPlan=false, tasks start at
// StatusBacklog). The experiment's SourceTaskID is set to sourceTaskID.
//
// NOTE: This is a stub. The real implementation is pending.
func CreateFromTask(db *gorm.DB, projectID uuid.UUID, sourceTaskID uuid.UUID, title, description string, profiles []string, defaultProfile string, reusesPlan bool) (*Experiment, error) {
	return nil, fmt.Errorf("CreateFromTask: not implemented")
}

// containsProfile returns true if needle is present in the profiles slice.
func containsProfile(profiles []string, needle string) bool {
	for _, p := range profiles {
		if p == needle {
			return true
		}
	}
	return false
}
