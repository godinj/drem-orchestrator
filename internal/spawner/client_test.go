package spawner

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mockServer is a hand-rolled Unix-socket server that records the one
// request it receives and replies with a canned response. It is used to
// assert that the Client serialises method name and params exactly as
// expected, without depending on the full Service dispatch path.
type mockServer struct {
	t        *testing.T
	sockPath string
	ln       net.Listener
	reply    []byte
	gotBody  chan []byte
	done     chan struct{}
}

func newMockServer(t *testing.T, reply []byte) *mockServer {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "mock.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	m := &mockServer{
		t:        t,
		sockPath: sock,
		ln:       ln,
		reply:    reply,
		gotBody:  make(chan []byte, 1),
		done:     make(chan struct{}),
	}
	go m.loop()
	t.Cleanup(func() {
		_ = ln.Close()
		<-m.done
	})
	return m
}

func (m *mockServer) loop() {
	defer close(m.done)
	conn, err := m.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	body, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		return
	}
	m.gotBody <- body
	_ = writeFrame(conn, m.reply)
}

// recvBody returns the single request body the mock received, failing the
// test if none arrived within a short timeout. Keeping the timeout here
// rather than in the Client's context makes the failure mode clearer:
// "the server never saw the call" vs "the client gave up early".
func (m *mockServer) recvBody() []byte {
	m.t.Helper()
	select {
	case b := <-m.gotBody:
		return b
	case <-time.After(2 * time.Second):
		m.t.Fatal("mock server never received a request")
		return nil
	}
}

func TestClient_SpawnWorker_SendsMethodAndParams(t *testing.T) {
	// Pre-encode the reply so the mock can echo it verbatim.
	reply, err := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Result:  SpawnWorkerResult{ContainerID: "c-abc", Endpoint: "1.2.3.4:9000"},
	})
	require.NoError(t, err)

	mock := newMockServer(t, reply)
	client := NewClient(mock.sockPath)

	res, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:    "drem-orch",
		AgentType:  "coder",
		WorkerID:   "w-1",
		Branch:     "feature/x",
		Labels:     map[string]string{"drem.language": "go"},
		CredsMount: "/host/.claude/.credentials.json",
	})
	require.NoError(t, err)
	require.Equal(t, "c-abc", res.ContainerID)
	require.Equal(t, "1.2.3.4:9000", res.Endpoint)

	var req rpcRequest
	require.NoError(t, json.Unmarshal(mock.recvBody(), &req))
	require.Equal(t, "2.0", req.JSONRPC)
	require.Equal(t, "SpawnWorker", req.Method)

	var params SpawnWorkerParams
	require.NoError(t, json.Unmarshal(req.Params, &params))
	require.Equal(t, "drem-orch", params.Project)
	require.Equal(t, "coder", params.AgentType)
	require.Equal(t, "w-1", params.WorkerID)
	require.Equal(t, "feature/x", params.Branch)
	require.Equal(t, "go", params.Labels["drem.language"])
	// CredsMount round-trips through the JSON wire format so the
	// spawner sees the same host path the client sent.
	require.Equal(t, "/host/.claude/.credentials.json", params.CredsMount)
}

func TestClient_DestroyWorker_SendsContainerID(t *testing.T) {
	reply, err := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Result:  EmptyResult{},
	})
	require.NoError(t, err)
	mock := newMockServer(t, reply)
	client := NewClient(mock.sockPath)

	err = client.DestroyWorker(context.Background(), DestroyWorkerParams{ContainerID: "c-xyz"})
	require.NoError(t, err)

	var req rpcRequest
	require.NoError(t, json.Unmarshal(mock.recvBody(), &req))
	require.Equal(t, "DestroyWorker", req.Method)
	var p DestroyWorkerParams
	require.NoError(t, json.Unmarshal(req.Params, &p))
	require.Equal(t, "c-xyz", p.ContainerID)
}

func TestClient_ListWorkers_SendsProjectFilter(t *testing.T) {
	reply, err := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Result: ListWorkersResult{
			Workers: []WorkerInfo{{ContainerID: "c-1", Project: "alpha"}},
		},
	})
	require.NoError(t, err)
	mock := newMockServer(t, reply)
	client := NewClient(mock.sockPath)

	res, err := client.ListWorkers(context.Background(), ListWorkersParams{Project: "alpha"})
	require.NoError(t, err)
	require.Len(t, res.Workers, 1)
	require.Equal(t, "alpha", res.Workers[0].Project)

	var req rpcRequest
	require.NoError(t, json.Unmarshal(mock.recvBody(), &req))
	require.Equal(t, "ListWorkers", req.Method)
	var p ListWorkersParams
	require.NoError(t, json.Unmarshal(req.Params, &p))
	require.Equal(t, "alpha", p.Project)
}

func TestClient_InspectWorker_DecodesResult(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reply, err := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Result: InspectWorkerResult{
			Status:    "running",
			ExitCode:  0,
			StartedAt: start,
		},
	})
	require.NoError(t, err)
	mock := newMockServer(t, reply)
	client := NewClient(mock.sockPath)

	res, err := client.InspectWorker(context.Background(), InspectWorkerParams{ContainerID: "c-1"})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)
	require.True(t, res.StartedAt.Equal(start))

	var req rpcRequest
	require.NoError(t, json.Unmarshal(mock.recvBody(), &req))
	require.Equal(t, "InspectWorker", req.Method)
}

func TestClient_SurfacesRPCError(t *testing.T) {
	reply, err := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Error:   &rpcError{Code: -32601, Message: "method not found: Bogus"},
	})
	require.NoError(t, err)
	mock := newMockServer(t, reply)
	client := NewClient(mock.sockPath)

	_, err = client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "p", AgentType: "merger", WorkerID: "w", Branch: "b",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "code=-32601")
}
