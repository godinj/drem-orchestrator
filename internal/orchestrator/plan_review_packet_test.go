package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestPlanReviewDescriptionCompactsVerifiedSourceEvidence(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "review-packet", "/tmp/review-packet.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "review", model.StatusPlanReview)
	spec := orchdto.TaskSpecDTO{
		Description: "Divide selected audio events at transients.",
		AcceptanceCriteria: []orchdto.TaskAcceptanceCriterionDTO{{
			ID: "AC1", Description: "Split at stable interior transients.", ExpectedBehavior: []string{"event is divided"},
		}},
		ProposedScope: []string{"src/model/AudioClipTransientSlicing.cpp"},
		Exclusions:    []string{"persistent hitpoint state"},
		IntegrationSeams: []orchdto.TaskIntegrationSeamDTO{{
			ID: "command", AcceptanceCriteriaIDs: []string{"AC1"}, EntryPoint: "ActionCoordinator::registerAllActions",
			SourceEvidence: []orchdto.TaskSourceEvidenceDTO{{
				Path: "src/ui/ActionCoordinator.cpp", Symbol: "registerAllActions",
				Excerpt: strings.Repeat("SENSITIVE SOURCE BODY THAT MUST NOT REACH THE MODEL ", 20), ExcerptSHA256: "abc123",
			}},
			MissingEdges:      []orchdto.TaskIntegrationEdgeDTO{{Description: "register action", RequiredFiles: []string{"src/ui/ActionCoordinator.cpp"}}},
			VerificationLevel: "computer_use", VerificationSteps: []string{"invoke the command"},
		}},
	}
	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.TaskSpecification{
		ID: uuid.New(), TaskID: task.ID, ProjectID: project.ID,
		ObservationSessionID: "session", Product: "Cubase", ProductVersion: "14", OperatingSystem: "macOS",
		DisplayEnvironment: "desktop", ObservedAt: time.Now(), ObserverActor: "codex", CreatorActor: "codex",
		IdempotencyKey: "packet-test", RequestHash: "request", SpecFingerprint: "fingerprint", SpecJSON: string(specJSON),
	}).Error)

	packet, err := (&Orchestrator{db: db}).planReviewDescription(&task)
	require.NoError(t, err)
	require.Contains(t, packet, "AudioClipTransientSlicing.cpp")
	require.Contains(t, packet, "ActionCoordinator::registerAllActions")
	require.Contains(t, packet, "abc123")
	require.Contains(t, packet, "integration_scope_policy")
	require.Contains(t, packet, "read/merge/verify")
	require.NotContains(t, packet, "SENSITIVE SOURCE BODY")
	require.Less(t, len(packet), len(string(specJSON)))
}

func TestPlanReviewDescriptionFallsBackForLegacyTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := testutil.CreateProject(t, db, "legacy-packet", "/tmp/legacy-packet.git", "master")
	task := testutil.CreateTask(t, db, project.ID, "legacy", model.StatusPlanReview)
	task.Description = strings.Repeat("legacy ", 4)

	packet, err := (&Orchestrator{db: db}).planReviewDescription(&task)
	require.NoError(t, err)
	require.Equal(t, task.Description, packet)
}
