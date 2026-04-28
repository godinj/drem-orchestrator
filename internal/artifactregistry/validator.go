package artifactregistry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ValidationIssue struct {
	ArtifactID uuid.UUID
	ContentURI string
	Severity   string
	Message    string
}

type ValidationReport struct {
	Issues []ValidationIssue
}

func (r ValidationReport) ErrorCount() int {
	count := 0
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			count++
		}
	}
	return count
}

func (r ValidationReport) WarningCount() int {
	count := 0
	for _, issue := range r.Issues {
		if issue.Severity == "warning" {
			count++
		}
	}
	return count
}

func (r ValidationReport) HasErrors() bool {
	return r.ErrorCount() > 0
}

func (r *Registry) Validate(ctx context.Context) (*ValidationReport, error) {
	var artifacts []Artifact
	if err := r.db.WithContext(ctx).Find(&artifacts).Error; err != nil {
		return nil, fmt.Errorf("artifactregistry: Validate: list artifacts: %w", err)
	}
	var directives []Directive
	if err := r.db.WithContext(ctx).Find(&directives).Error; err != nil {
		return nil, fmt.Errorf("artifactregistry: Validate: list directives: %w", err)
	}
	directiveByID := make(map[uuid.UUID]Directive, len(directives))
	for _, directive := range directives {
		directiveByID[directive.ID] = directive
	}

	report := &ValidationReport{}
	for _, artifact := range artifacts {
		validateArtifact(report, artifact)
		if artifact.SupersededByID != nil {
			if err := r.assertArtifactExists(ctx, *artifact.SupersededByID); err != nil {
				report.Issues = append(report.Issues, issue(artifact, "error", "superseded_by_id points to a missing artifact"))
			}
		}
		if requiresDirectiveLink(artifact) {
			var count int64
			if err := r.db.WithContext(ctx).Model(&ArtifactDirectiveLink{}).Where("artifact_id = ?", artifact.ID).Count(&count).Error; err != nil {
				return nil, fmt.Errorf("artifactregistry: Validate: directive link count: %w", err)
			}
			if count == 0 {
				report.Issues = append(report.Issues, issue(artifact, "warning", "active authoritative artifact has no directive link"))
			}
		}
	}

	var links []ArtifactDirectiveLink
	if err := r.db.WithContext(ctx).Find(&links).Error; err != nil {
		return nil, fmt.Errorf("artifactregistry: Validate: list directive links: %w", err)
	}
	for _, link := range links {
		if _, ok := directiveByID[link.DirectiveID]; !ok {
			report.Issues = append(report.Issues, ValidationIssue{Severity: "error", Message: "artifact directive link points to a missing directive"})
		}
		if err := r.assertArtifactExists(ctx, link.ArtifactID); err != nil {
			report.Issues = append(report.Issues, ValidationIssue{Severity: "error", Message: "artifact directive link points to a missing artifact"})
		}
	}

	return report, nil
}

func validateArtifact(report *ValidationReport, artifact Artifact) {
	if artifact.ContentURI == "" {
		report.Issues = append(report.Issues, issue(artifact, "error", "content_uri is required"))
	}
	if artifact.ArtifactType == "" {
		report.Issues = append(report.Issues, issue(artifact, "error", "artifact_type is required"))
	}
	if artifact.Title == "" {
		report.Issues = append(report.Issues, issue(artifact, "error", "title is required"))
	}
	if !validArtifactStatus(artifact.Status) {
		report.Issues = append(report.Issues, issue(artifact, "error", "status is not recognized"))
	}
	if !validAuthorityClass(artifact.AuthorityClass) {
		report.Issues = append(report.Issues, issue(artifact, "error", "authority_class is not recognized"))
	}
	if !validTrust(artifact.EvidenceTrust) || !validTrust(artifact.Confidence) {
		report.Issues = append(report.Issues, issue(artifact, "error", "evidence trust or confidence is not recognized"))
	}
	if artifact.Status == StatusSuperseded && artifact.SupersededByID == nil {
		report.Issues = append(report.Issues, issue(artifact, "error", "superseded artifact must name superseded_by_id"))
	}
	if artifact.Status == StatusActive && artifact.Owner == "" {
		report.Issues = append(report.Issues, issue(artifact, "warning", "active artifact has no owner"))
	}
	if artifact.ValidUntil != nil && artifact.ValidUntil.Before(time.Now().UTC()) && artifact.Status == StatusActive {
		report.Issues = append(report.Issues, issue(artifact, "warning", "active artifact validity window has expired"))
	}
}

func (r *Registry) assertArtifactExists(ctx context.Context, id uuid.UUID) error {
	var count int64
	if err := r.db.WithContext(ctx).Model(&Artifact{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func requiresDirectiveLink(artifact Artifact) bool {
	return artifact.Status == StatusActive && (artifact.AuthorityClass == AuthoritySourceOfTruth || artifact.AuthorityClass == AuthorityDecision)
}

func issue(artifact Artifact, severity, message string) ValidationIssue {
	return ValidationIssue{ArtifactID: artifact.ID, ContentURI: artifact.ContentURI, Severity: severity, Message: message}
}

func validArtifactStatus(status ArtifactStatus) bool {
	switch status {
	case StatusCandidate, StatusDraft, StatusAccepted, StatusActive, StatusSuperseded, StatusLegacy, StatusArchived, StatusRejected, StatusScratch, StatusStale, StatusUnknown:
		return true
	default:
		return false
	}
}

func validAuthorityClass(class AuthorityClass) bool {
	switch class {
	case AuthoritySourceOfTruth, AuthorityRegistryMetadata, AuthorityDecision, AuthorityEvidence, AuthorityHistorical, AuthorityDerivedSummary, AuthorityTransient, AuthorityInadmissible:
		return true
	default:
		return false
	}
}

func validTrust(trust EvidenceTrust) bool {
	switch trust {
	case TrustHigh, TrustMedium, TrustLow, TrustUnknown, TrustInvalidated:
		return true
	default:
		return false
	}
}
