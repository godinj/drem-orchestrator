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

	"github.com/godinj/drem-orchestrator/internal/container"
)

// startHarness stands up a Service on a Unix socket under t.TempDir()
// backed by a FakeRuntime. The returned cleanup func blocks until the
// Serve goroutine has exited so tests cannot accidentally race the
// accept loop's shutdown against the test tear-down.
func startHarness(t *testing.T) (*container.FakeRuntime, *Client, func()) {
	t.Helper()
	fake := container.NewFakeRuntime()
	sockPath := filepath.Join(t.TempDir(), "spawner.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(ctx, ln, fake)
	}()

	client := NewClient(sockPath)
	cleanup := func() {
		cancel()
		<-done
	}
	return fake, client, cleanup
}

func TestService_SpawnWorker_ProducesSpawnCallWithLabelsAndMounts(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	res, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:       "drem-orch",
		AgentType:     "coder",
		WorkerID:      "w-1",
		Branch:        "feature/x",
		Labels:        map[string]string{"drem.language": "go"},
		BareRepoMount: "/host/bare",
		Env:           map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.ContainerID)

	calls := fake.Calls()
	var spawn *container.Call
	for i := range calls {
		if calls[i].Op == "Spawn" {
			spawn = &calls[i]
			break
		}
	}
	require.NotNil(t, spawn, "expected a Spawn call")
	require.Equal(t, res.ContainerID, spawn.ID)
	require.NotNil(t, spawn.Spec)

	// Image mapping: coder + drem.language=go → drem-worker-go.
	require.Equal(t, "localhost:5000/drem-worker-go:latest", spawn.Spec.Image)

	// Identifying labels are applied by the service; caller's drem.language
	// label is preserved.
	require.Equal(t, "drem-orch", spawn.Spec.Labels["drem.project"])
	require.Equal(t, "coder", spawn.Spec.Labels["drem.agent_type"])
	require.Equal(t, "w-1", spawn.Spec.Labels["drem.worker_id"])
	require.Equal(t, "feature/x", spawn.Spec.Labels["drem.branch"])
	require.Equal(t, "go", spawn.Spec.Labels["drem.language"])

	require.Equal(t, defaultNetwork, spawn.Spec.Network)
	require.Equal(t, map[string]string{"FOO": "bar"}, spawn.Spec.Env)
	require.Len(t, spawn.Spec.Mounts, 1)
	require.Equal(t, container.Mount{
		Source:   "/host/bare",
		Target:   "/bare",
		ReadOnly: true,
	}, spawn.Spec.Mounts[0])
}

func TestService_SpawnWorker_MissingLanguageForCoderIsInvalidParams(t *testing.T) {
	_, client, cleanup := startHarness(t)
	defer cleanup()

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:   "drem-orch",
		AgentType: "coder",
		WorkerID:  "w-1",
		Branch:    "feature/x",
		// no drem.language label → no image mapping available.
	})
	require.Error(t, err)
	// Runtime-level failures surface as code=-32000 per JSON-RPC server
	// reserved range.
	require.Contains(t, err.Error(), "code=-32000")
}

func TestService_SpawnWorker_ExplicitImageOverride(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:   "drem-orch",
		AgentType: "coder",
		WorkerID:  "w-1",
		Branch:    "feature/x",
		Image:     "localhost:5000/custom:tag",
	})
	require.NoError(t, err)

	var spawn *container.Call
	for _, c := range fake.Calls() {
		if c.Op == "Spawn" {
			call := c
			spawn = &call
			break
		}
	}
	require.NotNil(t, spawn)
	require.Equal(t, "localhost:5000/custom:tag", spawn.Spec.Image)
}

func TestService_DestroyWorker_ProducesDestroyCall(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	spawned, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "p", AgentType: "merger", WorkerID: "w", Branch: "b",
	})
	require.NoError(t, err)

	err = client.DestroyWorker(context.Background(), DestroyWorkerParams{ContainerID: spawned.ContainerID})
	require.NoError(t, err)

	var destroyed bool
	for _, c := range fake.Calls() {
		if c.Op == "Destroy" && c.ID == spawned.ContainerID {
			destroyed = true
			break
		}
	}
	require.True(t, destroyed, "expected a Destroy call for %s", spawned.ContainerID)
}

