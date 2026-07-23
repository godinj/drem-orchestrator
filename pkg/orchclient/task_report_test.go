package orchclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTaskReportUsesProjectScopedReportEndpoint(t *testing.T) {
	taskID := uuid.New()
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project":"canvas-local","task":{"id":"` + taskID.String() + `","title":"canary","status":"integration_ready"},"generated_at":"2026-07-23T00:00:00Z","wall_duration_ms":10,"children":[],"phases":[],"attempts":[],"totals":{},"measurement_coverage":{}}`))
	}))
	defer ts.Close()

	report, err := New(ts.URL).TaskReport(context.Background(), "canvas local", taskID)
	require.NoError(t, err)
	require.Equal(t, "/projects/canvas%20local/tasks/"+taskID.String()+"/report", path)
	require.Equal(t, "canary", report.Task.Title)
}
