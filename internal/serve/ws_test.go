package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Hub unit tests
// ---------------------------------------------------------------------------

// TestHub_RegisterAndUnregister verifies the hub tracks and removes clients.
func TestHub_RegisterAndUnregister(t *testing.T) {
	hub := NewHub()
	if hub.ClientCount() != 0 {
		t.Fatalf("ClientCount = %d, want 0", hub.ClientCount())
	}

	c := &wsConn{agent: "ceo"}
	hub.Register(c)
	if hub.ClientCount() != 1 {
		t.Errorf("after register: ClientCount = %d, want 1", hub.ClientCount())
	}

	hub.Unregister(c)
	if hub.ClientCount() != 0 {
		t.Errorf("after unregister: ClientCount = %d, want 0", hub.ClientCount())
	}
}

// TestHub_UnregisterIdempotent verifies that unregistering a non-existent
// client does not panic.
func TestHub_UnregisterIdempotent(t *testing.T) {
	hub := NewHub()
	c := &wsConn{agent: "ghost"}
	hub.Unregister(c) // should not panic
}

// ---------------------------------------------------------------------------
// WebSocket integration tests
// ---------------------------------------------------------------------------

// startTestServer creates a test bridge server with a fresh store and returns
// the server, its base URL, the token, and the store.
func startTestServer(t *testing.T) (*Server, string, string, *csuite.Store) {
	t.Helper()
	const token = "ws-test-token"
	store := testutil.NewTestStore(t)
	s := New(Config{Token: token, Addr: "127.0.0.1:0", Store: store})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
	return s, "http://" + s.ListenAddr(), token, store
}

// dialWS opens a WebSocket connection to the test server.
func dialWS(t *testing.T, baseURL, token, agent string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := fmt.Sprintf("ws://%s/api/ws?token=%s&agent=%s",
		baseURL[len("http://"):], token, agent)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// readEvent reads one wsEvent from the connection with a timeout.
func readEvent(t *testing.T, conn *websocket.Conn) wsEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var evt wsEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		t.Fatalf("unmarshal ws event: %v — data: %s", err, data)
	}
	return evt
}

// TestWS_ConnectReceivesWelcome verifies that connecting to /api/ws returns
// a "connected" event.
func TestWS_ConnectReceivesWelcome(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)
	conn := dialWS(t, baseURL, token, "ceo")

	evt := readEvent(t, conn)
	if evt.Type != "connected" {
		t.Errorf("event type = %q, want \"connected\"", evt.Type)
	}
}

// TestWS_AliasConnectReceivesWelcome verifies the PRD-compatible /ws alias
// stays mounted alongside the existing /api/ws endpoint.
func TestWS_AliasConnectReceivesWelcome(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := fmt.Sprintf("ws://%s/ws?token=%s&agent=operator",
		baseURL[len("http://"):], token)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws alias: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	evt := readEvent(t, conn)
	if evt.Type != "connected" {
		t.Errorf("event type = %q, want \"connected\"", evt.Type)
	}
}

// TestWS_UnauthorizedRejected verifies that connecting without a valid token
// returns 401 (connection refused).
func TestWS_UnauthorizedRejected(t *testing.T) {
	_, baseURL, _, _ := startTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := fmt.Sprintf("ws://%s/api/ws?token=wrong", baseURL[len("http://"):])
	_, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("expected error for unauthorized WS connection")
	}
}

// TestWS_PingPong verifies the ping/pong event.
func TestWS_PingPong(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)
	conn := dialWS(t, baseURL, token, "ceo")

	// Consume the welcome event.
	readEvent(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ping, _ := json.Marshal(wsEvent{Type: "ping"})
	if err := conn.Write(ctx, websocket.MessageText, ping); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	evt := readEvent(t, conn)
	if evt.Type != "pong" {
		t.Errorf("event type = %q, want \"pong\"", evt.Type)
	}
}

