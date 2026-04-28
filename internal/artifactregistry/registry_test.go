package artifactregistry

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	db := testutil.NewTestDBWithModels(t, Models()...)
	return NewRegistry(db)
}

func TestRegisterArtifactUpsertsByContentURI(t *testing.T) {
	ctx := context.Background()
	registry := newTestRegistry(t)

	artifact := Artifact{
		ArtifactType:   "implementation_plan",
		ContentURI:     "repo:plans/ob1.md",
		Title:          "OB1 plan",
		Owner:          "operator",
		Status:         StatusActive,
		AuthorityClass: AuthoritySourceOfTruth,
		EvidenceTrust:  TrustHigh,
	}
	if err := registry.RegisterArtifact(ctx, &artifact); err != nil {
		t.Fatalf("register artifact: %v", err)
	}
	firstID := artifact.ID

	artifact.Title = "Updated OB1 plan"
	artifact.Status = StatusAccepted
	if err := registry.RegisterArtifact(ctx, &artifact); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	if artifact.ID != firstID {
		t.Fatalf("expected upsert to preserve id %s, got %s", firstID, artifact.ID)
	}

	loaded, err := registry.FindArtifactByURI(ctx, "repo:plans/ob1.md")
	if err != nil {
		t.Fatalf("find artifact: %v", err)
	}
	if loaded.Title != "Updated OB1 plan" || loaded.Status != StatusAccepted {
		t.Fatalf("artifact was not updated: %#v", loaded)
	}
}

func TestSupersedeMarksOldArtifactAndLinksReplacement(t *testing.T) {
	ctx := context.Background()
	registry := newTestRegistry(t)

	oldArtifact := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "persona_contract",
		ContentURI:     "repo:old.md",
		Title:          "Old contract",
		Status:         StatusActive,
		AuthorityClass: AuthoritySourceOfTruth,
	})
	newArtifact := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "persona_contract",
		ContentURI:     "repo:new.md",
		Title:          "New contract",
		Status:         StatusActive,
		AuthorityClass: AuthoritySourceOfTruth,
	})

	if err := registry.Supersede(ctx, oldArtifact.ID, newArtifact.ID, "new contract accepted", "operator"); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	loadedOld, err := registry.GetArtifact(ctx, oldArtifact.ID)
	if err != nil {
		t.Fatalf("load old artifact: %v", err)
	}
	if loadedOld.Status != StatusSuperseded || loadedOld.SupersededByID == nil || *loadedOld.SupersededByID != newArtifact.ID {
		t.Fatalf("old artifact not superseded correctly: %#v", loadedOld)
	}

	var link ArtifactLink
	if err := registry.db.Where("source_artifact_id = ? AND target_artifact_id = ? AND link_type = ?", newArtifact.ID, oldArtifact.ID, "supersedes").Take(&link).Error; err != nil {
		t.Fatalf("expected supersession link: %v", err)
	}
}

func TestValidateFlagsActiveAuthoritativeArtifactWithoutDirective(t *testing.T) {
	ctx := context.Background()
	registry := newTestRegistry(t)
	mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "workflow_recipe",
		ContentURI:     "repo:recipes/review.md",
		Title:          "Review recipe",
		Owner:          "seth",
		Status:         StatusActive,
		AuthorityClass: AuthoritySourceOfTruth,
	})

	report, err := registry.Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(report.Issues) != 1 {
		t.Fatalf("expected one warning, got %#v", report.Issues)
	}
	if report.HasErrors() || report.ErrorCount() != 0 || report.WarningCount() != 1 {
		t.Fatalf("unexpected report counts: errors=%d warnings=%d", report.ErrorCount(), report.WarningCount())
	}
	if report.Issues[0].Severity != "warning" || report.Issues[0].Message != "active authoritative artifact has no directive link" {
		t.Fatalf("unexpected issue: %#v", report.Issues[0])
	}
}

