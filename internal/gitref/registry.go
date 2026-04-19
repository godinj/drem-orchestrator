package gitref

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// Registry is the CRUD surface for BranchRef records. It owns no filesystem
// state and performs no git operations; it is a thin projection over gorm.DB
// so that orchestrator code has a single place to register, transition, and
// look up tracked branches.
type Registry struct {
	db *gorm.DB
}

// NewRegistry constructs a Registry backed by the given database handle. The
// caller is responsible for running AutoMigrate on *BranchRef before using
// the registry — the production path does this in internal/db, and tests use
// testutil.NewTestDBWithModels.
func NewRegistry(db *gorm.DB) *Registry {
	return &Registry{db: db}
}

// Register upserts a BranchRef keyed by (BareRepoPath, Branch). If an active
// row already exists for that key, Register returns a descriptive error so
// the caller can distinguish "branch already claimed" from generic DB errors.
// If the existing row is in a terminal status (merged or deleted), Register
// revives it by updating the caller-supplied fields and resetting Status to
// active. On successful upsert ref.ID is populated.
func (r *Registry) Register(ctx context.Context, ref *BranchRef) error {
	if ref == nil {
		return errors.New("gitref: Register: ref is nil")
	}
	if ref.BareRepoPath == "" || ref.Branch == "" {
		return fmt.Errorf("gitref: Register: BareRepoPath and Branch are required")
	}
	if ref.Status == "" {
		ref.Status = StatusActive
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing BranchRef
		err := tx.Where("bare_repo_path = ? AND branch = ?", ref.BareRepoPath, ref.Branch).
			Take(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if createErr := tx.Create(ref).Error; createErr != nil {
				return fmt.Errorf("gitref: Register: create %s@%s: %w",
					ref.Branch, ref.BareRepoPath, createErr)
			}
			return nil
		case err != nil:
			return fmt.Errorf("gitref: Register: lookup %s@%s: %w",
				ref.Branch, ref.BareRepoPath, err)
		}

		if existing.Status == StatusActive {
			return fmt.Errorf("gitref: Register: branch %q already active for bare repo %q",
				ref.Branch, ref.BareRepoPath)
		}

		// Revive the terminal row with the caller-supplied metadata.
		existing.Project = ref.Project
		existing.TaskID = ref.TaskID
		existing.AgentType = ref.AgentType
		existing.Status = ref.Status
		if saveErr := tx.Save(&existing).Error; saveErr != nil {
			return fmt.Errorf("gitref: Register: revive %s@%s: %w",
				ref.Branch, ref.BareRepoPath, saveErr)
		}
		*ref = existing
		return nil
	})
}

// MarkMerged transitions the referenced row to StatusMerged.
func (r *Registry) MarkMerged(ctx context.Context, id uint) error {
	return r.updateStatus(ctx, id, StatusMerged)
}

// MarkDeleted transitions the referenced row to StatusDeleted.
func (r *Registry) MarkDeleted(ctx context.Context, id uint) error {
	return r.updateStatus(ctx, id, StatusDeleted)
}

// updateStatus performs the shared status-mutation path used by MarkMerged
// and MarkDeleted. It returns gorm.ErrRecordNotFound if no row matches id so
// callers can distinguish "no such ref" from write errors.
func (r *Registry) updateStatus(ctx context.Context, id uint, status Status) error {
	res := r.db.WithContext(ctx).
		Model(&BranchRef{}).
		Where("id = ?", id).
		Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("gitref: update status id=%d to %s: %w", id, status, res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Get returns the BranchRef with the given primary-key id. A not-found
// result is returned as gorm.ErrRecordNotFound so callers can use
// errors.Is for detection.
func (r *Registry) Get(ctx context.Context, id uint) (*BranchRef, error) {
	var ref BranchRef
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&ref).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, fmt.Errorf("gitref: Get id=%d: %w", id, err)
	}
	return &ref, nil
}

// FindByBranch returns the BranchRef for (bareRepo, branch) if one exists.
// A not-found result is returned as gorm.ErrRecordNotFound.
func (r *Registry) FindByBranch(ctx context.Context, bareRepo, branch string) (*BranchRef, error) {
	var ref BranchRef
	err := r.db.WithContext(ctx).
		Where("bare_repo_path = ? AND branch = ?", bareRepo, branch).
		Take(&ref).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, fmt.Errorf("gitref: FindByBranch %s@%s: %w", branch, bareRepo, err)
	}
	return &ref, nil
}

// ListByProject returns every BranchRef for the given project, optionally
// filtered by status. Passing an empty status returns rows in all states.
// Results are ordered by CreatedAt ascending for stable iteration.
func (r *Registry) ListByProject(ctx context.Context, project string, status Status) ([]BranchRef, error) {
	results := make([]BranchRef, 0)
	q := r.db.WithContext(ctx).Where("project = ?", project)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("created_at ASC").Find(&results).Error; err != nil {
		return results, fmt.Errorf("gitref: ListByProject %s: %w", project, err)
	}
	return results, nil
}
