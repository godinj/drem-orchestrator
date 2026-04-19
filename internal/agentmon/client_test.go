package agentmon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// TestHTTPIngestorPostsBatch verifies the wire format: one POST per
// Ingest call, body matches orchdto.IngestRequest with a flattened
// record per entry, token header present.
func TestHTTPIngestorPostsBatch(t *testing.T) {
	const token = "tok-1"
	var gotBody []byte
	var gotToken string
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/internal/logs", r.URL.Path)
		gotToken = r.Header.Get("X-Drem-Agentmon-Token")
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		_ = json.NewEncoder(w).Encode(orchdto.IngestResponse{Accepted: 1})
	}))
	defer srv.Close()

	ing := &HTTPIngestor{OrchURL: srv.URL, Token: token}
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	err := ing.Ingest(context.Background(), []IngestRecord{{
		Type:        recordTypeCommit,
		ContainerID: "c1",
		WorkerID:    "w1",
		Timestamp:   now,
		Payload:     map[string]any{"sha": "abc", "branch": "main", "message": "wip"},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, token, gotToken)

	var decoded orchdto.IngestRequest
	require.NoError(t, json.Unmarshal(gotBody, &decoded))
	require.Len(t, decoded.Records, 1)

	// The flattened record must carry the envelope fields at the top level.
	var flat map[string]any
	require.NoError(t, json.Unmarshal(decoded.Records[0], &flat))
	require.Equal(t, "commit", flat["type"])
	require.Equal(t, "c1", flat["container_id"])
	require.Equal(t, "w1", flat["worker_id"])
	require.Equal(t, "abc", flat["sha"])
	require.Equal(t, "main", flat["branch"])
}

// TestHTTPIngestorSurfacesNon2xx asserts non-2xx responses become
// errors so the tail loop can log and drop per the scope note.
func TestHTTPIngestorSurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()

	ing := &HTTPIngestor{OrchURL: srv.URL, Token: "t"}
	err := ing.Ingest(context.Background(), []IngestRecord{{
		Type:    recordTypeHeartbeat,
		Payload: map[string]any{"agent_id": "x"},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "status 400")
}

// TestHTTPIngestorEmptyBatchIsNoop short-circuits the network call when
// there is nothing to send — important because the tail loop can drive
// a spurious flush under clock edge cases.
func TestHTTPIngestorEmptyBatchIsNoop(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { calls++ }))
	defer srv.Close()

	ing := &HTTPIngestor{OrchURL: srv.URL, Token: "t"}
	require.NoError(t, ing.Ingest(context.Background(), nil))
	require.Equal(t, 0, calls)
}