func TestService_InspectWorker_ReturnsInjectedState(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	spawned, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "p", AgentType: "merger", WorkerID: "w", Branch: "b",
	})
	require.NoError(t, err)

	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	end := start.Add(time.Hour)
	fake.SetInspectResult(spawned.ContainerID, container.State{
		Status:     container.StatusExited,
		ExitCode:   137,
		StartedAt:  start,
		FinishedAt: end,
		OOMKilled:  true,
	})

	res, err := client.InspectWorker(context.Background(), InspectWorkerParams{ContainerID: spawned.ContainerID})
	require.NoError(t, err)
	require.Equal(t, string(container.StatusExited), res.Status)
	require.Equal(t, 137, res.ExitCode)
	require.True(t, res.StartedAt.Equal(start))
	require.True(t, res.FinishedAt.Equal(end))
	require.True(t, res.OOMKilled)
}

func TestService_ListWorkers_FiltersByProject(t *testing.T) {
	_, client, cleanup := startHarness(t)
	defer cleanup()

	// Spawn two workers on different projects.
	a, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "alpha", AgentType: "merger", WorkerID: "w-1", Branch: "b1",
	})
	require.NoError(t, err)
	b, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "beta", AgentType: "merger", WorkerID: "w-2", Branch: "b2",
	})
	require.NoError(t, err)

	all, err := client.ListWorkers(context.Background(), ListWorkersParams{})
	require.NoError(t, err)
	require.Len(t, all.Workers, 2)

	alpha, err := client.ListWorkers(context.Background(), ListWorkersParams{Project: "alpha"})
	require.NoError(t, err)
	require.Len(t, alpha.Workers, 1)
	require.Equal(t, a.ContainerID, alpha.Workers[0].ContainerID)
	require.Equal(t, "alpha", alpha.Workers[0].Project)

	beta, err := client.ListWorkers(context.Background(), ListWorkersParams{Project: "beta"})
	require.NoError(t, err)
	require.Len(t, beta.Workers, 1)
	require.Equal(t, b.ContainerID, beta.Workers[0].ContainerID)
}

func TestService_ListWorkers_DropsDestroyed(t *testing.T) {
	_, client, cleanup := startHarness(t)
	defer cleanup()

	spawned, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "alpha", AgentType: "merger", WorkerID: "w-1", Branch: "b",
	})
	require.NoError(t, err)

	require.NoError(t, client.DestroyWorker(context.Background(), DestroyWorkerParams{ContainerID: spawned.ContainerID}))

	res, err := client.ListWorkers(context.Background(), ListWorkersParams{})
	require.NoError(t, err)
	require.Empty(t, res.Workers)
}

func TestService_UnknownMethod_Returns32601(t *testing.T) {
	_, _, cleanup := startHarness(t)
	defer cleanup()

	// Call an unknown method by hand — the typed client surface only
	// exposes the four defined RPCs, so we use the raw framing helpers.
	raw := rawRequest(t, 1, "Nope", map[string]string{})
	resp := roundTripRaw(t, raw)

	require.NotNil(t, resp.Error)
	require.Equal(t, errCodeMethodNotFound, resp.Error.Code)
}

func TestService_MalformedParams_Returns32602(t *testing.T) {
	_, _, cleanup := startHarness(t)
	defer cleanup()

	// A raw request whose params field is a string, which cannot be
	// decoded into SpawnWorkerParams. The service rejects it with
	// -32602 before reaching the method implementation.
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"SpawnWorker","params":"not-an-object"}`)
	resp := roundTripRaw(t, raw)
	require.NotNil(t, resp.Error)
	require.Equal(t, errCodeInvalidParams, resp.Error.Code)
}

// rawRequest builds an arbitrary JSON-RPC 2.0 request body. Used by tests
// that need to exercise error paths outside the client's typed surface.
func rawRequest(t *testing.T, id int, method string, params interface{}) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	require.NoError(t, err)
	return body
}

// roundTripRaw sends a raw request body over a fresh connection to the
// service the current test stood up and returns the decoded response.
// Shares a socket path with startHarness via the test's temp dir.
func roundTripRaw(t *testing.T, body []byte) rpcResponse {
	t.Helper()
	// Find the socket the service is listening on by locating the
	// server goroutine's path via the test helper — but startHarness
	// does not expose it. For simplicity, spin up our own service here
	// for the two "raw" tests; they don't share state with the others.
	fake := container.NewFakeRuntime()
	sockPath := filepath.Join(t.TempDir(), "raw.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(ctx, ln, fake)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	conn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, writeFrame(conn, body))

	respBody, err := readFrame(bufio.NewReader(conn))
	require.NoError(t, err)
	var resp rpcResponse
	require.NoError(t, json.Unmarshal(respBody, &resp))
	return resp
}
