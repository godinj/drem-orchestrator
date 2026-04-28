package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
	"github.com/godinj/drem-orchestrator/internal/container"
	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/orchhttp"
)

type fakeStartupLogStreamer struct {
	calls atomic.Int32
}

func (f *fakeStartupLogStreamer) StreamLogs(_ context.Context, _ string, _ container.LogOptions) (io.ReadCloser, error) {
	f.calls.Add(1)
	return io.NopCloser(strings.NewReader("startup logs\n")), nil
}

// TestEffectiveAgentmonTokenPrefersConfig verifies that when drem.toml
// sets agentmon_token, the TOML value wins over the env-var fallback.
// This keeps file-driven dev setups stable even when a stray
// DREM_AGENTMON_TOKEN is exported in the operator's shell.
func TestEffectiveAgentmonTokenPrefersConfig(t *testing.T) {
	t.Setenv("DREM_AGENTMON_TOKEN", "env-value")

	cfg := Config{AgentmonToken: "toml-value"}
	require.Equal(t, "toml-value", effectiveAgentmonToken(cfg))
}

// TestEffectiveAgentmonTokenFallsBackToEnv is the regression test for
// the 41-hour agentmon-ingest-401 outage. The per-project compose sets
// DREM_AGENTMON_TOKEN on the orch container env block, but the mounted
// drem.toml does not carry an agentmon_token key. Before the fix,
// Config.AgentmonToken was empty and the ingest middleware rejected
// every agentmon POST with 401; heartbeats never reached the DB, the
// stuck-agent reconciler declared workers dead, and tasks failed with
// spurious "agent session died" errors. The fix: when the TOML key is
// empty, fall back to os.Getenv("DREM_AGENTMON_TOKEN") so the compose
// env alone is sufficient to authenticate the orch↔agentmon path.
func TestEffectiveAgentmonTokenFallsBackToEnv(t *testing.T) {
	t.Setenv("DREM_AGENTMON_TOKEN", "env-value")

	cfg := Config{AgentmonToken: ""}
	require.Equal(t, "env-value", effectiveAgentmonToken(cfg))
}

// TestEffectiveAgentmonTokenEmptyWhenBothUnset documents the
// fail-closed contract. When neither source supplies a token, the
// result is empty and orchhttp.requireAgentmonToken will reject every
// request — exactly the pre-fix behaviour, but now explicit in a test
// so a future refactor cannot accidentally turn it into a silent
// "anything goes" default.
func TestEffectiveAgentmonTokenEmptyWhenBothUnset(t *testing.T) {
	t.Setenv("DREM_AGENTMON_TOKEN", "")

	cfg := Config{AgentmonToken: ""}
	require.Empty(t, effectiveAgentmonToken(cfg))
}

// TestStartOrchHTTPAuthenticatesAgentmonClient is the full-stack
// regression test for the 401 outage. It wires a real orchhttp.Server
// through startOrchHTTP with DREM_AGENTMON_TOKEN in the env (exactly
// how the per-project compose feeds the orch container), then drives
// agentmon.HTTPIngestor — the production HTTP client — against it.
// Before the fix this test failed with HTTP 401; after the fix it
// returns nil (HTTP 202 Accepted, empty batch short-circuited).
//
// This is the seam the outage slipped through: unit tests for
// orchhttp.requireAgentmonToken asserted middleware correctness, and
// unit tests for HTTPIngestor asserted client correctness, but no test
// ran the two together against the cmd/drem startup path. A header
// rename, path typo, or (as in this case) a config-loading gap can
// drift either side in isolation without any test failing.
func TestStartOrchHTTPAuthenticatesAgentmonClient(t *testing.T) {
	const token = "test-agentmon-token"
	t.Setenv("DREM_AGENTMON_TOKEN", token)

	db := newOrchHTTPTestDB(t)

	// Pick a free port for the orch HTTP listener so this test can
	// run in parallel with other binaries.
	port := freePort(t)
	cfg := Config{
		OrchHTTPPort:    port,
		ProjectLanguage: "go",
		// AgentmonToken deliberately empty to exercise the env-var
		// fallback path — this is exactly how the production compose
		// ships orch today (env on container, not in drem.toml).
		AgentmonToken: "",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// orch=nil exercises the read-only-ingest path these tests cover; the
	// gate mutation endpoints (POST /approve etc.) are covered by
	// internal/orchhttp/gate_handlers_test.go with an in-process stub.
	stop := startOrchHTTP(ctx, cfg, db, "test-project", nil)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = stop(shutdownCtx)
	}()

	// Give the listener a moment to bind.
	waitForListener(t, "127.0.0.1:"+port)

	// Drive the real agentmon HTTP client against the real orch
	// server. A heartbeat record is the tightest round-trip — the
	// ingestor sends a flat JSON object with type=heartbeat, the
	// middleware gate verifies the X-Drem-Agentmon-Token header,
	// and the handler accepts on success.
	ing := &agentmon.HTTPIngestor{
		OrchURL: "http://127.0.0.1:" + port,
		Token:   token,
	}
	err := ing.Ingest(ctx, []agentmon.IngestRecord{{
		Type:        "heartbeat",
		ContainerID: "c1",
		WorkerID:    "w1",
		Timestamp:   time.Now().UTC(),
		Payload:     map[string]any{"agent_id": "a1"},
	}})
	require.NoError(t, err, "agentmon HTTPIngestor round-trip must succeed when DREM_AGENTMON_TOKEN matches on both sides; a 401 here means the env-var fallback regressed")
}

