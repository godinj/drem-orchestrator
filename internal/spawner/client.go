package spawner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// Client is a thin JSON-RPC 2.0 client over the spawner's Unix socket. One
// Client instance may be shared across goroutines; each call opens its own
// connection so the transport stays simple and short-lived. A persistent-
// connection pool is a follow-up if call volume warrants it.
type Client struct {
	socket  string
	dialer  func(ctx context.Context, socket string) (net.Conn, error)
	nextID  atomic.Int64
	timeout time.Duration
}

// NewClient returns a Client that connects to the given Unix socket path.
// The default per-call timeout is 30 seconds; Spawn RPCs that hit cold
// Docker pulls can exceed this, so callers with known-slow workloads
// should use NewClientWithTimeout or wrap their ctx with a deadline.
func NewClient(socket string) *Client {
	return &Client{
		socket:  socket,
		dialer:  defaultDialer,
		timeout: 30 * time.Second,
	}
}

// NewClientWithTimeout is NewClient with an explicit per-call timeout.
// A zero duration disables the client-side timeout; cancellation still
// flows through the caller's context.
func NewClientWithTimeout(socket string, timeout time.Duration) *Client {
	c := NewClient(socket)
	c.timeout = timeout
	return c
}

// defaultDialer opens a Unix-domain socket. Extracted as a variable so
// tests can swap in an in-memory transport without running a real
// Unix socket.
func defaultDialer(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}

// SpawnWorker invokes the SpawnWorker RPC.
func (c *Client) SpawnWorker(ctx context.Context, p SpawnWorkerParams) (SpawnWorkerResult, error) {
	var res SpawnWorkerResult
	if err := c.call(ctx, "SpawnWorker", p, &res); err != nil {
		return SpawnWorkerResult{}, err
	}
	return res, nil
}

// DestroyWorker invokes the DestroyWorker RPC.
func (c *Client) DestroyWorker(ctx context.Context, p DestroyWorkerParams) error {
	var res EmptyResult
	return c.call(ctx, "DestroyWorker", p, &res)
}

// ListWorkers invokes the ListWorkers RPC.
func (c *Client) ListWorkers(ctx context.Context, p ListWorkersParams) (ListWorkersResult, error) {
	var res ListWorkersResult
	if err := c.call(ctx, "ListWorkers", p, &res); err != nil {
		return ListWorkersResult{}, err
	}
	return res, nil
}

// InspectWorker invokes the InspectWorker RPC.
func (c *Client) InspectWorker(ctx context.Context, p InspectWorkerParams) (InspectWorkerResult, error) {
	var res InspectWorkerResult
	if err := c.call(ctx, "InspectWorker", p, &res); err != nil {
		return InspectWorkerResult{}, err
	}
	return res, nil
}

// call dials the server, writes a single framed request, reads one framed
// response, and decodes the result. The connection is closed in either
// direction when this function returns.
func (c *Client) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	callCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	conn, err := c.dialer(callCtx, c.socket)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.socket, err)
	}
	defer conn.Close()

	if deadline, ok := callCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	id := c.nextID.Add(1)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf("%d", id)),
		Method:  method,
		Params:  paramsJSON,
	}
	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	if err := writeFrame(conn, reqBody); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	body, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		return fmt.Errorf("read frame: %w", err)
	}
	var resp rpcResponse
	resp.Result = out
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc %s: code=%d %s", method, resp.Error.Code, resp.Error.Message)
	}
	return nil
}
