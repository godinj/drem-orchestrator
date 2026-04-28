package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/artifactregistry"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

func TestArtifactRegistryValidateRendersWarnings(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, artifactregistry.Models()...)
	registry := artifactregistry.NewRegistry(db)
	artifact := artifactregistry.Artifact{
		ArtifactType:   "implementation_plan",
		ContentURI:     "repo:plans/current.md",
		Title:          "Current plan",
		Owner:          "operator",
		Status:         artifactregistry.StatusActive,
		AuthorityClass: artifactregistry.AuthoritySourceOfTruth,
	}
	if err := registry.RegisterArtifact(context.Background(), &artifact); err != nil {
		t.Fatalf("register artifact: %v", err)
	}

	var buf bytes.Buffer
	err := Run(db, []string{"artifact-registry", "validate"}, &buf, false, nil, "")
	if err != nil {
		t.Fatalf("validate should not fail on warnings: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0 error(s), 1 warning(s)") {
		t.Fatalf("expected warning summary, got:\n%s", out)
	}
	if !strings.Contains(out, "active authoritative artifact has no directive link") {
		t.Fatalf("expected issue detail, got:\n%s", out)
	}
}

func TestArtifactRegistryValidateJSONReturnsErrorWhenInvalid(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, artifactregistry.Models()...)
	artifact := artifactregistry.Artifact{
		ID:             uuid.New(),
		ArtifactType:   "implementation_plan",
		ContentURI:     "repo:plans/bad.md",
		Title:          "Bad plan",
		Owner:          "operator",
		Status:         artifactregistry.ArtifactStatus("bogus"),
		AuthorityClass: artifactregistry.AuthoritySourceOfTruth,
		EvidenceTrust:  artifactregistry.TrustHigh,
		Confidence:     artifactregistry.TrustHigh,
	}
	if err := db.Create(&artifact).Error; err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	var buf bytes.Buffer
	err := Run(db, []string{"artifact-registry", "validate"}, &buf, true, nil, "")
	if err == nil {
		t.Fatalf("validate should fail when registry has errors")
	}

	var result artifactRegistryValidationJSON
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode validation JSON: %v\n%s", err, buf.String())
	}
	if result.OK || result.Errors != 1 {
		t.Fatalf("expected one validation error, got %#v", result)
	}
	if len(result.Issues) != 1 || result.Issues[0].Message != "status is not recognized" {
		t.Fatalf("unexpected issues: %#v", result.Issues)
	}
}
