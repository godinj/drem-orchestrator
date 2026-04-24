package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/google/uuid"
)

// messageResponse is the JSON shape for a single inbox message.
type messageResponse struct {
	ID        string `json:"id"`
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Priority  string `json:"priority"`
	Type      string `json:"type"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at"`
}

// sendMessageRequest is the JSON body for POST /api/messages and the
// "send_message" WebSocket event.
type sendMessageRequest struct {
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Priority  string `json:"priority"`
	Type      string `json:"type"`
}

// messagesHandler returns a handler for GET and POST /api/messages.
//
// GET  /api/messages?from=<agent>&to=<agent>[&limit=N][&before_id=<uuid>]
// GET  /api/messages?agent=<agent>[&limit=N][&before=<uuid>]
//
//	Returns cursor-paginated messages between two agents (newest first).
//	Defaults to 50 messages per page. Pass before_id to fetch the next page.
//
// POST /api/messages
//
//	Creates a new inbox message. Body must be JSON matching sendMessageRequest.
//	Returns 201 with the created message on success. If a Hub is provided,
//	the new message is broadcast to all connected WebSocket clients.
func messagesHandler(s dashboardStore, hub *Hub) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleGetMessages(w, r, s)
		case http.MethodPost:
			handlePostMessage(w, r, s, hub)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
}

func handleGetMessages(w http.ResponseWriter, r *http.Request, s dashboardStore) {
	q := r.URL.Query()
	agent1 := q.Get("from")
	agent2 := q.Get("to")
	if agent1 == "" && agent2 == "" && q.Get("agent") != "" {
		agent1 = "operator"
		agent2 = q.Get("agent")
	}
	if agent1 == "" || agent2 == "" {
		writeJSONError(w, http.StatusBadRequest, "from and to query parameters or agent query parameter are required")
		return
	}

	limit := 0
	if ls := q.Get("limit"); ls != "" {
		n, err := strconv.Atoi(ls)
		if err != nil || n < 0 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		limit = n
	}

	var beforeID uuid.UUID
	if bid := q.Get("before_id"); bid != "" {
		id, err := uuid.Parse(bid)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "before_id must be a valid UUID")
			return
		}
		beforeID = id
	} else if bid := q.Get("before"); bid != "" {
		id, err := uuid.Parse(bid)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "before must be a valid UUID")
			return
		}
		beforeID = id
	}

	msgs, err := s.GetMessagesBetween(agent1, agent2, limit, beforeID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store unavailable")
		return
	}

	resp := make([]messageResponse, len(msgs))
	for i, m := range msgs {
		resp[i] = toMessageResponse(m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func handlePostMessage(w http.ResponseWriter, r *http.Request, s dashboardStore, hub *Hub) {
	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	msg, err := createMessageFromRequest(req, s)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toMessageResponse(*msg)) //nolint:errcheck

	// Broadcast to WebSocket clients after responding.
	if hub != nil {
		broadcastNewMessage(hub, *msg)
	}
}

// createMessageFromRequest validates the request, persists the message, and
// returns it. Shared between the REST handler and the WebSocket handler.
func createMessageFromRequest(req sendMessageRequest, s dashboardStore) (*csuite.CsuiteInboxMessage, error) {
	req = normalizeSendMessageRequest(req)

	priority := csuite.PriorityNormal
	if req.Priority != "" {
		p, err := csuite.ParseInboxPriority(req.Priority)
		if err != nil {
			return nil, err
		}
		priority = p
	}

	msgType := csuite.MessageTypeRequest
	if req.Type != "" {
		t, err := csuite.ParseInboxMessageType(req.Type)
		if err != nil {
			return nil, err
		}
		msgType = t
	}

	msg := &csuite.CsuiteInboxMessage{
		FromAgent: req.FromAgent,
		ToAgent:   req.ToAgent,
		Subject:   req.Subject,
		Body:      req.Body,
		Priority:  priority,
		Type:      msgType,
	}

	if err := s.CreateMessage(msg); err != nil {
		return nil, err
	}

	// Write an inbox .md file so disk-based agent prompts can read it,
	// then touch .signal so the watcher wakes the agent.
	writeInboxFile(req, msg.CreatedAt)
	touchSignalFile(req.ToAgent)

	return msg, nil
}

func normalizeSendMessageRequest(req sendMessageRequest) sendMessageRequest {
	if req.FromAgent == "" {
		req.FromAgent = "operator"
	}
	if req.ToAgent == "" {
		req.ToAgent = req.To
	}
	if req.Subject == "" {
		req.Subject = "chat"
	}
	if req.Priority == "" {
		req.Priority = "normal"
	}
	if req.Type == "" {
		req.Type = "request"
	}
	return req
}

// writeInboxFile writes a .md file to the recipient's disk inbox in the same
// format as csuite-proto.sh's csuite_send, so agent prompts that read from
// disk can see messages sent via the bridge API.
func writeInboxFile(req sendMessageRequest, createdAt time.Time) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	inboxDir := filepath.Join(home, ".drem-csuite", req.ToAgent, "inbox")
	_ = os.MkdirAll(inboxDir, 0o700)

	ts := createdAt.UTC()
	filename := fmt.Sprintf("%s-%s.md", ts.Format("20060102-150405"), req.FromAgent)
	filePath := filepath.Join(inboxDir, filename)

	// Map bridge priority/type to proto values for consistency.
	priority := req.Priority
	if priority == "" {
		priority = "normal"
	}
	msgType := req.Type
	if msgType == "" {
		msgType = "request"
	}

	content := fmt.Sprintf(`---
from: %s
to: %s
timestamp: %s
subject: "%s"
priority: %s
type: %s
---

%s
`, req.FromAgent, req.ToAgent, ts.Format(time.RFC3339), req.Subject, priority, msgType, req.Body)

	_ = os.WriteFile(filePath, []byte(content), 0o644)
}

// touchSignalFile creates a .signal file in the recipient's inbox directory
// to trigger the watcher to spawn a turn for that agent.
func touchSignalFile(agent string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	signalPath := filepath.Join(home, ".drem-csuite", agent, "inbox", ".signal")
	_ = os.MkdirAll(filepath.Dir(signalPath), 0o700)
	f, err := os.Create(signalPath)
	if err == nil {
		f.Close()
	}
}

func toMessageResponse(m csuite.CsuiteInboxMessage) messageResponse {
	return messageResponse{
		ID:        m.ID.String(),
		FromAgent: m.FromAgent,
		ToAgent:   m.ToAgent,
		Subject:   m.Subject,
		Body:      m.Body,
		Priority:  string(m.Priority),
		Type:      string(m.Type),
		Archived:  m.Archived,
		CreatedAt: m.CreatedAt.Format(time.RFC3339Nano),
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
