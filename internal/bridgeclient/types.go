// Package bridgeclient provides an HTTP and WebSocket client for the
// drem-bridge C-Suite API server.
package bridgeclient

import "encoding/json"

// Agent is the JSON shape returned by GET /api/agents.
type Agent struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	ContextPercent  int    `json:"context_percent"`
	CurrentActivity string `json:"current_activity"`
	UnreadCount     int    `json:"unread_count"`
	LatestInbox     string `json:"latest_inbox,omitempty"`
	AckCount        int    `json:"ack_count"`
	LatestAck       string `json:"latest_ack,omitempty"`
}

// Message is the JSON shape for a single inbox message.
type Message struct {
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

// InboxQueueItem is the JSON shape returned by GET /api/inbox.
type InboxQueueItem struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// SendRequest is the JSON body for POST /api/messages and the
// "send_message" WebSocket event.
type SendRequest struct {
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Priority  string `json:"priority"`
	Type      string `json:"type"`
}

// InboxQueueActionRequest is the JSON body for POST /api/inbox/archive and
// POST /api/inbox/ignore.
type InboxQueueActionRequest struct {
	Agent  string `json:"agent"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// PersonaContainer is one allowlisted persona container returned by
// GET /api/personas/containers.
type PersonaContainer struct {
	Target  string `json:"target"`
	Service string `json:"service"`
	Status  string `json:"status"`
}

// PersonaContainersResponse is the JSON body returned by
// GET /api/personas/containers.
type PersonaContainersResponse struct {
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Compose   string             `json:"compose,omitempty"`
	Items     []PersonaContainer `json:"items"`
}

// PersonaControlRequest is the JSON body for POST /api/personas/control.
type PersonaControlRequest struct {
	Target string `json:"target"`
	Action string `json:"action"`
}

// PersonaControlResult is the JSON body returned by POST /api/personas/control.
type PersonaControlResult struct {
	Status   string   `json:"status"`
	Target   string   `json:"target"`
	Action   string   `json:"action"`
	Services []string `json:"services"`
}

// WSEvent is the JSON envelope for WebSocket messages.
type WSEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
