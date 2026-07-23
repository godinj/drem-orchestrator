package orchhttp_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestRevisePlanEndpointValidatesImmutableScopeAndReplays(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := createAdapterTaskWithSpecification(t, srv, project, model.StatusPlanReview)
	spec := validTaskSpecWithExecutionPlan()
	revised := *spec.ExecutionPlan
	for i := range revised.Subtasks {
		revised.Subtasks[i].AgentType = "coder"
	}
	revised.Subtasks[2].Description = "Verify compile commands, native gates, and exact-binary Computer Use at the host delivery boundary."
	body, err := json.Marshal(orchdto.ReviseTaskPlanRequest{
		ExecutionPlan: revised,
		Reason:        "Cover every reviewer-identified verification boundary",
	})
	require.NoError(t, err)
	url := base + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/revise-plan"

	first, firstBody := doGuardedJSON(t, http.MethodPost, url, string(body), "codex:thread-42", "revise-plan-1", task.StateVersion)
	require.Equal(t, http.StatusOK, first.StatusCode, string(firstBody))
	second, secondBody := doGuardedJSON(t, http.MethodPost, url, string(body), "codex:thread-42", "revise-plan-1", task.StateVersion)
	require.Equal(t, http.StatusOK, second.StatusCode, string(secondBody))
	require.Equal(t, "true", second.Header.Get("X-Drem-Idempotent-Replay"))
	require.Equal(t, firstBody, secondBody)
	require.Len(t, fake.Calls, 1)
	require.Equal(t, "RevisePlan", fake.Calls[0].Method)

	var dto orchdto.TaskDTO
	require.NoError(t, json.Unmarshal(firstBody, &dto))
	require.Equal(t, task.StateVersion+1, dto.StateVersion)
	require.Equal(t, string(model.StatusPlanReview), dto.Status)
}

func TestRevisePlanEndpointRejectsScopeExpansionBeforeDispatch(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := createAdapterTaskWithSpecification(t, srv, project, model.StatusPlanReview)
	spec := validTaskSpecWithExecutionPlan()
	revised := *spec.ExecutionPlan
	for i := range revised.Subtasks {
		revised.Subtasks[i].AgentType = "coder"
	}
	revised.Subtasks[0].Files = []string{"outside/undeclared.cpp"}
	body, err := json.Marshal(orchdto.ReviseTaskPlanRequest{ExecutionPlan: revised, Reason: "expand scope"})
	require.NoError(t, err)
	url := base + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/revise-plan"

	resp, responseBody := doGuardedJSON(t, http.MethodPost, url, string(body), "codex:thread-42", "revise-plan-outside", task.StateVersion)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(responseBody))
	require.Contains(t, decodeErr(t, responseBody), "outside proposed_scope")
	require.Empty(t, fake.Calls)
}

func TestRevisePlanEndpointRejectsRemovalOfRequiredIntegrationEdge(t *testing.T) {
	fake, project, srv, base := setupGateHTTPTest(t)
	task := createAdapterTaskWithSpecification(t, srv, project, model.StatusPlanReview)
	spec := validTaskSpecWithExecutionPlan()
	revised := *spec.ExecutionPlan
	for i := range revised.Subtasks {
		revised.Subtasks[i].AgentType = "coder"
	}
	revised.Subtasks[2].Files = []string{"src/model/TakeCompModel.cpp"}
	body, err := json.Marshal(orchdto.ReviseTaskPlanRequest{ExecutionPlan: revised, Reason: "drop wiring file"})
	require.NoError(t, err)
	url := base + "/projects/" + projectName + "/tasks/" + task.ID.String() + "/revise-plan"

	resp, responseBody := doGuardedJSON(t, http.MethodPost, url, string(body), "codex:thread-42", "revise-plan-missing-edge", task.StateVersion)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(responseBody))
	require.Contains(t, decodeErr(t, responseBody), "absent from the integration subtask")
	require.Empty(t, fake.Calls)
}

func createAdapterTaskWithSpecification(t *testing.T, srv *orchhttp.Server, project model.Project, status model.TaskStatus) model.Task {
	t.Helper()
	task := testutil.CreateTask(t, srv.DB, project.ID, "adapter-authored plan", status)
	spec := validTaskSpecWithExecutionPlan()
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, srv.DB.Create(&model.TaskSpecification{
		ID: uuid.New(), TaskID: task.ID, ProjectID: project.ID,
		ObservationSessionID: spec.Observation.SessionID,
		Product:              spec.Observation.Product, ProductVersion: spec.Observation.ProductVersion,
		OperatingSystem: spec.Observation.OS, DisplayEnvironment: spec.Observation.DisplayEnvironment,
		ObservedAt: spec.Observation.ObservedAt, ObserverActor: spec.Observation.ObserverActor,
		CreatorActor: spec.Actor, IdempotencyKey: uuid.NewString(),
		RequestHash: uuid.NewString(), SpecFingerprint: uuid.NewString(), SpecJSON: string(raw),
		CreatedAt: time.Now().UTC(),
	}).Error)
	return task
}
