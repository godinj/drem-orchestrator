package spawner

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/container"
)

// startHarness stands up a Service on a short Unix socket path
// backed by a FakeRuntime. The returned cleanup func blocks until the
// Serve goroutine has exited so tests cannot accidentally race the
// accept loop's shutdown against the test tear-down.
func startHarness(t *testing.T) (*container.FakeRuntime, *Client, func()) {
	t.Helper()
	fake := container.NewFakeRuntime()
	sockPath := shortUnixSocketPath(t, "spawner.sock")
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
		ProjectID:     "11111111-2222-3333-4444-555555555555",
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
	// label is preserved. Dual-label contract (plans/dual-label-worker-spawn.md):
	// drem.project carries the human-readable name so agentmon's
	// DREM_PROJECT env filter matches; drem.project_id carries the
	// stable UUID so internal orch filters match even after a rename.
	require.Equal(t, "drem-orch", spawn.Spec.Labels["drem.project"])
	require.Equal(t, "11111111-2222-3333-4444-555555555555", spawn.Spec.Labels["drem.project_id"])
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

// TestService_SpawnWorker_CmdForwardedToSpec verifies that a non-empty Cmd
// on SpawnWorkerParams propagates to container.Spec.Cmd. This is the
// mechanism the merger uses to pass its per-task flags (--feature-branch,
// --task-id, --test-cmd, --orch-url, --agentmon-token). See
// plans/merger-spawn-on-demand-impl.md.
func TestService_SpawnWorker_CmdForwardedToSpec(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	argv := []string{
		"--feature-branch", "feature/x",
		"--task-id", "abc-123",
		"--orch-url", "http://orch:8080",
	}
	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:   "drem-orch",
		AgentType: "merger",
		WorkerID:  "m-1",
		Branch:    "feature/x",
		Cmd:       argv,
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
	require.NotNil(t, spawn, "expected Spawn call")
	require.Equal(t, argv, spawn.Spec.Cmd, "Cmd must be forwarded to container.Spec")
}

// TestService_SpawnWorker_BareRepoReadWriteFlipsMount verifies the
// read-only default and the read-write flip. The merger uses the flip to
// push the integration branch; workers rely on the default being read-only.
func TestService_SpawnWorker_BareRepoReadWriteFlipsMount(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	// Default (zero value) stays read-only.
	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "p", AgentType: "merger", WorkerID: "w1", Branch: "b",
		BareRepoMount: "/host/bare",
	})
	require.NoError(t, err)

	// Explicit BareRepoReadWrite: true flips the flag.
	_, err = client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:           "p",
		AgentType:         "merger",
		WorkerID:          "w2",
		Branch:            "b",
		BareRepoMount:     "/host/bare",
		BareRepoReadWrite: true,
	})
	require.NoError(t, err)

	var spawns []*container.Call
	for _, c := range fake.Calls() {
		if c.Op == "Spawn" {
			call := c
			spawns = append(spawns, &call)
		}
	}
	require.Len(t, spawns, 2)
	require.Len(t, spawns[0].Spec.Mounts, 1)
	require.True(t, spawns[0].Spec.Mounts[0].ReadOnly,
		"first spawn without BareRepoReadWrite must be read-only")
	require.Len(t, spawns[1].Spec.Mounts, 1)
	require.False(t, spawns[1].Spec.Mounts[0].ReadOnly,
		"second spawn with BareRepoReadWrite=true must be read-write")
}

// TestService_SpawnWorker_CredsMountProducesReadOnlyFileMount verifies
// that a populated CredsMount translates into a read-only bind mount at
// /home/drem/.claude/.credentials.json with the caller-supplied host
// path as the source. See plans/worker-subscription-auth.md §3.
func TestService_SpawnWorker_CredsMountProducesReadOnlyFileMount(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	// Pre-create the creds file on disk; SpawnWorker stats it fail-closed.
	credsPath := filepath.Join(t.TempDir(), ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, []byte("{}"), 0o600))

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:       "drem-orch",
		AgentType:     "coder",
		WorkerID:      "w-1",
		Branch:        "feature/x",
		Labels:        map[string]string{"drem.language": "go"},
		BareRepoMount: "/host/bare",
		CredsMount:    credsPath,
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
	require.NotNil(t, spawn, "expected a Spawn call")

	// Both mounts in the expected order: /bare first, creds second.
	require.Len(t, spawn.Spec.Mounts, 2)
	require.Equal(t, container.Mount{
		Source:   "/host/bare",
		Target:   "/bare",
		ReadOnly: true,
	}, spawn.Spec.Mounts[0])
	require.Equal(t, container.Mount{
		Source:   credsPath,
		Target:   "/home/drem/.claude/.credentials.json",
		ReadOnly: true,
	}, spawn.Spec.Mounts[1])
}

