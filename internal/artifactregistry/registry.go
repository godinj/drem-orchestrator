package artifactregistry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Registry is the persistence surface for artifact authority metadata. It does
// not read or write artifact bodies; it only records whether body references are
// accepted, current, visible, and admissible.
type Registry struct {
	db *gorm.DB
	mu *sync.Mutex
}

func NewRegistry(db *gorm.DB) *Registry {
	return &Registry{db: db, mu: &sync.Mutex{}}
}

// Models returns every GORM model owned by this package.
func Models() []any {
	return []any{
		&Artifact{},
		&ArtifactLink{},
		&Directive{},
		&ArtifactDirectiveLink{},
		&ContextAdmissionDecision{},
		&ContextPacket{},
	}
}

func (r *Registry) RegisterArtifact(ctx context.Context, artifact *Artifact) error {
	if artifact == nil {
		return errors.New("artifactregistry: RegisterArtifact: artifact is nil")
	}
	if artifact.ContentURI == "" || artifact.ArtifactType == "" || artifact.Title == "" {
		return errors.New("artifactregistry: RegisterArtifact: ContentURI, ArtifactType, and Title are required")
	}
	setArtifactDefaults(artifact)
	r.mu.Lock()
	defer r.mu.Unlock()

	return withSQLiteRetry(ctx, func() error { return r.registerArtifactOnce(ctx, artifact) })
}

func (r *Registry) registerArtifactOnce(ctx context.Context, artifact *Artifact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Artifact
		err := tx.Where("content_uri = ?", artifact.ContentURI).Take(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if createErr := tx.Create(artifact).Error; createErr != nil {
				var raced Artifact
				if lookupErr := tx.Where("content_uri = ?", artifact.ContentURI).Take(&raced).Error; lookupErr == nil {
					artifact.ID = raced.ID
					if updateErr := tx.Model(&raced).Updates(artifact).Error; updateErr != nil {
						return fmt.Errorf("artifactregistry: RegisterArtifact: update raced %s: %w", artifact.ContentURI, updateErr)
					}
					return tx.Where("id = ?", raced.ID).Take(artifact).Error
				}
				return fmt.Errorf("artifactregistry: RegisterArtifact: create %s: %w", artifact.ContentURI, createErr)
			}
			return nil
		case err != nil:
			return fmt.Errorf("artifactregistry: RegisterArtifact: lookup %s: %w", artifact.ContentURI, err)
		}

		artifact.ID = existing.ID
		if err := tx.Model(&existing).Updates(artifact).Error; err != nil {
			return fmt.Errorf("artifactregistry: RegisterArtifact: update %s: %w", artifact.ContentURI, err)
		}
		return tx.Where("id = ?", existing.ID).Take(artifact).Error
	})
}

var ErrArtifactConflict = errors.New("artifact conflict")

func (r *Registry) GetArtifact(ctx context.Context, id uuid.UUID) (*Artifact, error) {
	var artifact Artifact
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, fmt.Errorf("artifactregistry: GetArtifact %s: %w", id, err)
	}
	return &artifact, nil
}

func (r *Registry) FindArtifactByURI(ctx context.Context, uri string) (*Artifact, error) {
	var artifact Artifact
	if err := r.db.WithContext(ctx).Where("content_uri = ?", uri).Take(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, fmt.Errorf("artifactregistry: FindArtifactByURI %s: %w", uri, err)
	}
	return &artifact, nil
}

type ArtifactFilter struct {
	ProjectID     string
	TaskID        string
	Persona       string
	AgentRole     string
	WorkflowStage string
	Statuses      []ArtifactStatus
	ArtifactTypes []string
	CandidateIDs  []uuid.UUID
	IncludeGlobal bool
	Limit         int
}

func (r *Registry) ListArtifacts(ctx context.Context, filter ArtifactFilter) ([]Artifact, error) {
	q := r.db.WithContext(ctx).Model(&Artifact{})
	if len(filter.CandidateIDs) > 0 {
		q = q.Where("id IN ?", filter.CandidateIDs)
	}
	if len(filter.Statuses) > 0 {
		q = q.Where("status IN ?", filter.Statuses)
	}
	if len(filter.ArtifactTypes) > 0 {
		q = q.Where("artifact_type IN ?", filter.ArtifactTypes)
	}
	if len(filter.CandidateIDs) > 0 {
		var artifacts []Artifact
		if err := q.Order("updated_at DESC").Find(&artifacts).Error; err != nil {
			return nil, fmt.Errorf("artifactregistry: ListArtifacts: %w", err)
		}
		return artifacts, nil
	}
	if filter.ProjectID != "" {
		if filter.IncludeGlobal {
			q = q.Where("project_id = ? OR project_id = ''", filter.ProjectID)
		} else {
			q = q.Where("project_id = ?", filter.ProjectID)
		}
	}
	if filter.TaskID != "" {
		if filter.IncludeGlobal {
			q = q.Where("task_id = ? OR task_id = ''", filter.TaskID)
		} else {
			q = q.Where("task_id = ?", filter.TaskID)
		}
	}
	if filter.Persona != "" {
		q = q.Where("persona_scope IN ?", []string{"", "all", filter.Persona})
	}
	if filter.AgentRole != "" {
		q = q.Where("agent_role_scope IN ?", []string{"", "all", filter.AgentRole})
	}
	if filter.WorkflowStage != "" {
		q = q.Where("workflow_scope IN ?", []string{"", "all", filter.WorkflowStage})
	}
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}

	var artifacts []Artifact
	if err := q.Order("updated_at DESC").Find(&artifacts).Error; err != nil {
		return nil, fmt.Errorf("artifactregistry: ListArtifacts: %w", err)
	}
	return artifacts, nil
}

