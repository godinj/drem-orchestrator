package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/artifactregistry"
)

type artifactRegistryValidationJSON struct {
	OK       bool                               `json:"ok"`
	Errors   int                                `json:"errors"`
	Warnings int                                `json:"warnings"`
	Issues   []artifactregistry.ValidationIssue `json:"issues"`
}

type artifactRegistrySeedJSON struct {
	RegisteredArtifacts  int      `json:"registered_artifacts"`
	RegisteredDirectives int      `json:"registered_directives"`
	LinkedArtifacts      int      `json:"linked_artifacts"`
	Skipped              []string `json:"skipped"`
}

func handleArtifactRegistry(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli artifact-registry <validate>")
	}

	sub := args[0]
	switch sub {
	case "validate", "audit":
		return handleArtifactRegistryValidate(db, w, jsonMode)
	case "seed-persona-context":
		return handleArtifactRegistrySeedPersonaContext(db, w, jsonMode)
	default:
		return fmt.Errorf("unknown artifact-registry subcommand: %q", sub)
	}
}

func handleArtifactRegistryValidate(db *gorm.DB, w io.Writer, jsonMode bool) error {
	report, err := artifactregistry.NewRegistry(db).Validate(context.Background())
	if err != nil {
		return err
	}
	errors := report.ErrorCount()
	warnings := report.WarningCount()

	if jsonMode {
		if err := writeJSON(w, artifactRegistryValidationJSON{
			OK:       errors == 0,
			Errors:   errors,
			Warnings: warnings,
			Issues:   report.Issues,
		}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "Artifact registry validation: %d error(s), %d warning(s)\n", errors, warnings)
		if len(report.Issues) > 0 {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SEVERITY\tARTIFACT\tCONTENT_URI\tMESSAGE")
			for _, issue := range report.Issues {
				artifactID := "-"
				if issue.ArtifactID != uuid.Nil {
					artifactID = shortID(issue.ArtifactID)
				}
				contentURI := issue.ContentURI
				if contentURI == "" {
					contentURI = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", issue.Severity, artifactID, contentURI, issue.Message)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}

	if errors > 0 {
		return fmt.Errorf("artifact registry validation failed: %d error(s)", errors)
	}
	return nil
}

type personaSeedArtifact struct {
	Path           string
	Title          string
	ArtifactType   string
	Owner          string
	PersonaScope   string
	WorkflowScope  string
	AuthorityClass artifactregistry.AuthorityClass
	EvidenceTrust  artifactregistry.EvidenceTrust
	Status         artifactregistry.ArtifactStatus
	Admissibility  string
}

func handleArtifactRegistrySeedPersonaContext(db *gorm.DB, w io.Writer, jsonMode bool) error {
	ctx := context.Background()
	registry := artifactregistry.NewRegistry(db)
	directive := artifactregistry.Directive{
		ID:            uuid.NewSHA1(uuid.NameSpaceURL, []byte("drem:directive:persona-context-firewall")),
		DirectiveType: "operating_principle",
		Title:         "Persona context must be registry-admitted and minimal",
		Description:   "Persona agents should receive only current, accepted, relevant artifacts selected through the artifact registry/context firewall.",
		Owner:         "operator",
		Status:        artifactregistry.DirectiveActive,
		Priority:      100,
		AuthorityRank: 100,
		ScopeType:     "repo",
		ScopeKey:      "drem-orchestrator",
		AcceptedBy:    "operator",
	}
	if err := registry.RegisterDirective(ctx, &directive); err != nil {
		return err
	}

	seed := []personaSeedArtifact{
		{Path: "CLAUDE.md", Title: "Standing project guidance", ArtifactType: "standing_guidance", Owner: "operator", PersonaScope: "all", WorkflowScope: "all", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "ARCHITECTURE.md", Title: "Architecture constitution", ArtifactType: "architecture_constitution", Owner: "seth", PersonaScope: "seth", WorkflowScope: "review", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/c-suite-world-state-2026-04-22.md", Title: "C-Suite world state", ArtifactType: "world_state", Owner: "kyle", PersonaScope: "all", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/ob1-open-brain-operating-system.md", Title: "OB1 operating-system plan", ArtifactType: "implementation_plan", Owner: "operator", PersonaScope: "all", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/ob1-artifact-registry-context-firewall.md", Title: "Artifact registry context firewall PRD", ArtifactType: "prd", Owner: "operator", PersonaScope: "all", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/drem-pipeline-reliability-policy.md", Title: "DREM pipeline reliability policy", ArtifactType: "operating_policy", Owner: "operator", PersonaScope: "all", WorkflowScope: "pipeline_reliability", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/capacity-health-verification-2026-04-30.md", Title: "Capacity health verification runbook", ArtifactType: "runbook", Owner: "mike", PersonaScope: "all", WorkflowScope: "capacity_health_verification", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/preflight-readiness-2026-04-28.md", Title: "Preflight readiness plan", ArtifactType: "implementation_plan", Owner: "mike", PersonaScope: "all", WorkflowScope: "runtime_preflight", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "plans/user-stories-catalog-operator-annotated.md", Title: "Operator-annotated user stories catalog", ArtifactType: "product_context", Owner: "alex", PersonaScope: "alex", WorkflowScope: "planning", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "deploy/docker/context/csuite-prompts/kyle.md", Title: "Kyle persona contract prompt", ArtifactType: "persona_contract", Owner: "kyle", PersonaScope: "kyle", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "deploy/docker/context/csuite-prompts/mike.md", Title: "Mike persona contract prompt", ArtifactType: "persona_contract", Owner: "mike", PersonaScope: "mike", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "deploy/docker/context/csuite-prompts/alex.md", Title: "Alex persona contract prompt", ArtifactType: "persona_contract", Owner: "alex", PersonaScope: "alex", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
		{Path: "deploy/docker/context/csuite-prompts/seth.md", Title: "Seth persona contract prompt", ArtifactType: "persona_contract", Owner: "seth", PersonaScope: "seth", WorkflowScope: "persona_turn_prompt_assembly", AuthorityClass: artifactregistry.AuthoritySourceOfTruth, EvidenceTrust: artifactregistry.TrustHigh, Status: artifactregistry.StatusActive, Admissibility: "admissible"},
	}

	result := artifactRegistrySeedJSON{RegisteredDirectives: 1}
	for _, item := range seed {
		artifact, ok, err := seedArtifactFromFile(item)
		if err != nil {
			return err
		}
		if !ok {
			result.Skipped = append(result.Skipped, item.Path)
			continue
		}
		if err := registry.RegisterArtifact(ctx, &artifact); err != nil {
			return err
		}
		result.RegisteredArtifacts++
		if err := registry.LinkArtifactDirective(ctx, &artifactregistry.ArtifactDirectiveLink{
			ID:          uuid.NewSHA1(uuid.NameSpaceURL, []byte("drem:artifact-directive:"+artifact.ContentURI+":"+directive.ID.String())),
			ArtifactID:  artifact.ID,
			DirectiveID: directive.ID,
			LinkType:    "supports",
			Strength:    10,
			Rationale:   "Seeded canonical persona context artifact.",
			Confidence:  artifactregistry.TrustHigh,
		}); err == nil {
			result.LinkedArtifacts++
		}
	}

	if jsonMode {
		return writeJSON(w, result)
	}
	fmt.Fprintf(w, "Seeded %d persona-context artifact(s), %d directive(s), %d link(s)\n", result.RegisteredArtifacts, result.RegisteredDirectives, result.LinkedArtifacts)
	if len(result.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped missing artifact(s): %v\n", result.Skipped)
	}
	return nil
}

func seedArtifactFromFile(item personaSeedArtifact) (artifactregistry.Artifact, bool, error) {
	path := filepath.Clean(item.Path)
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return artifactregistry.Artifact{}, false, nil
		}
		return artifactregistry.Artifact{}, false, fmt.Errorf("read seed artifact %s: %w", item.Path, err)
	}
	sum := sha256.Sum256(contents)
	now := time.Now().UTC()
	return artifactregistry.Artifact{
		ID:               uuid.NewSHA1(uuid.NameSpaceURL, []byte("drem:artifact:repo:"+filepath.ToSlash(path))),
		ArtifactType:     item.ArtifactType,
		ContentURI:       "repo:" + filepath.ToSlash(path),
		ContentHash:      hex.EncodeToString(sum[:]),
		Title:            item.Title,
		Owner:            item.Owner,
		PersonaScope:     item.PersonaScope,
		WorkflowScope:    item.WorkflowScope,
		Status:           item.Status,
		AuthorityClass:   item.AuthorityClass,
		Admissibility:    item.Admissibility,
		EvidenceTrust:    item.EvidenceTrust,
		Confidence:       artifactregistry.TrustHigh,
		VisibilityScope:  item.PersonaScope,
		LastSeenAt:       &now,
		LastValidatedAt:  &now,
		ValidationStatus: artifactregistry.ValidationValid,
		Metadata: map[string]any{
			"path":          filepath.ToSlash(path),
			"size_bytes":    len(contents),
			"seeded_by":     "artifact-registry seed-persona-context",
			"seeded_at_utc": now.Format(time.RFC3339),
		},
	}, true, nil
}