// TestService_SpawnWorker_CredsMountMissingFileFails verifies the
// fail-closed pre-flight check: when the caller passes a CredsMount
// that does not exist on host, SpawnWorker returns an error without
// creating the container.
func TestService_SpawnWorker_CredsMountMissingFileFails(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	missing := filepath.Join(t.TempDir(), "does-not-exist", ".credentials.json")

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:    "drem-orch",
		AgentType:  "coder",
		WorkerID:   "w-1",
		Branch:     "feature/x",
		Labels:     map[string]string{"drem.language": "go"},
		CredsMount: missing,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "creds file not found")
	require.Contains(t, err.Error(), "claude login")

	// No Spawn call reached the runtime — pre-flight rejected.
	for _, c := range fake.Calls() {
		require.NotEqual(t, "Spawn", c.Op, "runtime must not be called when creds file is missing")
	}
}

func TestService_SpawnWorker_CodexAuthMountProducesReadOnlyFileMount(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	authPath := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(authPath, []byte("{}"), 0o600))

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:        "drem-orch",
		AgentType:      "coder",
		WorkerID:       "w-1",
		Branch:         "feature/x",
		Labels:         map[string]string{"drem.language": "go"},
		CodexAuthMount: authPath,
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
	require.NotNil(t, spawn, "expected a Spawn call")
	require.Contains(t, spawn.Spec.Mounts, container.Mount{
		Source:   authPath,
		Target:   "/home/drem/.codex/auth.json",
		ReadOnly: true,
	})
}

// TestService_SpawnWorker_PromptMountProducesReadOnlyMountAndEnv verifies
// that a populated PromptMount translates into a read-only bind mount at
// /home/drem/.drem/prompt.md AND injects DREM_PROMPT_PATH into the Spec
// env so the worker entrypoint's `claude -p` path finds it without the
// caller having to plumb the env key explicitly. See
// plans/worker-prompt-delivery.md §3.
func TestService_SpawnWorker_PromptMountProducesReadOnlyMountAndEnv(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	// Creds mount also needs to exist for the same spawn — claude-backed
	// roles carry both. Pre-create both temp files.
	credsPath := filepath.Join(t.TempDir(), ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, []byte("{}"), 0o600))
	promptPath := filepath.Join(t.TempDir(), "task-abc.md")
	require.NoError(t, os.WriteFile(promptPath, []byte("# Task\nDo the thing."), 0o600))

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:       "drem-orch",
		AgentType:     "coder",
		WorkerID:      "w-1",
		Branch:        "feature/x",
		Labels:        map[string]string{"drem.language": "go"},
		BareRepoMount: "/host/bare",
		CredsMount:    credsPath,
		PromptMount:   promptPath,
		Env:           map[string]string{"FOO": "bar"},
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
	require.NotNil(t, spawn, "expected a Spawn call")

	// Three mounts in stable order: /bare, creds, prompt.
	require.Len(t, spawn.Spec.Mounts, 3)
	require.Equal(t, container.Mount{
		Source:   promptPath,
		Target:   "/home/drem/.drem/prompt.md",
		ReadOnly: true,
	}, spawn.Spec.Mounts[2])

	// Env carries the deterministic DREM_PROMPT_PATH alongside the
	// caller's FOO=bar.
	require.Equal(t, "/home/drem/.drem/prompt.md", spawn.Spec.Env["DREM_PROMPT_PATH"])
	require.Equal(t, "bar", spawn.Spec.Env["FOO"])
}

func TestService_SpawnWorker_JournalMountIsWritableAndDeterministic(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()
	journalPath := filepath.Join(t.TempDir(), "attempt")
	require.NoError(t, os.MkdirAll(journalPath, 0o700))

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "drem-orch", AgentType: "coder", WorkerID: "w-journal", Branch: "feature/x",
		Labels: map[string]string{"drem.language": "go"}, JournalMount: journalPath,
	})
	require.NoError(t, err)

	var spawn *container.Call
	for _, call := range fake.Calls() {
		if call.Op == "Spawn" {
			copy := call
			spawn = &copy
		}
	}
	require.NotNil(t, spawn)
	require.Contains(t, spawn.Spec.Mounts, container.Mount{Source: journalPath, Target: "/home/drem/.drem/state", ReadOnly: false})
	require.Equal(t, "/home/drem/.drem/state/journal.json", spawn.Spec.Env["DREM_DIRECT_JOURNAL_PATH"])
}

