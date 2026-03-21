// Package bugreport provides the core service for ingesting, managing, and
// promoting agent-filed bug reports.
package bugreport

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// validTransitions defines the allowed bug report status transitions.
// Terminal states (promoted, dismissed) have no outbound transitions.
var validTransitions = map[model.BugReportStatus][]model.BugReportStatus{
	model.BugStatusOpen:         {model.BugStatusAcknowledged, model.BugStatusPromoted, model.BugStatusDismissed},
	model.BugStatusAcknowledged: {model.BugStatusPromoted, model.BugStatusDismissed},
	model.BugStatusPromoted:     {},
	model.BugStatusDismissed:    {},
}

// ListFilters specifies optional filters for listing bug reports.
type ListFilters struct {
	Category  *model.BugReportCategory
	Severity  *model.BugReportSeverity
	Status    *model.BugReportStatus
	ProjectID *uuid.UUID
}

// Service manages bug report lifecycle operations.
type Service struct {
	db *gorm.DB
}

// New creates a Service.
func New(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Create inserts a new bug report. Title and Description are required.
func (s *Service) Create(report *model.BugReport) error {
	if report.Title == "" {
		return fmt.Errorf("create bug report: title is required")
	}
	if report.Description == "" {
		return fmt.Errorf("create bug report: description is required")
	}
	if report.Status == "" {
		report.Status = model.BugStatusOpen
	}
	now := time.Now()
	if report.CreatedAt.IsZero() {
		report.CreatedAt = now
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = now
	}
	if err := s.db.Create(report).Error; err != nil {
		return fmt.Errorf("create bug report: %w", err)
	}
	return nil
}

// Get retrieves a bug report by ID with preloaded comments.
func (s *Service) Get(id uuid.UUID) (*model.BugReport, error) {
	var report model.BugReport
	err := s.db.Preload("Comments").First(&report, "id = ?", id).Error
	if err != nil {
		return nil, fmt.Errorf("get bug report %s: %w", id, err)
	}
	return &report, nil
}

// List returns bug reports matching the given filters.
func (s *Service) List(filters ListFilters) ([]model.BugReport, error) {
	q := s.db.Model(&model.BugReport{})
	if filters.Category != nil {
		q = q.Where("category = ?", *filters.Category)
	}
	if filters.Severity != nil {
		q = q.Where("severity = ?", *filters.Severity)
	}
	if filters.Status != nil {
		q = q.Where("status = ?", *filters.Status)
	}
	if filters.ProjectID != nil {
		q = q.Where("project_id = ?", *filters.ProjectID)
	}
	var reports []model.BugReport
	if err := q.Order("created_at DESC").Find(&reports).Error; err != nil {
		return nil, fmt.Errorf("list bug reports: %w", err)
	}
	return reports, nil
}

// validateTransition checks whether moving from current to target is allowed.
func validateTransition(current, target model.BugReportStatus) error {
	allowed, ok := validTransitions[current]
	if !ok {
		return fmt.Errorf("unknown bug report status: %q", current)
	}
	for _, s := range allowed {
		if s == target {
			return nil
		}
	}
	return fmt.Errorf("invalid transition from %q to %q", current, target)
}

// transition loads a bug report, validates the status change, and persists it.
func (s *Service) transition(id uuid.UUID, target model.BugReportStatus) (*model.BugReport, error) {
	var report model.BugReport
	if err := s.db.First(&report, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("transition bug report %s: %w", id, err)
	}
	if err := validateTransition(report.Status, target); err != nil {
		return nil, fmt.Errorf("transition bug report %s: %w", id, err)
	}
	report.Status = target
	report.UpdatedAt = time.Now()
	if err := s.db.Save(&report).Error; err != nil {
		return nil, fmt.Errorf("transition bug report %s: %w", id, err)
	}
	return &report, nil
}

// Acknowledge transitions a bug report from open to acknowledged.
func (s *Service) Acknowledge(id uuid.UUID) error {
	_, err := s.transition(id, model.BugStatusAcknowledged)
	return err
}

// Dismiss transitions a bug report to dismissed.
func (s *Service) Dismiss(id uuid.UUID) error {
	_, err := s.transition(id, model.BugStatusDismissed)
	return err
}

// Promote transitions a bug report to promoted, creating a new Task in backlog
// status. Returns the created task. The caller opens $EDITOR -- this function
// only handles DB operations.
func (s *Service) Promote(id uuid.UUID, taskTitle, taskDescription string, projectID uuid.UUID) (*model.Task, error) {
	var task model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 1. Load and validate the bug report.
		var report model.BugReport
		if err := tx.First(&report, "id = ?", id).Error; err != nil {
			return fmt.Errorf("load bug report %s: %w", id, err)
		}
		if err := validateTransition(report.Status, model.BugStatusPromoted); err != nil {
			return fmt.Errorf("promote bug report %s: %w", id, err)
		}

		// 2. Create a new task in backlog status.
		now := time.Now()
		task = model.Task{
			ID:          uuid.New(),
			ProjectID:   projectID,
			Title:       taskTitle,
			Description: taskDescription,
			Status:      model.StatusBacklog,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(&task).Error; err != nil {
			return fmt.Errorf("create promoted task: %w", err)
		}

		// 3. Update the bug report.
		report.Status = model.BugStatusPromoted
		report.PromotedTaskID = &task.ID
		report.UpdatedAt = now
		if err := tx.Save(&report).Error; err != nil {
			return fmt.Errorf("update bug report %s: %w", id, err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("promote bug report: %w", err)
	}
	return &task, nil
}

// Delete permanently removes a bug report and its comments from the database.
func (s *Service) Delete(id uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Verify the bug report exists.
		var report model.BugReport
		if err := tx.First(&report, "id = ?", id).Error; err != nil {
			return fmt.Errorf("delete bug report %s: %w", id, err)
		}
		// Delete comments first (cascade).
		if err := tx.Where("bug_report_id = ?", id).Delete(&model.BugReportComment{}).Error; err != nil {
			return fmt.Errorf("delete bug report %s comments: %w", id, err)
		}
		// Delete the bug report itself.
		if err := tx.Delete(&report).Error; err != nil {
			return fmt.Errorf("delete bug report %s: %w", id, err)
		}
		return nil
	})
}

// AddComment adds a comment to a bug report.
func (s *Service) AddComment(bugReportID uuid.UUID, author, body string) error {
	// Verify the bug report exists.
	var report model.BugReport
	if err := s.db.First(&report, "id = ?", bugReportID).Error; err != nil {
		return fmt.Errorf("add comment to bug report %s: %w", bugReportID, err)
	}
	comment := model.BugReportComment{
		ID:          uuid.New(),
		BugReportID: bugReportID,
		Author:      author,
		Body:        body,
		CreatedAt:   time.Now(),
	}
	if err := s.db.Create(&comment).Error; err != nil {
		return fmt.Errorf("add comment to bug report %s: %w", bugReportID, err)
	}
	return nil
}

// GetComments returns comments for a bug report in chronological order.
func (s *Service) GetComments(bugReportID uuid.UUID) ([]model.BugReportComment, error) {
	// Verify the bug report exists.
	var report model.BugReport
	if err := s.db.First(&report, "id = ?", bugReportID).Error; err != nil {
		return nil, fmt.Errorf("get comments for bug report %s: %w", bugReportID, err)
	}
	var comments []model.BugReportComment
	err := s.db.Where("bug_report_id = ?", bugReportID).
		Order("created_at ASC").
		Find(&comments).Error
	if err != nil {
		return nil, fmt.Errorf("get comments for bug report %s: %w", bugReportID, err)
	}
	return comments, nil
}
