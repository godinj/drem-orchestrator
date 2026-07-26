package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
)

type incidentReplayFixture struct {
	IncidentID string `json:"incident_id"`
	Task       struct {
		Phase   string           `json:"phase"`
		Status  model.TaskStatus `json:"status"`
		Context model.JSONField  `json:"context"`
	} `json:"task"`
	Attempt struct {
		AttemptID             string `json:"attempt_id"`
		FailureClassification string `json:"failure_classification"`
		FirstError            string `json:"first_error"`
	} `json:"attempt"`
	Expected struct {
		Classification       string `json:"classification"`
		BoundedRetryEligible bool   `json:"bounded_retry_eligible"`
		RejectionPath        string `json:"rejection_path"`
		MaxRetries           int    `json:"max_retries"`
	} `json:"expected"`
}

func TestProductionIncidentReplayCorpus(t *testing.T) {
	paths, err := filepath.Glob("testdata/incidents/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, fixturePath := range paths {
		fixturePath := fixturePath
		t.Run(filepath.Base(fixturePath), func(t *testing.T) {
			contents, err := os.ReadFile(fixturePath)
			require.NoError(t, err)
			var fixture incidentReplayFixture
			require.NoError(t, json.Unmarshal(contents, &fixture))
			require.NotEmpty(t, fixture.IncidentID)
			require.NoError(t, uuid.Validate(fixture.Attempt.AttemptID))

			task := &model.Task{ID: uuid.New(), Phase: fixture.Task.Phase, Status: fixture.Task.Status, Context: fixture.Task.Context}
			rejections, eligible := testContractOnlyRejections(task)
			require.Equal(t, fixture.Expected.BoundedRetryEligible, eligible)
			require.NotEmpty(t, rejections)
			require.Equal(t, fixture.Expected.RejectionPath, rejections[0].Path)
			require.Equal(t, fixture.Expected.Classification, normalizeFailureClass(fixture.Attempt.FailureClassification, fixture.Attempt.FirstError))

			budget := consumeRetryBudget(task, retryEdgeForTask(*task, string(model.AgentCoder)), fixture.Expected.Classification, fixture.Attempt.FirstError, time.Unix(1, 0))
			require.Equal(t, fixture.Expected.MaxRetries, budget.MaxRetries)
			require.False(t, budget.Exhausted)
		})
	}
}
