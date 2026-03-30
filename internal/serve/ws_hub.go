package serve

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/coder/websocket"
	"github.com/godinj/drem-orchestrator/internal/csuite"
)

// Hub manages active WebSocket connections and broadcasts messages.
// It is safe for concurrent use.
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsConn]struct{}
}

// wsConn is a registered WebSocket connection with its associated agent name.
type wsConn struct {
	conn  *websocket.Conn
	agent string // the agent name this connection is associated with
}

// NewHub creates a Hub ready to accept connections.
func NewHub() *Hub {
	return &Hub{clients: make(map[*wsConn]struct{})}
}

// Register adds a client to the hub.
func (h *Hub) Register(c *wsConn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *wsConn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// Broadcast sends data to all connected clients. Errors on individual
// connections are logged but do not prevent delivery to other clients.
func (h *Hub) Broadcast(ctx context.Context, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
			log.Printf("ws broadcast error: %v", err)
		}
	}
}

// BroadcastToAgent sends data only to clients connected as the given agent.
func (h *Hub) BroadcastToAgent(ctx context.Context, agent string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.agent == agent {
			if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
				log.Printf("ws broadcast to %s error: %v", agent, err)
			}
		}
	}
}

// ClientCount returns the number of currently connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// wsEvent is the JSON envelope for all WebSocket messages in both directions.
type wsEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// broadcastNewMessage serialises a new-message event and sends it to all
// connected WebSocket clients. Called after a message is persisted via REST
// or WebSocket.
func broadcastNewMessage(hub *Hub, msg csuite.CsuiteInboxMessage) {
	data, err := json.Marshal(toMessageResponse(msg))
	if err != nil {
		return
	}
	evt, err := json.Marshal(wsEvent{Type: "new_message", Data: data})
	if err != nil {
		return
	}
	hub.Broadcast(context.Background(), evt)
}