// TestService_SpawnWorker_PromptMountMissingFileFails verifies the
// fail-closed pre-flight check: a PromptMount that does not exist on
// host returns an error without reaching the runtime.
func TestService_SpawnWorker_PromptMountMissingFileFails(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	missing := filepath.Join(t.TempDir(), "does-not-exist", "task.md")

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:     "drem-orch",
		AgentType:   "coder",
		WorkerID:    "w-1",
		Branch:      "feature/x",
		Labels:      map[string]string{"drem.language": "go"},
		PromptMount: missing,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt file not found")
	require.Contains(t, err.Error(), "orch must write the prompt")

	for _, c := range fake.Calls() {
		require.NotEqual(t, "Spawn", c.Op, "runtime must not be called when prompt file is missing")
	}
}

// TestService_SpawnWorker_PromptMountOverwritesCallerDREM_PROMPT_PATH
// verifies the deterministic-env contract: a caller that set
// DREM_PROMPT_PATH in the Env map (wrong target, stale value, anything)
// is always overridden by the spawner's canonical container-side path.
func TestService_SpawnWorker_PromptMountOverwritesCallerDREM_PROMPT_PATH(t *testing.T) {
	fake, client, cleanup := startHarness(t)
	defer cleanup()

	promptPath := filepath.Join(t.TempDir(), "task.md")
	require.NoError(t, os.WriteFile(promptPath, []byte("# T"), 0o600))

	_, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project:     "drem-orch",
		AgentType:   "coder",
		WorkerID:    "w-1",
		Branch:      "feature/x",
		Labels:      map[string]string{"drem.language": "go"},
		PromptMount: promptPath,
		Env: map[string]string{
			// A malicious / buggy caller sets a wrong target here. The
			// spawner must overwrite to the canonical mount target so
			// the container-side path agrees with the bind-mount.
			"DREM_PROMPT_PATH": "/tmp/attacker-controlled.md",
		},
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
	require.Equal(t, "/home/drem/.drem/prompt.md", spawn.Spec.Env["DREM_PROMPT_PATH"],
		"spawner must overwrite caller-supplied DREM_PROMPT_PATH to the canonical target")
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

func TestService_InspectWorker_ReturnsRemovedForMissingContainer(t *testing.T) {
	_, client, cleanup := startHarness(t)
	defer cleanup()

	res, err := client.InspectWorker(context.Background(), InspectWorkerParams{ContainerID: "removed-worker"})
	require.NoError(t, err)
	require.Equal(t, string(container.StatusRemoved), res.Status)
}

func TestService_ListWorkers_FiltersByProject(t *testing.T) {
	_, client, cleanup := startHarness(t)
	defer cleanup()

	// Spawn two workers on different projects. ProjectID threaded
	// through so the dual-label contract is exercised at the
	// service boundary too (see plans/dual-label-worker-spawn.md).
	a, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "alpha", ProjectID: "uuid-alpha",
		AgentType: "merger", WorkerID: "w-1", Branch: "b1",
	})
	require.NoError(t, err)
	b, err := client.SpawnWorker(context.Background(), SpawnWorkerParams{
		Project: "beta", ProjectID: "uuid-beta",
		AgentType: "merger", WorkerID: "w-2", Branch: "b2",
	})
	require.NoError(t, err)

	all, err := client.ListWorkers(context.Background(), ListWorkersParams{})
	require.NoError(t, err)
	require.Len(t, all.Workers, 2)

	// Filter by human-readable name.
	alpha, err := client.ListWorkers(context.Background(), ListWorkersParams{Project: "alpha"})
	require.NoError(t, err)
	require.Len(t, alpha.Workers, 1)
	require.Equal(t, a.ContainerID, alpha.Workers[0].ContainerID)
	require.Equal(t, "alpha", alpha.Workers[0].Project)
	require.Equal(t, "uuid-alpha", alpha.Workers[0].ProjectID)

	// Filter by stable UUID — the path every internal orch filter uses.
	byID, err := client.ListWorkers(context.Background(), ListWorkersParams{ProjectID: "uuid-beta"})
	require.NoError(t, err)
	require.Len(t, byID.Workers, 1)
	require.Equal(t, b.ContainerID, byID.Workers[0].ContainerID)
	require.Equal(t, "uuid-beta", byID.Workers[0].ProjectID)

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
	sockPath := shortUnixSocketPath(t, "raw.sock")
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