// TestStartOrchHTTPRejectsWrongTokenFromEnv asserts that the env-var
// fallback is not a blanket "anything works" escape hatch. A worker
// (or attacker) presenting a different token still gets 401. Together
// with TestStartOrchHTTPAuthenticatesAgentmonClient this pins down the
// precedence and the fail-closed behaviour.
func TestStartOrchHTTPRejectsWrongTokenFromEnv(t *testing.T) {
	t.Setenv("DREM_AGENTMON_TOKEN", "server-token")

	db := newOrchHTTPTestDB(t)
	port := freePort(t)
	cfg := Config{OrchHTTPPort: port, ProjectLanguage: "go"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startOrchHTTP(ctx, cfg, db, "test-project", nil)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = stop(shutdownCtx)
	}()
	waitForListener(t, "127.0.0.1:"+port)

	ing := &agentmon.HTTPIngestor{
		OrchURL: "http://127.0.0.1:" + port,
		Token:   "wrong-token",
	}
	err := ing.Ingest(ctx, []agentmon.IngestRecord{{
		Type:      "heartbeat",
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"agent_id": "a1"},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "status 401")
}

func TestStartOrchHTTPConfiguresDockerLogs(t *testing.T) {
	orig := newDockerLogStreamer
	fakeLogs := &fakeStartupLogStreamer{}
	var closed atomic.Bool
	newDockerLogStreamer = func() (orchhttp.LogStreamer, func() error, error) {
		return fakeLogs, func() error {
			closed.Store(true)
			return nil
		}, nil
	}
	t.Cleanup(func() { newDockerLogStreamer = orig })

	db := newOrchHTTPTestDB(t)
	port := freePort(t)
	cfg := Config{OrchHTTPPort: port, ProjectLanguage: "go"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startOrchHTTP(ctx, cfg, db, "test-project", nil)
	waitForListener(t, "127.0.0.1:"+port)

	resp, err := http.Get("http://127.0.0.1:" + port + "/logs?container=c1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "startup logs\n", string(body))
	require.EqualValues(t, 1, fakeLogs.calls.Load())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	require.NoError(t, stop(shutdownCtx))
	require.True(t, closed.Load())
}

// newOrchHTTPTestDB returns an in-memory SQLite DB with just the
// tables handleIngest reads and writes. Kept local to this test file
// so we do not pull internal/testutil's full fixture surface into a
// cmd/drem test — the orchhttp handler code itself writes TaskEvent
// rows, nothing else.
func newOrchHTTPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Project{}, &model.Task{}, &model.Agent{}, &model.TaskEvent{}))
	// Seed the project row so handleIngest's worker lookups do not
	// trip on a missing FK. The name must match the one passed to
	// startOrchHTTP above.
	require.NoError(t, db.Create(&model.Project{
		Name:          "test-project",
		BareRepoPath:  "/tmp/test.git",
		DefaultBranch: "master",
	}).Error)
	return db
}

// freePort asks the kernel for a free TCP port, closes the listener,
// and returns the port as a string. There is a tiny race between close
// and the startOrchHTTP bind, but it is wide enough in practice that
// the test is stable; replace with fd-passing if flakes ever appear.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)
	return port
}

// waitForListener polls the given addr until a TCP connect succeeds or
// a short deadline elapses. Keeps the integration tests above free of
// magic-number sleeps.
func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("orch HTTP listener at %s did not come up within deadline", addr)
}
