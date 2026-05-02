package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestArtifactRegistrySeedPersonaContextUpdatesChangedArtifacts(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, artifactregistry.Models()...)
	repo := t.TempDir()
	t.Chdir(repo)
	requireWriteFile(t, filepath.Join(repo, "CLAUDE.md"), "first guidance\n")
	requireWriteFile(t, filepath.Join(repo, "plans/drem-pipeline-reliability-policy.md"), "pipeline policy\n")
	requireWriteFile(t, filepath.Join(repo, "plans/orch-stale-task-cleanup-2026-05-02.md"), "cleanup record\n")

	var buf bytes.Buffer
	err := Run(db, []string{"artifact-registry", "seed-persona-context"}, &buf, false, nil, "")
	if err != nil {
		t.Fatalf("seed should succeed: %v", err)
	}

	registry := artifactregistry.NewRegistry(db)
	first, err := registry.FindArtifactByURI(context.Background(), "repo:CLAUDE.md")
	if err != nil {
		t.Fatalf("find seeded artifact: %v", err)
	}
	if first.ContentHash == "" || first.LastSeenAt == nil || first.LastValidatedAt == nil {
		t.Fatalf("expected content hash and validation timestamps, got %#v", first)
	}
	reliabilityPolicy, err := registry.FindArtifactByURI(context.Background(), "repo:plans/drem-pipeline-reliability-policy.md")
	if err != nil {
		t.Fatalf("find seeded reliability policy artifact: %v", err)
	}
	if reliabilityPolicy.ArtifactType != "operating_policy" || reliabilityPolicy.WorkflowScope != "pipeline_reliability" {
		t.Fatalf("unexpected reliability policy metadata: %#v", reliabilityPolicy)
	}
	cleanupRecord, err := registry.FindArtifactByURI(context.Background(), "repo:plans/orch-stale-task-cleanup-2026-05-02.md")
	if err != nil {
		t.Fatalf("find seeded cleanup record artifact: %v", err)
	}
	if cleanupRecord.ArtifactType != "cleanup_record" || cleanupRecord.Owner != "kyle" {
		t.Fatalf("unexpected cleanup record metadata: %#v", cleanupRecord)
	}

	requireWriteFile(t, filepath.Join(repo, "CLAUDE.md"), "second guidance\n")
	buf.Reset()
	err = Run(db, []string{"artifact-registry", "seed-persona-context"}, &buf, false, nil, "")
	if err != nil {
		t.Fatalf("second seed should succeed: %v", err)
	}

	second, err := registry.FindArtifactByURI(context.Background(), "repo:CLAUDE.md")
	if err != nil {
		t.Fatalf("find updated artifact: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("artifact should be updated in place, got %s then %s", first.ID, second.ID)
	}
	if first.ContentHash == second.ContentHash {
		t.Fatalf("content hash should change when artifact body changes")
	}
	if !second.UpdatedAt.After(first.UpdatedAt) && second.LastSeenAt.Equal(*first.LastSeenAt) {
		t.Fatalf("metadata timestamps should be refreshed on reseed")
	}
}

func requireWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
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
