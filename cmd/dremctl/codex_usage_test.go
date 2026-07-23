package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestCodexUsageCommandSubmitsFinalGoalMetrics(t *testing.T) {
	var post recordedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		post = recordRequest(t, r)
		writeJSONResponse(t, w, orchdto.CodexGoalUsageDTO{
			ID: "usage-1", TaskID: testTaskID, ThreadID: "thread-1", GoalStatus: "complete",
			TokensUsed: 12345, ElapsedMS: 67890,
		})
	}))
	defer ts.Close()

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{
		"codex-usage", testTaskID, "--goal-objective", "supervise Canvas task",
		"--goal-status", "complete", "--tokens-used", "12345", "--elapsed-ms", "67890",
	}, mapEnv(map[string]string{
		"DREM_ORCH_URL": ts.URL, "DREM_PROJECT": "canvas", "DREM_ORCH_TOKEN": "token-1", "DREM_ACTOR": "codex:thread-1",
	}), &out, &errOut)
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas/tasks/"+testTaskID+"/codex-usage", post.Path)
	require.Equal(t, "codex:thread-1", post.Actor)
	var body orchdto.SubmitCodexGoalUsageRequest
	require.NoError(t, json.Unmarshal([]byte(post.Body), &body))
	require.Equal(t, int64(12345), body.TokensUsed)
	require.Equal(t, "thread-1", body.ThreadID)
	require.Contains(t, body.IdempotencyKey, testTaskID)
	require.Contains(t, out.String(), "tokens_used=12345")
}
