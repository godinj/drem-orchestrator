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

// WSEvent is the JSON envelope for WebSocket messages.
type WSEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
