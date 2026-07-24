package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// planReviewDescription removes already-verified source bodies and reference
// observation media from the model packet. The immutable specification and
// source-evidence gate remain authoritative in the database and worktree.
func (o *Orchestrator) planReviewDescription(task *model.Task) (string, error) {
	var stored model.TaskSpecification
	if err := o.db.Where("task_id = ?", task.ID).First(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return task.Description, nil
		}
		return "", err
	}
	var spec orchdto.TaskSpecDTO
	if err := json.Unmarshal([]byte(stored.SpecJSON), &spec); err != nil {
		return "", fmt.Errorf("decode immutable task specification: %w", err)
	}
	type sourceProof struct {
		Path          string `json:"path"`
		Symbol        string `json:"symbol"`
		ExcerptSHA256 string `json:"excerpt_sha256"`
	}
	type seam struct {
		ID                    string                           `json:"id"`
		AcceptanceCriteriaIDs []string                         `json:"acceptance_criteria_ids"`
		EntryPoint            string                           `json:"entry_point"`
		SourceProofs          []sourceProof                    `json:"verified_source_proofs"`
		MissingEdges          []orchdto.TaskIntegrationEdgeDTO `json:"missing_edges"`
		VerificationLevel     string                           `json:"verification_level"`
		VerificationSteps     []string                         `json:"verification_steps"`
	}
	seams := make([]seam, 0, len(spec.IntegrationSeams))
	for _, original := range spec.IntegrationSeams {
		proofs := make([]sourceProof, 0, len(original.SourceEvidence))
		for _, evidence := range original.SourceEvidence {
			proofs = append(proofs, sourceProof{evidence.Path, evidence.Symbol, evidence.ExcerptSHA256})
		}
		seams = append(seams, seam{
			ID: original.ID, AcceptanceCriteriaIDs: original.AcceptanceCriteriaIDs,
			EntryPoint: original.EntryPoint, SourceProofs: proofs, MissingEdges: original.MissingEdges,
			VerificationLevel: original.VerificationLevel, VerificationSteps: original.VerificationSteps,
		})
	}
	packet := struct {
		Description            string                               `json:"description"`
		AcceptanceCriteria     []orchdto.TaskAcceptanceCriterionDTO `json:"acceptance_criteria"`
		ProposedScope          []string                             `json:"proposed_scope"`
		Exclusions             []string                             `json:"exclusions"`
		IntegrationSeams       []seam                               `json:"integration_seams"`
		IntegrationScopePolicy string                               `json:"integration_scope_policy"`
	}{
		Description: spec.Description, AcceptanceCriteria: spec.AcceptanceCriteria,
		ProposedScope: spec.ProposedScope, Exclusions: spec.Exclusions, IntegrationSeams: seams,
		IntegrationScopePolicy: "Integration files are the complete read/merge/verify declaration. Every missing_edges.required_files path must remain in integration files. Only writable_files (or files when writable_files is omitted) is the worker mutation and branch-acceptance scope.",
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		return "", fmt.Errorf("encode compact plan-review description: %w", err)
	}
	return "The source excerpts and hashes in this immutable task specification were verified against the exact base before review. Review this compact contract:\n" + string(encoded), nil
}
