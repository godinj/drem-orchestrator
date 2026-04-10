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
	StatusCancelled ExperimentStatus = "cancelled"
)

// VariantStatus represents the lifecycle state of a variant.
type VariantStatus string

const (
	VariantPending VariantStatus = "pending"
	VariantRunning VariantStatus = "running"
	VariantPassed  VariantStatus = "passed"
	VariantFailed  VariantStatus = "failed"
	VariantWinner  VariantStatus = "winner"
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
func CreateFromTask(db *gorm.DB, projectID uuid.UUID, sourceTaskID uuid.UUID, title, description string, profiles []string, defaultProfile string, reusesPlan bool) (*Experiment, error) {
	if len(profiles) < minProfiles {
		return nil, fmt.Errorf("experiment requires at least %d profiles, got %d", minProfiles, len(profiles))
	}
	if len(profiles) > maxProfiles {
		return nil, fmt.Errorf("experiment allows at most %d profiles, got %d", maxProfiles, len(profiles))
	}
	if !containsProfile(profiles, defaultProfile) {
		return nil, fmt.Errorf("default profile %q not found in profiles %v", defaultProfile, profiles)
	}

	var src model.Task
	if err := db.First(&src, "id = ?", sourceTaskID).Error; err != nil {
		return nil, fmt.Errorf("load source task: %w", err)
	}
	if src.Status != model.StatusDone {
		return nil, fmt.Errorf("source task must be in status done, got %q", src.Status)
	}

	exp := Experiment{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Title:          title,
		Description:    description,
		Status:         StatusPending,
		DefaultVariant: defaultProfile,
		SourceTaskID:   &sourceTaskID,
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&exp).Error; err != nil {
			return fmt.Errorf("create experiment: %w", err)
		}

		for _, profile := range profiles {
			taskStatus := model.StatusBacklog
			var plan model.JSONField
			if reusesPlan {
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
				IsDefault:    profile == defaultProfile,
				ReusesPlan:   reusesPlan,
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

// containsProfile returns true if needle is present in the profiles slice.
func containsProfile(profiles []string, needle string) bool {
	for _, p := range profiles {
		if p == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Experiment Status Transitions
// ---------------------------------------------------------------------------

// StartExperiment sets the experiment status to "running" and all variant statuses to "running".
// Returns an error if the experiment is not in "pending" status.
func StartExperiment(db *gorm.DB, experimentID uuid.UUID) error {
	var exp Experiment
	if err := db.First(&exp, "id = ?", experimentID).Error; err != nil {
		return fmt.Errorf("load experiment: %w", err)
	}
	if exp.Status != StatusPending {
		return fmt.Errorf("experiment must be in status pending, got %q", exp.Status)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Experiment{}).Where("id = ?", experimentID).Update("status", StatusRunning).Error; err != nil {
			return fmt.Errorf("update experiment status: %w", err)
		}
		if err := tx.Model(&Variant{}).Where("experiment_id = ?", experimentID).Update("status", VariantRunning).Error; err != nil {
			return fmt.Errorf("update variant statuses: %w", err)
		}
		return nil
	})
}

// MoveToReview sets the experiment status to "review".
// All variants must be in a terminal state (passed, failed, or winner).
// Returns an error if any variant is still running or pending.
func MoveToReview(db *gorm.DB, experimentID uuid.UUID) error {
	var exp Experiment
	if err := db.Preload("Variants").First(&exp, "id = ?", experimentID).Error; err != nil {
		return fmt.Errorf("load experiment: %w", err)
	}

	for _, v := range exp.Variants {
		if v.Status == VariantPending || v.Status == VariantRunning {
			return fmt.Errorf("variant %s is still %q, all variants must be in terminal state", v.ProfileName, v.Status)
		}
	}

	return db.Model(&Experiment{}).Where("id = ?", experimentID).Update("status", StatusReview).Error
}

// CompleteExperiment sets the experiment status to "completed".
// Returns an error if the experiment is not in "review" status.
func CompleteExperiment(db *gorm.DB, experimentID uuid.UUID) error {
	var exp Experiment
	if err := db.First(&exp, "id = ?", experimentID).Error; err != nil {
		return fmt.Errorf("load experiment: %w", err)
	}
	if exp.Status != StatusReview {
		return fmt.Errorf("experiment must be in status review, got %q", exp.Status)
	}

	return db.Model(&Experiment{}).Where("id = ?", experimentID).Update("status", StatusCompleted).Error
}

// CancelExperiment sets the experiment status to "cancelled" from any non-completed state.
// Also sets all variant statuses to "failed".
func CancelExperiment(db *gorm.DB, experimentID uuid.UUID) error {
	var exp Experiment
	if err := db.First(&exp, "id = ?", experimentID).Error; err != nil {
		return fmt.Errorf("load experiment: %w", err)
	}
	if exp.Status == StatusCompleted {
		return fmt.Errorf("cannot cancel a completed experiment")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Experiment{}).Where("id = ?", experimentID).Update("status", StatusCancelled).Error; err != nil {
			return fmt.Errorf("update experiment status: %w", err)
		}
		if err := tx.Model(&Variant{}).Where("experiment_id = ?", experimentID).Update("status", VariantFailed).Error; err != nil {
			return fmt.Errorf("update variant statuses: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Variant Status Transitions
// ---------------------------------------------------------------------------

// PassVariant marks a variant as "passed".
// Returns an error if the variant is already in a terminal state.
func PassVariant(db *gorm.DB, variantID uuid.UUID) error {
	var v Variant
	if err := db.First(&v, "id = ?", variantID).Error; err != nil {
		return fmt.Errorf("load variant: %w", err)
	}
	if v.Status == VariantPassed || v.Status == VariantFailed || v.Status == VariantWinner {
		return fmt.Errorf("variant already in terminal state %q", v.Status)
	}

	return db.Model(&Variant{}).Where("id = ?", variantID).Update("status", VariantPassed).Error
}

// FailVariant marks a variant as "failed".
// Returns an error if the variant is already in a terminal state.
func FailVariant(db *gorm.DB, variantID uuid.UUID) error {
	var v Variant
	if err := db.First(&v, "id = ?", variantID).Error; err != nil {
		return fmt.Errorf("load variant: %w", err)
	}
	if v.Status == VariantPassed || v.Status == VariantFailed || v.Status == VariantWinner {
		return fmt.Errorf("variant already in terminal state %q", v.Status)
	}

	return db.Model(&Variant{}).Where("id = ?", variantID).Update("status", VariantFailed).Error
}

// PromoteVariant marks a variant as "winner".
// Returns an error if the variant is not in "passed" status.
func PromoteVariant(db *gorm.DB, variantID uuid.UUID) error {
	var v Variant
	if err := db.First(&v, "id = ?", variantID).Error; err != nil {
		return fmt.Errorf("load variant: %w", err)
	}
	if v.Status != VariantPassed {
		return fmt.Errorf("variant must be in status passed, got %q", v.Status)
	}

	return db.Model(&Variant{}).Where("id = ?", variantID).Update("status", VariantWinner).Error
}

// ---------------------------------------------------------------------------
// Query Helpers
// ---------------------------------------------------------------------------

// ListActiveExperiments returns all experiments with status "running".
func ListActiveExperiments(db *gorm.DB) ([]Experiment, error) {
	var experiments []Experiment
	if err := db.Preload("Variants").Where("status = ?", StatusRunning).Find(&experiments).Error; err != nil {
		return nil, fmt.Errorf("list active experiments: %w", err)
	}
	return experiments, nil
}

// GetExperimentByID returns an experiment with preloaded variants.
func GetExperimentByID(db *gorm.DB, id uuid.UUID) (*Experiment, error) {
	var exp Experiment
	if err := db.Preload("Variants").First(&exp, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("get experiment: %w", err)
	}
	return &exp, nil
}

// GetVariantByTaskID finds the variant associated with a given task ID.
func GetVariantByTaskID(db *gorm.DB, taskID uuid.UUID) (*Variant, error) {
	var v Variant
	if err := db.First(&v, "task_id = ?", taskID).Error; err != nil {
		return nil, fmt.Errorf("get variant by task ID: %w", err)
	}
	return &v, nil
}

// CountActiveExperiments returns the count of experiments with status "running".
func CountActiveExperiments(db *gorm.DB) (int64, error) {
	var count int64
	if err := db.Model(&Experiment{}).Where("status = ?", StatusRunning).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count active experiments: %w", err)
	}
	return count, nil
}
