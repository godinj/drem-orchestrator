package spawner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/godinj/drem-orchestrator/internal/container"
)

// JSON-RPC 2.0 reserved error codes used by the service. The PRD pins the
// protocol at JSON-RPC 2.0 §"Spawner RPC" so these match the spec exactly.
const (
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603
	errCodeRuntime        = -32000 // server-reserved range: -32000..-32099
)

// Service dispatches JSON-RPC requests onto the container Runtime. A single
// Service handles many concurrent connections; Runtime is the ONLY piece of
// mutable external state it touches. The in-memory worker registry tracks
// containers the service itself spawned so ListWorkers can answer without
// a full-daemon scan.
type Service struct {
	Runtime container.Runtime

	mu       sync.Mutex
	registry map[string]*registryEntry
}

// registryEntry holds the label context captured at spawn time so
// ListWorkers and InspectWorker can attribute a container to the project,
// agent type, worker id, and branch without a second round trip to the
// runtime for label lookup.
type registryEntry struct {
	info WorkerInfo
}

// rpcRequest is the decoded JSON-RPC 2.0 request. Params is captured raw
// so methods unmarshal into their own typed parameter struct.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is the JSON-RPC 2.0 response envelope. Exactly one of Result
// and Error is populated on every response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC 2.0 error object. Data is left nil today but is
// reserved for future structured error details without a wire break.
type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// NewService constructs a Service wrapping the given runtime. The in-memory
// registry starts empty; every successful SpawnWorker registers a new entry
// and every DestroyWorker removes one.
func NewService(rt container.Runtime) *Service {
	return &Service{
		Runtime:  rt,
		registry: make(map[string]*registryEntry),
	}
}

// Serve runs the accept loop on ln. Each connection is handled on its own
// goroutine so a slow or misbehaving client cannot block others. Serve
// returns when ctx is cancelled or ln.Accept returns a non-temporary error.
func Serve(ctx context.Context, ln net.Listener, rt container.Runtime) error {
	svc := NewService(rt)

	// Close the listener when ctx is cancelled so Accept unblocks and the
	// goroutine can return. The goroutine exits cleanly on listener close.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener closed — ctx was cancelled or the caller closed us.
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			return fmt.Errorf("spawner accept: %w", err)
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			svc.handleConn(ctx, c)
		}(conn)
	}
}

// handleConn reads framed requests from a single client connection and
// writes framed responses until EOF or a framing error. The connection is
// closed when the loop exits.
func (s *Service) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		body, err := readFrame(reader)
		if err != nil {
			// EOF on a clean disconnect and any framing error both terminate
			// the conn. JSON-RPC has no recovery story for partial frames.
			return
		}
		resp := s.dispatch(ctx, body)
		if resp == nil {
			// Notifications (no id) produce no response per JSON-RPC 2.0.
			continue
		}
		if err := writeFrame(conn, resp); err != nil {
			return
		}
	}
}

// dispatch parses a single request body and routes it to the named method.
// It returns the response to write back, or nil when the request was a
// notification (no id).
func (s *Service) dispatch(ctx context.Context, body []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return mustEncode(errorResponse(nil, errCodeInvalidRequest, "parse error: "+err.Error()))
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return mustEncode(errorResponse(req.ID, errCodeInvalidRequest, "invalid JSON-RPC 2.0 request"))
	}

	result, rpcErr := s.invoke(ctx, req.Method, req.Params)

	if len(req.ID) == 0 {
		// Notification: per spec no response is returned, even on error.
		return nil
	}
	if rpcErr != nil {
		return mustEncode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
	}
	return mustEncode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// invoke is the method dispatch table. Adding a new RPC means extending
// this switch and implementing the corresponding method in methods.go.
// Each branch decodes its own params struct and delegates to the method
// implementation so the switch itself stays free of runtime I/O.
func (s *Service) invoke(ctx context.Context, method string, rawParams json.RawMessage) (interface{}, *rpcError) {
	switch method {
	case "SpawnWorker":
		var p SpawnWorkerParams
		if err := decodeParams(rawParams, &p); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
		}
		res, err := s.SpawnWorker(ctx, p)
		if err != nil {
			return nil, &rpcError{Code: errCodeRuntime, Message: err.Error()}
		}
		return res, nil
	case "DestroyWorker":
		var p DestroyWorkerParams
		if err := decodeParams(rawParams, &p); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
		}
		if err := s.DestroyWorker(ctx, p); err != nil {
			return nil, &rpcError{Code: errCodeRuntime, Message: err.Error()}
		}
		return EmptyResult{}, nil
	case "ListWorkers":
		var p ListWorkersParams
		if err := decodeParams(rawParams, &p); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
		}
		res, err := s.ListWorkers(ctx, p)
		if err != nil {
			return nil, &rpcError{Code: errCodeRuntime, Message: err.Error()}
		}
		return res, nil
	case "InspectWorker":
		var p InspectWorkerParams
		if err := decodeParams(rawParams, &p); err != nil {
			return nil, &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
		}
		res, err := s.InspectWorker(ctx, p)
		if err != nil {
			return nil, &rpcError{Code: errCodeRuntime, Message: err.Error()}
		}
		return res, nil
	default:
		return nil, &rpcError{Code: errCodeMethodNotFound, Message: "method not found: " + method}
	}
}

// decodeParams unmarshals a JSON-RPC params value into dst. A nil or empty
// params is treated as an empty object so callers that omit params for
// methods whose fields are all optional don't hit an invalid-params error.
func decodeParams(raw json.RawMessage, dst interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

// errorResponse builds a response envelope wrapping an rpcError. Extracted
// as a helper because three dispatch-failure paths build the same shape.
func errorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// mustEncode marshals a response. Marshal can only fail here on an
// unserializable value, which would indicate a programming error in one of
// our own result types; surfacing it as a panic is louder than a wire
// error.
func mustEncode(resp rpcResponse) []byte {
	out, err := json.Marshal(resp)
	if err != nil {
		panic(fmt.Sprintf("spawner: encode response: %v", err))
	}
	return out
}

// readFrame parses an LSP-style "Content-Length: N\r\n\r\n<body>" frame.
// Any unknown header is tolerated and ignored so future header additions
// (e.g. Content-Type) remain forward-compatible. Returns an error on EOF
// or any framing violation.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var length int64 = -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			return nil, fmt.Errorf("malformed header: %q", line)
		}
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if strings.EqualFold(name, "Content-Length") {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("invalid Content-Length: %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// writeFrame emits an LSP-style frame. Content-Length is written first so
// a reader using ReadFull can size its buffer before allocating.
func writeFrame(w io.Writer, body []byte) error {
	hdr := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, hdr); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return nil
}
