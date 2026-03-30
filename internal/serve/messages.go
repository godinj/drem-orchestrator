package serve

import (
	"encoding/json"
	"net/http"
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
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Priority  string `json:"priority"`
	Type      string `json:"type"`
}

// messagesHandler returns a handler for GET and POST /api/messages.
//
// GET  /api/messages?from=<agent>&to=<agent>[&limit=N][&before_id=<uuid>]
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
	if agent1 == "" || agent2 == "" {
		writeJSONError(w, http.StatusBadRequest, "from and to query parameters are required")
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
	priority := csuite.PriorityNormal
	if req.Priority != "" {
		p, err := csuite.ParseInboxPriority(req.Priority)
		if err != nil {
			return nil, err
		}
		priority = p
	}

	msgType := csuite.MessageTypeStatus
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
	return msg, nil
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
