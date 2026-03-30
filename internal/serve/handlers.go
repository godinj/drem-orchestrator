package serve

import (
	"encoding/json"
	"net/http"
	"time"
)

// agentResponse is the JSON shape returned by GET /api/agents.
type agentResponse struct {
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	ContextPercent  int        `json:"context_percent"`
	CurrentActivity string     `json:"current_activity"`
	UnreadCount     int        `json:"unread_count"`
	LatestInbox     *time.Time `json:"latest_inbox,omitempty"`
}

// healthHandler serves GET /api/health.
// Returns 200 with {"status":"ok"}.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
}

// agentsHandler returns a handler for GET /api/agents.
// It queries the store for the agent dashboard and returns the rows as JSON.
func agentsHandler(s dashboardStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.AgentDashboard()
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		out := make([]agentResponse, len(rows))
		for i, row := range rows {
			out[i] = agentResponse{
				Name:            row.Agent.Name,
				Status:          row.Agent.Status.String(),
				ContextPercent:  row.Agent.ContextPercent,
				CurrentActivity: row.Agent.CurrentActivity,
				UnreadCount:     row.UnreadCount,
				LatestInbox:     row.LatestInbox,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out) //nolint:errcheck
	})
}