// TestWS_SendMessageBroadcasts verifies that sending a message via WebSocket
// persists it and broadcasts to all connected clients.
func TestWS_SendMessageBroadcasts(t *testing.T) {
	_, baseURL, token, store := startTestServer(t)

	// Connect two clients.
	conn1 := dialWS(t, baseURL, token, "ceo")
	conn2 := dialWS(t, baseURL, token, "cfo")

	// Consume welcome events.
	readEvent(t, conn1)
	readEvent(t, conn2)

	// Client 1 sends a message.
	msgData, _ := json.Marshal(sendMessageRequest{
		FromAgent: "ceo",
		ToAgent:   "cfo",
		Subject:   "ws test",
		Body:      "hello from ws",
	})
	evt, _ := json.Marshal(wsEvent{Type: "send_message", Data: msgData})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn1.Write(ctx, websocket.MessageText, evt); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Both clients should receive the new_message broadcast.
	evt1 := readEvent(t, conn1)
	if evt1.Type != "new_message" {
		t.Errorf("conn1 event type = %q, want \"new_message\"", evt1.Type)
	}

	evt2 := readEvent(t, conn2)
	if evt2.Type != "new_message" {
		t.Errorf("conn2 event type = %q, want \"new_message\"", evt2.Type)
	}

	// Verify the broadcast contains the correct message data.
	var msg messageResponse
	if err := json.Unmarshal(evt1.Data, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.FromAgent != "ceo" {
		t.Errorf("from_agent = %q, want \"ceo\"", msg.FromAgent)
	}
	if msg.Subject != "ws test" {
		t.Errorf("subject = %q, want \"ws test\"", msg.Subject)
	}

	// Verify the message was persisted in the store.
	msgs, err := store.GetMessagesBetween("ceo", "cfo", 10, [16]byte{})
	if err != nil {
		t.Fatalf("GetMessagesBetween: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(msgs))
	}
	if msgs[0].Subject != "ws test" {
		t.Errorf("persisted subject = %q, want \"ws test\"", msgs[0].Subject)
	}
}

// TestWS_SendCompactEvent verifies the original mobile PRD event shape:
// {"type":"send","to":"kyle","body":"status"}.
func TestWS_SendCompactEvent(t *testing.T) {
	_, baseURL, token, store := startTestServer(t)
	conn := dialWS(t, baseURL, token, "operator")
	readEvent(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"send","to":"kyle","body":"status"}`)); err != nil {
		t.Fatalf("write compact send: %v", err)
	}

	evt := readEvent(t, conn)
	if evt.Type != "new_message" {
		t.Fatalf("event type = %q, want \"new_message\"", evt.Type)
	}

	var msg messageResponse
	if err := json.Unmarshal(evt.Data, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.FromAgent != "operator" {
		t.Errorf("from_agent = %q, want \"operator\"", msg.FromAgent)
	}
	if msg.ToAgent != "kyle" {
		t.Errorf("to_agent = %q, want \"kyle\"", msg.ToAgent)
	}
	if msg.Body != "status" {
		t.Errorf("body = %q, want \"status\"", msg.Body)
	}
	if msg.Type != string(csuite.MessageTypeRequest) {
		t.Errorf("type = %q, want %q", msg.Type, csuite.MessageTypeRequest)
	}

	msgs, err := store.GetMessagesBetween("operator", "kyle", 10, [16]byte{})
	if err != nil {
		t.Fatalf("GetMessagesBetween: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(msgs))
	}
}

// TestWS_RESTPostBroadcastsToWebSocket verifies that creating a message via
// POST /api/messages broadcasts it to connected WebSocket clients.
func TestWS_RESTPostBroadcastsToWebSocket(t *testing.T) {
	srv, baseURL, token, _ := startTestServer(t)

	// Connect a WebSocket client.
	conn := dialWS(t, baseURL, token, "cfo")
	readEvent(t, conn) // consume welcome

	// Wait a moment for the WebSocket connection to fully register.
	// This ensures the Hub has the client before the REST POST fires.
	time.Sleep(50 * time.Millisecond)

	// Verify client is registered before posting.
	if srv.Hub().ClientCount() != 1 {
		t.Fatalf("hub client count = %d, want 1", srv.Hub().ClientCount())
	}

	// POST a message via REST API.
	body := `{"from_agent":"ceo","to_agent":"cfo","subject":"rest broadcast","body":"hi via rest"}`
	url := baseURL + "/api/messages"
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/messages: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("REST POST status = %d, want 201", resp.StatusCode)
	}

	// WebSocket client should receive the broadcast.
	evt := readEvent(t, conn)
	if evt.Type != "new_message" {
		t.Errorf("event type = %q, want \"new_message\"", evt.Type)
	}

	var msg messageResponse
	if err := json.Unmarshal(evt.Data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Subject != "rest broadcast" {
		t.Errorf("subject = %q, want \"rest broadcast\"", msg.Subject)
	}
}

// TestWS_UnknownEventType verifies that sending an unknown event type returns
// an error event.
func TestWS_UnknownEventType(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)
	conn := dialWS(t, baseURL, token, "ceo")
	readEvent(t, conn) // welcome

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bad, _ := json.Marshal(wsEvent{Type: "foobar"})
	if err := conn.Write(ctx, websocket.MessageText, bad); err != nil {
		t.Fatalf("write: %v", err)
	}

	evt := readEvent(t, conn)
	if evt.Type != "error" {
		t.Errorf("event type = %q, want \"error\"", evt.Type)
	}
}

// TestWS_InvalidJSON verifies that sending non-JSON data returns an error event.
func TestWS_InvalidJSON(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)
	conn := dialWS(t, baseURL, token, "ceo")
	readEvent(t, conn) // welcome

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, []byte("not json")); err != nil {
		t.Fatalf("write: %v", err)
	}

	evt := readEvent(t, conn)
	if evt.Type != "error" {
		t.Errorf("event type = %q, want \"error\"", evt.Type)
	}
}

// TestWS_ClientDisconnectRemovesFromHub verifies that closing a WebSocket
// connection removes the client from the hub.
func TestWS_ClientDisconnectRemovesFromHub(t *testing.T) {
	srv, baseURL, token, _ := startTestServer(t)

	conn := dialWS(t, baseURL, token, "ceo")
	readEvent(t, conn) // welcome

	// Wait for registration.
	time.Sleep(50 * time.Millisecond)

	if srv.Hub().ClientCount() != 1 {
		t.Fatalf("hub count = %d, want 1 before disconnect", srv.Hub().ClientCount())
	}

	// Close the connection.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Give the server time to process the disconnect.
	time.Sleep(100 * time.Millisecond)

	if srv.Hub().ClientCount() != 0 {
		t.Errorf("hub count = %d, want 0 after disconnect", srv.Hub().ClientCount())
	}
}

// TestWS_MultipleClientsReceiveBroadcast verifies that broadcasting reaches
// all connected clients, not just one.
func TestWS_MultipleClientsReceiveBroadcast(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)

	// Connect three clients.
	conns := make([]*websocket.Conn, 3)
	for i := range conns {
		conns[i] = dialWS(t, baseURL, token, fmt.Sprintf("agent-%d", i))
		readEvent(t, conns[i]) // consume welcome
	}

	// Client 0 sends a message.
	msgData, _ := json.Marshal(sendMessageRequest{
		FromAgent: "agent-0",
		ToAgent:   "agent-1",
		Subject:   "multi test",
		Body:      "hello all",
	})
	evt, _ := json.Marshal(wsEvent{Type: "send_message", Data: msgData})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conns[0].Write(ctx, websocket.MessageText, evt); err != nil {
		t.Fatalf("write: %v", err)
	}

	// All three clients should receive the broadcast.
	for i, c := range conns {
		received := readEvent(t, c)
		if received.Type != "new_message" {
			t.Errorf("client %d: event type = %q, want \"new_message\"", i, received.Type)
		}
	}
}

// TestWS_AuthViaHeader verifies that WebSocket connections can authenticate
// via the Authorization header in addition to the query parameter.
func TestWS_AuthViaHeader(t *testing.T) {
	_, baseURL, token, _ := startTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := fmt.Sprintf("ws://%s/api/ws", baseURL[len("http://"):])
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + token},
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Should receive the welcome event.
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var evt wsEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != "connected" {
		t.Errorf("event type = %q, want \"connected\"", evt.Type)
	}
}
