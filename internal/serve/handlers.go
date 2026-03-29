package serve

import (
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
}

// agentsHandler returns a handler for GET /api/agents.
// It queries the store for the agent dashboard and returns the rows as JSON.
func agentsHandler(s dashboardStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	})
}
