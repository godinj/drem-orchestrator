package bridgeclient

import (
	"context"
	"encoding/json"

	"github.com/coder/websocket"
)

// WSConn wraps a WebSocket connection to the bridge server.
type WSConn struct {
	conn *websocket.Conn
}

// DialWS connects to the bridge WebSocket endpoint. The wsURL should be
// obtained from Client.WSURL() and includes the auth token as a query param.
func DialWS(ctx context.Context, wsURL string) (*WSConn, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	return &WSConn{conn: conn}, nil
}

// ReadEvent blocks until the next WebSocket event arrives and decodes it.
func (w *WSConn) ReadEvent(ctx context.Context) (WSEvent, error) {
	_, data, err := w.conn.Read(ctx)
	if err != nil {
		return WSEvent{}, err
	}
	var evt WSEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return WSEvent{}, err
	}
	return evt, nil
}

// SendMessage sends a "send_message" event over the WebSocket.
func (w *WSConn) SendMessage(ctx context.Context, req SendRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	evt := WSEvent{Type: "send_message", Data: data}
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return w.conn.Write(ctx, websocket.MessageText, payload)
}

// SendPing sends a ping event; the server responds with a pong.
func (w *WSConn) SendPing(ctx context.Context) error {
	payload, _ := json.Marshal(WSEvent{Type: "ping"})
	return w.conn.Write(ctx, websocket.MessageText, payload)
}

// Close gracefully closes the WebSocket connection.
func (w *WSConn) Close() error {
	return w.conn.Close(websocket.StatusNormalClosure, "bye")
}
