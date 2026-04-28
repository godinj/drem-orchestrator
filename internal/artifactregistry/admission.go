package artifactregistry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/google/uuid"
)

type AdmissionRequest struct {
	ProjectID              string
	TaskID                 string
	Persona                string
	AgentRole              string
	WorkflowStage          string
	ActiveDirectiveIDs     []uuid.UUID
	CandidateArtifactIDs   []uuid.UUID
	RequiredArtifactTypes  []string
	EvidenceTrustThreshold EvidenceTrust
	ReportOnly             bool
}

type AdmissionResult struct {
	Packet             ContextPacket
	Decisions          []ContextAdmissionDecision
	Admitted           []Artifact
	Excluded           []Artifact
	Historical         []Artifact
	WeakEvidence       []Artifact
	EscalationRequired bool
}

func (r *Registry) AdmitContext(ctx context.Context, req AdmissionRequest) (*AdmissionResult, error) {
	filter := ArtifactFilter{
		ProjectID:     req.ProjectID,
		TaskID:        req.TaskID,
		Persona:       req.Persona,
		AgentRole:     req.AgentRole,
		WorkflowStage: req.WorkflowStage,
		CandidateIDs:  req.CandidateArtifactIDs,
		IncludeGlobal: true,
		Limit:         250,
	}
	artifacts, err := r.ListArtifacts(ctx, filter)
	if err != nil {
		return nil, err
	}
	return r.AdmitArtifacts(ctx, req, artifacts)
}

func (r *Registry) AdmitArtifacts(ctx context.Context, req AdmissionRequest, artifacts []Artifact) (*AdmissionResult, error) {
	packet := ContextPacket{
		ID:               uuid.New(),
		TaskID:           req.TaskID,
		ProjectID:        req.ProjectID,
		Persona:          req.Persona,
		AgentRole:        req.AgentRole,
		WorkflowStage:    req.WorkflowStage,
		ActiveDirectives: directiveIDStrings(req.ActiveDirectiveIDs),
		Summary:          "artifact registry context admission",
		Metadata:         model.JSONField{"report_only": req.ReportOnly},
		CreatedAt:        time.Now().UTC(),
	}
	result := &AdmissionResult{Packet: packet}
	directiveScores, err := r.directiveScores(ctx, req.ActiveDirectiveIDs)
	if err != nil {
		return nil, err
	}
	requiredTypes := stringSet(req.RequiredArtifactTypes)
	threshold := trustRank(req.EvidenceTrustThreshold)
	if threshold == 0 {
		threshold = trustRank(TrustLow)
	}

	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifacts[i].UpdatedAt.After(artifacts[j].UpdatedAt)
	})

	for _, artifact := range artifacts {
		decision, reason := decideAdmission(req, artifact, directiveScores, requiredTypes, threshold)
		record := ContextAdmissionDecision{
			ID:                 uuid.New(),
			ContextPacketID:    packet.ID,
			TaskID:             req.TaskID,
			ProjectID:          req.ProjectID,
			Persona:            req.Persona,
			AgentRole:          req.AgentRole,
			WorkflowStage:      req.WorkflowStage,
			ArtifactID:         artifact.ID,
			Decision:           decision,
			Reason:             reason,
			TrustAtAdmission:   artifact.EvidenceTrust,
			GoalRelevanceScore: directiveScores[artifact.ID],
			Metadata: model.JSONField{
				"report_only":   req.ReportOnly,
				"artifact_type": artifact.ArtifactType,
				"content_uri":   artifact.ContentURI,
			},
			CreatedAt: packet.CreatedAt,
		}
		result.Decisions = append(result.Decisions, record)
		switch decision {
		case DecisionAdmit, DecisionAdmitMinimal:
			result.Admitted = append(result.Admitted, artifact)
		case DecisionAdmitHistorical:
			result.Historical = append(result.Historical, artifact)
		case DecisionAdmitWeakEvidence:
			result.WeakEvidence = append(result.WeakEvidence, artifact)
		case DecisionEscalateConflict:
			result.EscalationRequired = true
			result.Excluded = append(result.Excluded, artifact)
		default:
			result.Excluded = append(result.Excluded, artifact)
		}
	}

	if err := r.db.WithContext(ctx).Create(&packet).Error; err != nil {
		return nil, fmt.Errorf("artifactregistry: AdmitArtifacts: create packet: %w", err)
	}
	if len(result.Decisions) > 0 {
		if err := r.db.WithContext(ctx).Create(&result.Decisions).Error; err != nil {
			return nil, fmt.Errorf("artifactregistry: AdmitArtifacts: create decisions: %w", err)
		}
	}
	return result, nil
}