func (r *Registry) RegisterDirective(ctx context.Context, directive *Directive) error {
	if directive == nil {
		return errors.New("artifactregistry: RegisterDirective: directive is nil")
	}
	if directive.DirectiveType == "" || directive.Title == "" {
		return errors.New("artifactregistry: RegisterDirective: DirectiveType and Title are required")
	}
	if directive.ID == uuid.Nil {
		directive.ID = uuid.New()
	}
	if directive.Status == "" {
		directive.Status = DirectiveActive
	}
	if err := r.db.WithContext(ctx).Save(directive).Error; err != nil {
		return fmt.Errorf("artifactregistry: RegisterDirective %s: %w", directive.Title, err)
	}
	return nil
}

func (r *Registry) LinkArtifactDirective(ctx context.Context, link *ArtifactDirectiveLink) error {
	if link == nil {
		return errors.New("artifactregistry: LinkArtifactDirective: link is nil")
	}
	if link.ArtifactID == uuid.Nil || link.DirectiveID == uuid.Nil || link.LinkType == "" {
		return errors.New("artifactregistry: LinkArtifactDirective: ArtifactID, DirectiveID, and LinkType are required")
	}
	if link.ID == uuid.Nil {
		link.ID = uuid.New()
	}
	if link.Confidence == "" {
		link.Confidence = TrustUnknown
	}
	if err := r.db.WithContext(ctx).Create(link).Error; err != nil {
		return fmt.Errorf("artifactregistry: LinkArtifactDirective: %w", err)
	}
	return nil
}

func (r *Registry) Supersede(ctx context.Context, supersededID, supersedingID uuid.UUID, reason, decidedBy string) error {
	if supersededID == uuid.Nil || supersedingID == uuid.Nil {
		return errors.New("artifactregistry: Supersede: both artifact IDs are required")
	}
	if supersededID == supersedingID {
		return errors.New("artifactregistry: Supersede: artifact cannot supersede itself")
	}
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	return withSQLiteRetry(ctx, func() error { return r.supersedeOnce(ctx, supersededID, supersedingID, reason, decidedBy, now) })
}

func (r *Registry) supersedeOnce(ctx context.Context, supersededID, supersedingID uuid.UUID, reason, decidedBy string, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Artifact{}).Where("id = ? AND superseded_by_id IS NULL", supersededID).Updates(map[string]any{
			"status":            StatusSuperseded,
			"superseded_by_id":  supersedingID,
			"staleness_reason":  reason,
			"last_validated_at": now,
		})
		if res.Error != nil {
			return fmt.Errorf("artifactregistry: Supersede: update superseded: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			var existing Artifact
			if err := tx.Where("id = ?", supersededID).Take(&existing).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return gorm.ErrRecordNotFound
				}
				return fmt.Errorf("artifactregistry: Supersede: reload superseded: %w", err)
			}
			if existing.SupersededByID != nil && *existing.SupersededByID != supersedingID {
				return fmt.Errorf("%w: artifact %s already superseded by %s", ErrArtifactConflict, supersededID, existing.SupersededByID.String())
			}
			if existing.SupersededByID != nil && *existing.SupersededByID == supersedingID {
				return nil
			}
			return gorm.ErrRecordNotFound
		}
		res = tx.Model(&Artifact{}).Where("id = ?", supersedingID).Update("supersedes_id", supersededID)
		if res.Error != nil {
			return fmt.Errorf("artifactregistry: Supersede: update superseding: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		link := ArtifactLink{
			ID:               uuid.New(),
			SourceArtifactID: supersedingID,
			TargetArtifactID: supersededID,
			LinkType:         "supersedes",
			Reason:           reason,
			Confidence:       TrustHigh,
			CreatedAt:        now,
		}
		if decidedBy != "" {
			link.Reason = fmt.Sprintf("%s (decided by %s)", reason, decidedBy)
		}
		if err := tx.Create(&link).Error; err != nil {
			return fmt.Errorf("artifactregistry: Supersede: create link: %w", err)
		}
		return nil
	})
}

func withSQLiteRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = fn()
		if !isSQLiteLocked(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
	return err
}

func isSQLiteLocked(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database table is locked") || strings.Contains(msg, "database is locked")
}

func setArtifactDefaults(artifact *Artifact) {
	if artifact.ID == uuid.Nil {
		artifact.ID = uuid.New()
	}
	if artifact.Status == "" {
		artifact.Status = StatusUnknown
	}
	if artifact.AuthorityClass == "" {
		artifact.AuthorityClass = AuthorityTransient
	}
	if artifact.Admissibility == "" {
		artifact.Admissibility = "unknown"
	}
	if artifact.EvidenceTrust == "" {
		artifact.EvidenceTrust = TrustUnknown
	}
	if artifact.Confidence == "" {
		artifact.Confidence = TrustUnknown
	}
	if artifact.ValidationStatus == "" {
		artifact.ValidationStatus = ValidationUnknown
	}
}
