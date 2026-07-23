package orchclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func TestSubmitCodexGoalUsageUsesAttributedTaskEndpoint(t *testing.T) {
	taskID := uuid.New()
	var path, actor, token string
	var body orchdto.SubmitCodexGoalUsageRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, actor, token = r.URL.EscapedPath(), r.Header.Get("X-Drem-Actor"), r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"usage-1","task_id":"` + taskID.String() + `","tokens_used":123}`))
	}))
	defer ts.Close()

	req := orchdto.SubmitCodexGoalUsageRequest{
		Actor: "codex:thread-42", ThreadID: "thread-42", GoalObjective: "supervise task",
		GoalStatus: "complete", TokensUsed: 123, ElapsedMS: 456, IdempotencyKey: "goal-1",
	}
	got, err := New(ts.URL).WithToken("secret").WithActor(req.Actor).
		SubmitCodexGoalUsage(context.Background(), "canvas local", taskID, req)
	require.NoError(t, err)
	require.Equal(t, int64(123), got.TokensUsed)
	require.Equal(t, "/projects/canvas%20local/tasks/"+taskID.String()+"/codex-usage", path)
	require.Equal(t, req.Actor, actor)
	require.Equal(t, "Bearer secret", token)
	require.Equal(t, req, body)
}