func TestAdmitContextFiltersStaleSupersededAndIrrelevantArtifacts(t *testing.T) {
	ctx := context.Background()
	registry := newTestRegistry(t)
	directive := Directive{DirectiveType: "operator_directive", Title: "Use registry", Status: DirectiveActive}
	if err := registry.RegisterDirective(ctx, &directive); err != nil {
		t.Fatalf("register directive: %v", err)
	}

	relevant := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "implementation_plan",
		ContentURI:     "repo:plans/current.md",
		Title:          "Current plan",
		ProjectID:      "drem",
		TaskID:         "task-1",
		PersonaScope:   "seth",
		WorkflowScope:  "review",
		Status:         StatusActive,
		AuthorityClass: AuthoritySourceOfTruth,
		EvidenceTrust:  TrustHigh,
	})
	mustLinkDirective(t, registry, relevant.ID, directive.ID, "supports", 5)

	otherTask := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "implementation_plan",
		ContentURI:     "repo:plans/other-task.md",
		Title:          "Other task plan",
		ProjectID:      "drem",
		TaskID:         "task-2",
		PersonaScope:   "seth",
		WorkflowScope:  "review",
		Status:         StatusActive,
		AuthorityClass: AuthoritySourceOfTruth,
	})
	superseded := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "implementation_plan",
		ContentURI:     "repo:plans/superseded.md",
		Title:          "Superseded plan",
		ProjectID:      "drem",
		TaskID:         "task-1",
		PersonaScope:   "seth",
		WorkflowScope:  "review",
		Status:         StatusSuperseded,
		AuthorityClass: AuthoritySourceOfTruth,
		SupersededByID: &relevant.ID,
	})
	expiredAt := time.Now().Add(-time.Hour)
	staleReport := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "generated_report",
		ContentURI:     "repo:reports/stale.json",
		Title:          "Stale report",
		ProjectID:      "drem",
		TaskID:         "task-1",
		PersonaScope:   "seth",
		WorkflowScope:  "review",
		Status:         StatusActive,
		AuthorityClass: AuthorityEvidence,
		EvidenceTrust:  TrustMedium,
		ValidUntil:     &expiredAt,
	})

	result, err := registry.AdmitContext(ctx, AdmissionRequest{
		ProjectID:          "drem",
		TaskID:             "task-1",
		Persona:            "seth",
		WorkflowStage:      "review",
		ActiveDirectiveIDs: []uuid.UUID{directive.ID},
		CandidateArtifactIDs: []uuid.UUID{
			relevant.ID,
			otherTask.ID,
			superseded.ID,
			staleReport.ID,
		},
	})
	if err != nil {
		t.Fatalf("admit context: %v", err)
	}
	if len(result.Admitted) != 1 || result.Admitted[0].ID != relevant.ID {
		t.Fatalf("expected only relevant artifact admitted, got %#v", result.Admitted)
	}
	assertDecision(t, result.Decisions, relevant.ID, DecisionAdmitMinimal)
	assertDecisionForURI(t, registry.db, result.Decisions, "repo:plans/superseded.md", DecisionExcludeSuperseded)
	assertDecisionForURI(t, registry.db, result.Decisions, "repo:reports/stale.json", DecisionExcludeStale)
	assertDecisionForURI(t, registry.db, result.Decisions, "repo:plans/other-task.md", DecisionExcludeIrrelevant)
}

func TestAdmitContextEscalatesDirectiveConflicts(t *testing.T) {
	ctx := context.Background()
	registry := newTestRegistry(t)
	directive := Directive{DirectiveType: "operator_directive", Title: "Current directive", Status: DirectiveActive}
	if err := registry.RegisterDirective(ctx, &directive); err != nil {
		t.Fatalf("register directive: %v", err)
	}
	artifact := mustRegisterArtifact(t, registry, Artifact{
		ArtifactType:   "decision_record",
		ContentURI:     "repo:decisions/conflict.md",
		Title:          "Conflicting decision",
		Status:         StatusActive,
		AuthorityClass: AuthorityDecision,
		EvidenceTrust:  TrustHigh,
	})
	mustLinkDirective(t, registry, artifact.ID, directive.ID, "conflicts_with", 3)

	result, err := registry.AdmitContext(ctx, AdmissionRequest{ActiveDirectiveIDs: []uuid.UUID{directive.ID}})
	if err != nil {
		t.Fatalf("admit context: %v", err)
	}
	if !result.EscalationRequired {
		t.Fatalf("expected escalation for directive conflict")
	}
	assertDecision(t, result.Decisions, artifact.ID, DecisionEscalateConflict)
}

func mustRegisterArtifact(t *testing.T, registry *Registry, artifact Artifact) Artifact {
	t.Helper()
	if err := registry.RegisterArtifact(context.Background(), &artifact); err != nil {
		t.Fatalf("register artifact %s: %v", artifact.ContentURI, err)
	}
	return artifact
}

func mustLinkDirective(t *testing.T, registry *Registry, artifactID, directiveID uuid.UUID, linkType string, strength int) {
	t.Helper()
	err := registry.LinkArtifactDirective(context.Background(), &ArtifactDirectiveLink{
		ArtifactID:  artifactID,
		DirectiveID: directiveID,
		LinkType:    linkType,
		Strength:    strength,
		Confidence:  TrustHigh,
	})
	if err != nil {
		t.Fatalf("link directive: %v", err)
	}
}

func assertDecision(t *testing.T, decisions []ContextAdmissionDecision, artifactID uuid.UUID, expected AdmissionDecision) {
	t.Helper()
	for _, decision := range decisions {
		if decision.ArtifactID == artifactID {
			if decision.Decision != expected {
				t.Fatalf("expected decision %s for %s, got %s (%s)", expected, artifactID, decision.Decision, decision.Reason)
			}
			return
		}
	}
	t.Fatalf("no decision found for artifact %s", artifactID)
}

func assertDecisionForURI(t *testing.T, db *gorm.DB, decisions []ContextAdmissionDecision, uri string, expected AdmissionDecision) {
	t.Helper()
	var artifact Artifact
	if err := db.Where("content_uri = ?", uri).Take(&artifact).Error; err != nil {
		t.Fatalf("load artifact %s: %v", uri, err)
	}
	assertDecision(t, decisions, artifact.ID, expected)
}
