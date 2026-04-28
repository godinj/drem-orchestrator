package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Health handler
// ---------------------------------------------------------------------------

// TestHealthHandler_Status verifies GET /api/health returns 200.
func TestHealthHandler_Status(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	healthHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestHealthHandler_JSONBody verifies the response is valid JSON with a
// "status" key set to "ok".
func TestHealthHandler_JSONBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	healthHandler(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v — %s", err, w.Body.Bytes())
	}
	status, ok := body["status"]
	if !ok {
		t.Fatalf("JSON body missing \"status\" key: %s", w.Body.Bytes())
	}
	if status != "ok" {
		t.Errorf("status = %q, want \"ok\"", status)
	}
}

// TestHealthHandler_ContentType verifies Content-Type is application/json.
func TestHealthHandler_ContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	healthHandler(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// ---------------------------------------------------------------------------
// Agents handler
// ---------------------------------------------------------------------------

// TestAgentsHandler_EmptyStore verifies that when no agents exist the
// handler returns 200 with an empty JSON array (not null).
func TestAgentsHandler_EmptyStore(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := agentsHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var rows []any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("body is not valid JSON array: %v — %s", err, w.Body.Bytes())
	}
	if rows == nil {
		t.Error("response must be [] not null for empty agent list")
	}
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0", len(rows))
	}
}

// TestAgentsHandler_ContentType verifies Content-Type is application/json.
func TestAgentsHandler_ContentType(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := agentsHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestAgentsHandler_ReturnsAgentRows verifies that agents created in the store
// appear in the response with their name, status, and unread_count fields.
func TestAgentsHandler_ReturnsAgentRows(t *testing.T) {
	store := testutil.NewTestStore(t)

	// Create two agents.
	ceo := &csuite.CsuiteAgent{Name: "ceo", Status: csuite.AgentMonOnline}
	cfo := &csuite.CsuiteAgent{Name: "cfo", Status: csuite.AgentMonOffline}
	for _, a := range []*csuite.CsuiteAgent{ceo, cfo} {
		if err := store.CreateAgent(a); err != nil {
			t.Fatalf("CreateAgent %q: %v", a.Name, err)
		}
	}

	h := agentsHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v — %s", err, w.Body.Bytes())
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}

	// Index by name for easier assertions.
	byName := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		byName[name] = row
	}

	for _, tc := range []struct{ name, status string }{
		{"ceo", "online"},
		{"cfo", "offline"},
	} {
		row, ok := byName[tc.name]
		if !ok {
			t.Errorf("agent %q missing from response", tc.name)
			continue
		}
		if row["status"] != tc.status {
			t.Errorf("agent %q status = %q, want %q", tc.name, row["status"], tc.status)
		}
		if _, ok := row["unread_count"]; !ok {
			t.Errorf("agent %q missing unread_count field", tc.name)
		}
	}
}

// TestAgentsHandler_UnreadCounts verifies that the unread_count for an agent
// reflects only that agent's non-archived messages.
func TestAgentsHandler_UnreadCounts(t *testing.T) {
	store := testutil.NewTestStore(t)

	// Create one agent that will have messages.
	ceo := &csuite.CsuiteAgent{Name: "ceo", Status: csuite.AgentMonOnline}
	if err := store.CreateAgent(ceo); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Send two messages to ceo.
	for i := range 2 {
		msg := &csuite.CsuiteInboxMessage{
			FromAgent: "orchestrator",
			ToAgent:   "ceo",
			Subject:   "update",
			Body:      strings.Repeat("x", i+1),
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
		}
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	h := agentsHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}

	unread, ok := rows[0]["unread_count"].(float64)
	if !ok {
		t.Fatalf("unread_count type = %T, want float64", rows[0]["unread_count"])
	}
	if int(unread) != 2 {
		t.Errorf("unread_count = %d, want 2", int(unread))
	}
}

// TestAgentsHandler_RequiredFields verifies each row in the response contains
// the required fields: name, status, context_percent, current_activity,
// unread_count.
func TestAgentsHandler_RequiredFields(t *testing.T) {
	store := testutil.NewTestStore(t)

	agent := &csuite.CsuiteAgent{
		Name:            "coo",
		Status:          csuite.AgentMonOnline,
		ContextPercent:  42,
		CurrentActivity: "reviewing quarterly plan",
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	h := agentsHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]

	for _, field := range []string{"name", "status", "context_percent", "current_activity", "unread_count", "ack_count"} {
		if _, ok := row[field]; !ok {
			t.Errorf("row missing required field %q", field)
		}
	}

	if row["name"] != "coo" {
		t.Errorf("name = %q, want \"coo\"", row["name"])
	}
	if row["context_percent"] != float64(42) {
		t.Errorf("context_percent = %v, want 42", row["context_percent"])
	}
	if row["current_activity"] != "reviewing quarterly plan" {
		t.Errorf("current_activity = %q", row["current_activity"])
	}
}
