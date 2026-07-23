package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestVerifiedSourcePacksCarryImmutableEvidenceAndPairing(t *testing.T) {
	db := testutil.NewTestDB(t)
	task := model.Task{ID: uuid.New(), ProjectID: uuid.New(), Title: "feature", Description: "x", Status: model.StatusPlanReview}
	require.NoError(t, db.Create(&task).Error)
	spec := orchdto.TaskSpecDTO{
		AcceptanceCriteria: []orchdto.TaskAcceptanceCriterionDTO{{ID: "registered", Description: "action is registered"}},
		IntegrationSeams: []orchdto.TaskIntegrationSeamDTO{{
			ID: "action-registration", EntryPoint: "ActionCoordinator::registerAllActions",
			SourceEvidence: []orchdto.TaskSourceEvidenceDTO{{Path: "src/ui/ActionCoordinator.cpp", Symbol: "registerAllActions", Excerpt: "registerEditActions();", ExcerptSHA256: "hash"}},
		}},
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.TaskSpecification{
		ID: uuid.New(), TaskID: task.ID, ProjectID: task.ProjectID, IdempotencyKey: "source-pack", SpecFingerprint: "fingerprint-1", SpecJSON: string(raw),
	}).Error)
	plans := []planEntry{
		{Title: "red", Phase: "test", EstimatedFiles: []string{"tests/test_action.cpp"}, TestsFor: []int{1}},
		{Title: "implementation", Phase: "implementation", EstimatedFiles: []string{"src/ui/ActionAudio.cpp"}},
	}

	packs, err := verifiedSourcePacks(db, &task, plans)
	require.NoError(t, err)
	require.Len(t, packs, 2)
	require.Contains(t, packs[0], "registerEditActions();")
	require.Contains(t, packs[0], "src/ui/ActionAudio.cpp")
	require.Contains(t, packs[1], "tests/test_action.cpp")
	require.Contains(t, packs[1], "fingerprint-1")
}
