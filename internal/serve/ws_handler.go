package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// wsHandler returns an HTTP handler that upgrades connections to WebSocket
// and manages the read/write loop.
//
// Authentication: accepts the bearer token as a ?token= query parameter
// (since browsers cannot set custom headers on WebSocket connections) or
// via the standard Authorization: Bearer <token> header.
//
// Query parameters:
//
//	token  — bearer token for authentication
//	agent  — optional agent name to associate with this connection
//
// Inbound event types:
//
//	send_message — creates a new message and broadcasts it
//	send         — PRD-compatible shorthand: {"type":"send","to":"kyle","body":"status"}
//	ping         — responds with {"type":"pong"}
func wsHandler(hub *Hub, store dashboardStore, token string, disableAuth bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Authenticate — query param takes precedence, then Authorization header.
		if !disableAuth {
			tok := r.URL.Query().Get("token")
			if tok == "" {
				auth := r.Header.Get("Authorization")
				const prefix = "Bearer "
				if strings.HasPrefix(auth, prefix) {
					tok = auth[len(prefix):]
				}
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(token)) != 1 {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}

		agent := r.URL.Query().Get("agent")

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Allow all origins — the mobile PWA client may connect from
			// any origin. Auth is handled via bearer token, not CORS.
			InsecureSkipVerify: true,
		})
		if err != nil {
			log.Printf("ws accept: %v", err)
			return
		}
		defer conn.CloseNow() //nolint:errcheck

		client := &wsConn{conn: conn, agent: agent}
		hub.Register(client)
		defer hub.Unregister(client)

		// Send a welcome event so the client knows the connection is live.
		welcome, _ := json.Marshal(wsEvent{Type: "connected"})
		if err := conn.Write(r.Context(), websocket.MessageText, welcome); err != nil {
			return
		}

		// Read loop — blocks until the connection is closed or an error occurs.
		ctx := r.Context()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				// Normal closure or context cancellation — not an error.
				return
			}
			if typ != websocket.MessageText {
				continue
			}

			var evt wsEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				errMsg, _ := json.Marshal(wsEvent{Type: "error", Data: json.RawMessage(`"invalid JSON"`)})
				conn.Write(ctx, websocket.MessageText, errMsg) //nolint:errcheck
				continue
			}

			switch evt.Type {
			case "send_message":
				handleWsSendMessage(ctx, hub, store, conn, evt.Data)
			case "send":
				handleWsSend(ctx, hub, store, conn, data, evt.Data)
			case "ping":
				pong, _ := json.Marshal(wsEvent{Type: "pong"})
				conn.Write(ctx, websocket.MessageText, pong) //nolint:errcheck
			default:
				errMsg, _ := json.Marshal(wsEvent{Type: "error", Data: json.RawMessage(`"unknown event type"`)})
				conn.Write(ctx, websocket.MessageText, errMsg) //nolint:errcheck
			}
		}
	})
}

// handleWsSendMessage processes a send_message event from a WebSocket client.
// It persists the message via the store and broadcasts it to all connected clients.
func handleWsSendMessage(ctx context.Context, hub *Hub, store dashboardStore, conn *websocket.Conn, data json.RawMessage) {
	var req sendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		errMsg, _ := json.Marshal(wsEvent{Type: "error", Data: json.RawMessage(`"invalid send_message data"`)})
		conn.Write(ctx, websocket.MessageText, errMsg) //nolint:errcheck
		return
	}

	msg, err := createMessageFromRequest(req, store)
	if err != nil {
		errData, _ := json.Marshal(err.Error())
		errMsg, _ := json.Marshal(wsEvent{Type: "error", Data: errData})
		conn.Write(ctx, websocket.MessageText, errMsg) //nolint:errcheck
		return
	}

	broadcastNewMessage(hub, *msg)
}

// handleWsSend processes the PRD's original compact event shape:
//
//	{"type":"send","to":"kyle","body":"status"}
//
// It also accepts a nested data payload with the same fields. Messages default
// to operator -> <to>, subject "chat", normal priority, and request type.
func handleWsSend(ctx context.Context, hub *Hub, store dashboardStore, conn *websocket.Conn, full json.RawMessage, nested json.RawMessage) {
	var req struct {
		FromAgent string `json:"from_agent"`
		ToAgent   string `json:"to_agent"`
		To        string `json:"to"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
		Priority  string `json:"priority"`
	}
	payload := full
	if len(nested) > 0 {
		payload = nested
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		errMsg, _ := json.Marshal(wsEvent{Type: "error", Data: json.RawMessage(`"invalid send data"`)})
		conn.Write(ctx, websocket.MessageText, errMsg) //nolint:errcheck
		return
	}

	from := req.FromAgent
	if from == "" {
		from = "operator"
	}
	to := req.ToAgent
	if to == "" {
		to = req.To
	}
	subject := req.Subject
	if subject == "" {
		subject = "chat"
	}
	priority := req.Priority
	if priority == "" {
		priority = "normal"
	}

	msg, err := createMessageFromRequest(sendMessageRequest{
		FromAgent: from,
		ToAgent:   to,
		Subject:   subject,
		Body:      req.Body,
		Priority:  priority,
		Type:      "request",
	}, store)
	if err != nil {
		errData, _ := json.Marshal(err.Error())
		errMsg, _ := json.Marshal(wsEvent{Type: "error", Data: errData})
		conn.Write(ctx, websocket.MessageText, errMsg) //nolint:errcheck
		return
	}

	broadcastNewMessage(hub, *msg)
}