func decideAdmission(req AdmissionRequest, artifact Artifact, directiveScores map[uuid.UUID]int, requiredTypes map[string]bool, threshold int) (AdmissionDecision, string) {
	now := time.Now().UTC()
	if artifact.ValidUntil != nil && artifact.ValidUntil.Before(now) {
		return DecisionExcludeStale, "artifact validity window has expired"
	}
	if artifact.Status == StatusSuperseded {
		return DecisionExcludeSuperseded, "artifact is superseded"
	}
	if artifact.Status == StatusStale {
		return DecisionExcludeStale, "artifact is stale"
	}
	if artifact.Status == StatusRejected || artifact.Status == StatusScratch || artifact.Status == StatusUnknown || artifact.Status == StatusCandidate {
		return DecisionExcludeInadmissible, "artifact status is not admissible for authoritative context"
	}
	if artifact.Status == StatusLegacy || artifact.Status == StatusArchived {
		return DecisionAdmitHistorical, "artifact is historical context only"
	}
	if artifact.AuthorityClass == AuthorityInadmissible || artifact.EvidenceTrust == TrustInvalidated {
		return DecisionExcludeInadmissible, "artifact is marked inadmissible or invalidated"
	}
	if !scopeMatches(artifact.PersonaScope, req.Persona) || !scopeMatches(artifact.AgentRoleScope, req.AgentRole) || !scopeMatches(artifact.WorkflowScope, req.WorkflowStage) {
		return DecisionExcludeVisibility, "artifact scope does not match this persona, role, or workflow"
	}
	if artifact.ProjectID != "" && req.ProjectID != "" && artifact.ProjectID != req.ProjectID {
		return DecisionExcludeIrrelevant, "artifact belongs to another project"
	}
	if artifact.TaskID != "" && req.TaskID != "" && artifact.TaskID != req.TaskID {
		return DecisionExcludeIrrelevant, "artifact belongs to another task"
	}
	if strings.EqualFold(artifact.Admissibility, "inadmissible") {
		return DecisionExcludeInadmissible, "artifact admissibility is inadmissible"
	}
	if strings.EqualFold(artifact.Admissibility, "historical") {
		return DecisionAdmitHistorical, "artifact is explicitly historical"
	}
	if strings.EqualFold(artifact.Admissibility, "weak_evidence") {
		return DecisionAdmitWeakEvidence, "artifact is explicitly weak evidence"
	}
	if artifact.AuthorityClass == AuthorityEvidence && trustRank(artifact.EvidenceTrust) < threshold {
		return DecisionAdmitWeakEvidence, "artifact evidence trust is below the requested threshold"
	}
	if directiveScores[artifact.ID] < 0 {
		return DecisionEscalateConflict, "artifact conflicts with an active directive"
	}
	if len(req.ActiveDirectiveIDs) > 0 && directiveScores[artifact.ID] <= 0 && !requiredTypes[artifact.ArtifactType] {
		return DecisionExcludeIrrelevant, "artifact is not linked to an active directive for this turn"
	}
	if requiredTypes[artifact.ArtifactType] {
		return DecisionAdmit, "artifact type is required for this turn"
	}
	if artifact.Status == StatusActive || artifact.Status == StatusAccepted {
		return DecisionAdmitMinimal, "artifact is accepted and relevant to this turn"
	}
	if artifact.Status == StatusDraft {
		return DecisionExcludeInadmissible, "draft artifacts are not admitted by default"
	}
	return DecisionExcludeInadmissible, "artifact did not satisfy admission rules"
}

func (r *Registry) directiveScores(ctx context.Context, directiveIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	scores := make(map[uuid.UUID]int)
	if len(directiveIDs) == 0 {
		return scores, nil
	}
	var links []ArtifactDirectiveLink
	if err := r.db.WithContext(ctx).Where("directive_id IN ?", directiveIDs).Find(&links).Error; err != nil {
		return nil, fmt.Errorf("artifactregistry: directiveScores: %w", err)
	}
	for _, link := range links {
		score := link.Strength
		if score == 0 {
			score = 1
		}
		switch link.LinkType {
		case "conflicts_with":
			scores[link.ArtifactID] -= score
		case "supports", "implements", "constrains", "provides_evidence_for", "explains", "historical_background_for":
			scores[link.ArtifactID] += score
		}
	}
	return scores, nil
}

func scopeMatches(scope, value string) bool {
	return scope == "" || scope == "all" || value == "" || scope == value
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	return set
}

func trustRank(trust EvidenceTrust) int {
	switch trust {
	case TrustHigh:
		return 4
	case TrustMedium:
		return 3
	case TrustLow:
		return 2
	case TrustUnknown:
		return 1
	case TrustInvalidated:
		return -1
	default:
		return 0
	}
}

func directiveIDStrings(ids []uuid.UUID) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != uuid.Nil {
			values = append(values, id.String())
		}
	}
	return values
}
