package orchhttp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/testutil"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestSubmitCodexGoalUsageIsAttributedAndIdempotent(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	task := testutil.CreateTask(t, srv.DB, project.ID, "measured task", model.StatusIntegrationReady)
	req := orchdto.SubmitCodexGoalUsageRequest{
		Actor: "codex:thread-42", ThreadID: "thread-42", GoalObjective: "supervise measured task",
		GoalStatus: "complete", TokensUsed: 12345, ElapsedMS: 67890, IdempotencyKey: "goal-usage-1",
	}

	first := postCodexUsage(t, ts.URL, task.ID.String(), req, "codex:thread-42")
	defer first.Body.Close()
	require.Equal(t, http.StatusCreated, first.StatusCode)
	var got orchdto.CodexGoalUsageDTO
	require.NoError(t, json.NewDecoder(first.Body).Decode(&got))
	require.Equal(t, int64(12345), got.TokensUsed)
	require.Equal(t, "codex_get_goal", got.Source)

	replay := postCodexUsage(t, ts.URL, task.ID.String(), req, "codex:thread-42")
	defer replay.Body.Close()
	require.Equal(t, http.StatusOK, replay.StatusCode)
	require.Equal(t, "true", replay.Header.Get("X-Drem-Idempotent-Replay"))
	var count int64
	require.NoError(t, srv.DB.Model(&model.CodexGoalUsage{}).Count(&count).Error)
	require.Equal(t, int64(1), count)

	req.TokensUsed++
	conflict := postCodexUsage(t, ts.URL, task.ID.String(), req, "codex:thread-42")
	defer conflict.Body.Close()
	require.Equal(t, http.StatusConflict, conflict.StatusCode)
}

func TestSubmitCodexGoalUsageRejectsSpoofedOrNonTerminalUsage(t *testing.T) {
	srv, ts, project := setupHTTPTest(t, nil)
	task := testutil.CreateTask(t, srv.DB, project.ID, "measured task", model.StatusIntegrationReady)
	req := orchdto.SubmitCodexGoalUsageRequest{
		Actor: "codex:thread-42", ThreadID: "different-thread", GoalObjective: "supervise measured task",
		GoalStatus: "active", TokensUsed: 10, ElapsedMS: 100, IdempotencyKey: "goal-usage-invalid",
	}
	resp := postCodexUsage(t, ts.URL, task.ID.String(), req, "codex:thread-42")
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	req.ThreadID = "thread-42"
	resp = postCodexUsage(t, ts.URL, task.ID.String(), req, "codex:other-thread")
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func postCodexUsage(t *testing.T, baseURL, taskID string, payload orchdto.SubmitCodexGoalUsageRequest, actor string) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost,
		baseURL+"/projects/"+projectName+"/tasks/"+taskID+"/codex-usage", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Drem-Actor", actor)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
