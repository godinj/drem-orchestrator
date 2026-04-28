package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// GET /api/messages
// ---------------------------------------------------------------------------

// TestGetMessages_RequiresFromAndTo verifies that GET /api/messages returns 400
// when the from or to query parameters are missing.
func TestGetMessages_RequiresFromAndTo(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	cases := []struct {
		name string
		url  string
	}{
		{"missing both", "/api/messages"},
		{"missing from", "/api/messages?to=ceo"},
		{"missing to", "/api/messages?from=ceo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}

// TestGetMessages_ReturnsBetweenAgents verifies that GET returns messages
// exchanged between two agents.
func TestGetMessages_ReturnsBetweenAgents(t *testing.T) {
	store := testutil.NewTestStore(t)

	// Create a message from ceo to cfo.
	msg := &csuite.CsuiteInboxMessage{
		FromAgent: "ceo",
		ToAgent:   "cfo",
		Subject:   "budget review",
		Body:      "please review Q1",
		Priority:  csuite.PriorityNormal,
		Type:      csuite.MessageTypeRequest,
	}
	if err := store.CreateMessage(msg); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	h := messagesHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/messages?from=ceo&to=cfo", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var msgs []messageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Subject != "budget review" {
		t.Errorf("subject = %q, want %q", msgs[0].Subject, "budget review")
	}
}

// TestGetMessages_AgentAliasReturnsOperatorConversation verifies the mobile PRD
// shorthand: /api/messages?agent=<name> means operator <-> that agent.
func TestGetMessages_AgentAliasReturnsOperatorConversation(t *testing.T) {
	store := testutil.NewTestStore(t)
	for _, msg := range []*csuite.CsuiteInboxMessage{
		{
			FromAgent: "operator",
			ToAgent:   "kyle",
			Subject:   "chat",
			Body:      "status?",
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeRequest,
		},
		{
			FromAgent: "alex",
			ToAgent:   "operator",
			Subject:   "noise",
			Body:      "not this thread",
			Priority:  csuite.PriorityNormal,
			Type:      csuite.MessageTypeStatus,
		},
	} {
		if err := store.CreateMessage(msg); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	h := messagesHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/messages?agent=kyle", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var msgs []messageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].ToAgent != "kyle" {
		t.Errorf("to_agent = %q, want \"kyle\"", msgs[0].ToAgent)
	}
}

// TestGetMessages_EmptyResult verifies that GET returns an empty array (not null)
// when no messages match.
func TestGetMessages_EmptyResult(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/messages?from=ceo&to=cfo", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var msgs []messageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msgs == nil {
		t.Error("response must be [] not null for empty result")
	}
	if len(msgs) != 0 {
		t.Errorf("len(msgs) = %d, want 0", len(msgs))
	}
}

// TestGetMessages_MethodNotAllowed verifies that PUT/DELETE etc. return 405.
func TestGetMessages_MethodNotAllowed(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/messages", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/messages
// ---------------------------------------------------------------------------

// TestPostMessage_Success verifies that POST /api/messages creates a message
// and returns 201 with the message payload.
func TestPostMessage_Success(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	body := `{"from_agent":"ceo","to_agent":"cfo","subject":"hello","body":"world"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var msg messageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.FromAgent != "ceo" {
		t.Errorf("from_agent = %q, want \"ceo\"", msg.FromAgent)
	}
	if msg.ToAgent != "cfo" {
		t.Errorf("to_agent = %q, want \"cfo\"", msg.ToAgent)
	}
	if msg.Subject != "hello" {
		t.Errorf("subject = %q, want \"hello\"", msg.Subject)
	}
	if msg.Type != string(csuite.MessageTypeRequest) {
		t.Errorf("type = %q, want %q", msg.Type, csuite.MessageTypeRequest)
	}
}

// TestPostMessage_CompactMobileShape verifies the mobile-friendly send payload:
// {"to":"kyle","body":"status"}. Defaults fill operator/chat/normal/request.
func TestPostMessage_CompactMobileShape(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	body := `{"to":"kyle","body":"status"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var msg messageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.FromAgent != "operator" {
		t.Errorf("from_agent = %q, want \"operator\"", msg.FromAgent)
	}
	if msg.ToAgent != "kyle" {
		t.Errorf("to_agent = %q, want \"kyle\"", msg.ToAgent)
	}
	if msg.Subject != "chat" {
		t.Errorf("subject = %q, want \"chat\"", msg.Subject)
	}
	if msg.Type != string(csuite.MessageTypeRequest) {
		t.Errorf("type = %q, want %q", msg.Type, csuite.MessageTypeRequest)
	}
}

// TestPostMessage_InvalidJSON verifies that a malformed body returns 400.
func TestPostMessage_InvalidJSON(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestPostMessage_MissingRequiredFields verifies that missing from_agent/to_agent/subject
// returns 400.
func TestPostMessage_MissingRequiredFields(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	body := `{"from_agent":"ceo","body":"no subject or to"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestPostMessage_InvalidPriority verifies that an invalid priority returns 400.
func TestPostMessage_InvalidPriority(t *testing.T) {
	store := testutil.NewTestStore(t)
	h := messagesHandler(store, nil)

	body := `{"from_agent":"ceo","to_agent":"cfo","subject":"test","priority":"critical"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestPostMessage_BroadcastsToHub verifies that POST /api/messages broadcasts
// the new message to WebSocket clients via the Hub.
func TestPostMessage_BroadcastsToHub(t *testing.T) {
	store := testutil.NewTestStore(t)
	hub := NewHub()
	h := messagesHandler(store, hub)

	body := `{"from_agent":"ceo","to_agent":"cfo","subject":"broadcast test","body":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	// Hub broadcast is best-effort; we verify no panic occurs with zero clients.
	// Full broadcast testing is covered in the WebSocket integration tests.
}
