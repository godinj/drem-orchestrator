package orchhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func validateIntegrationSeams(spec orchdto.TaskSpecDTO) error {
	if len(spec.IntegrationSeams) == 0 {
		return errors.New("at least one source-backed production entrypoint seam is required with execution_plan")
	}
	criteria := make(map[string]struct{}, len(spec.AcceptanceCriteria))
	covered := make(map[string]struct{}, len(spec.AcceptanceCriteria))
	for _, criterion := range spec.AcceptanceCriteria {
		criteria[criterion.ID] = struct{}{}
	}
	scope := make(map[string]struct{}, len(spec.ProposedScope))
	for _, path := range spec.ProposedScope {
		scope[path] = struct{}{}
	}
	integrationFiles := map[string]struct{}{}
	for _, sub := range spec.ExecutionPlan.Subtasks {
		if sub.Phase == "integration" {
			for _, path := range sub.Files {
				integrationFiles[path] = struct{}{}
			}
		}
	}
	seen := map[string]struct{}{}
	for i, seam := range spec.IntegrationSeams {
		if seam.ID == "" || !stableReferenceID.MatchString(seam.ID) {
			return fmt.Errorf("[%d].id must be a stable identifier", i)
		}
		if _, exists := seen[seam.ID]; exists {
			return fmt.Errorf("id %q is duplicated", seam.ID)
		}
		seen[seam.ID] = struct{}{}
		if seam.EntryPoint == "" {
			return fmt.Errorf("[%d].entry_point is required", i)
		}
		if len(seam.AcceptanceCriteriaIDs) == 0 || hasBlank(seam.AcceptanceCriteriaIDs) {
			return fmt.Errorf("[%d].acceptance_criteria_ids requires non-empty entries", i)
		}
		for _, id := range seam.AcceptanceCriteriaIDs {
			if _, ok := criteria[id]; !ok {
				return fmt.Errorf("[%d] references unknown acceptance criterion %q", i, id)
			}
			covered[id] = struct{}{}
		}
		switch seam.VerificationLevel {
		case "automated_integration", "native_runtime", "computer_use":
		default:
			return fmt.Errorf("[%d].verification_level must be automated_integration, native_runtime, or computer_use", i)
		}
		if len(seam.VerificationSteps) == 0 || hasBlank(seam.VerificationSteps) {
			return fmt.Errorf("[%d].verification_steps requires non-empty entries", i)
		}
		if len(seam.SourceEvidence) == 0 {
			return fmt.Errorf("[%d].source_evidence requires at least one excerpt", i)
		}
		for j, evidence := range seam.SourceEvidence {
			if err := validatePlanPath(evidence.Path); err != nil {
				return fmt.Errorf("[%d].source_evidence[%d].path: %w", i, j, err)
			}
			if evidence.Symbol == "" || strings.TrimSpace(evidence.Excerpt) == "" {
				return fmt.Errorf("[%d].source_evidence[%d] requires symbol and excerpt", i, j)
			}
			digest := sha256.Sum256([]byte(evidence.Excerpt))
			if evidence.ExcerptSHA256 != hex.EncodeToString(digest[:]) {
				return fmt.Errorf("[%d].source_evidence[%d].excerpt_sha256 does not match excerpt", i, j)
			}
		}
		if len(seam.MissingEdges) == 0 {
			return fmt.Errorf("[%d].missing_edges requires at least one production wiring edge", i)
		}
		for j, edge := range seam.MissingEdges {
			if edge.Description == "" || len(edge.RequiredFiles) == 0 || hasBlank(edge.RequiredFiles) {
				return fmt.Errorf("[%d].missing_edges[%d] requires description and required_files", i, j)
			}
			for _, path := range edge.RequiredFiles {
				if err := validatePlanPath(path); err != nil {
					return fmt.Errorf("[%d].missing_edges[%d] file: %w", i, j, err)
				}
				if _, ok := scope[path]; !ok {
					return fmt.Errorf("[%d].missing_edges[%d] required file %q is outside proposed_scope", i, j, path)
				}
				if _, ok := integrationFiles[path]; !ok {
					return fmt.Errorf("[%d].missing_edges[%d] required file %q is absent from the integration subtask", i, j, path)
				}
			}
		}
	}
	for id := range criteria {
		if _, ok := covered[id]; !ok {
			return fmt.Errorf("acceptance criterion %q has no production entrypoint seam", id)
		}
	}
	return nil
}
