package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godinj/drem-orchestrator/internal/csuite"
	"github.com/godinj/drem-orchestrator/internal/serve"
	"github.com/godinj/drem-orchestrator/internal/testutil"
)

// ---------------------------------------------------------------------------
// Config resolution: flag > env > default
// ---------------------------------------------------------------------------

func TestResolveConfig(t *testing.T) {
	cases := []struct {
		name       string
		envToken   string
		envAddr    string
		envDB      string
		envRoot    string
		envNoAuth  string
		flagToken  string
		flagAddr   string
		flagDB     string
		flagNoAuth bool
		wantToken  string
		wantAddr   string
		wantDB     string
		wantRoot   string
		wantNoAuth bool
	}{
		{
			name:      "all_defaults",
			wantToken: "",
			wantAddr:  ":8080",
			wantDB:    "~/.drem-csuite/csuite.db",
			wantRoot:  "~/.drem-csuite",
		},
		{
			name:      "env_vars_only",
			envToken:  "env-tok",
			envAddr:   ":9090",
			envDB:     "/tmp/test.db",
			envRoot:   "/tmp/csuite-root",
			wantToken: "env-tok",
			wantAddr:  ":9090",
			wantDB:    "/tmp/test.db",
			wantRoot:  "/tmp/csuite-root",
		},
		{
			name:       "flags_override_env",
			envToken:   "env-tok",
			envAddr:    ":9090",
			envDB:      "/tmp/env.db",
			envRoot:    "/tmp/env-root",
			envNoAuth:  "true",
			flagToken:  "flag-tok",
			flagAddr:   ":7070",
			flagDB:     "/tmp/flag.db",
			wantToken:  "flag-tok",
			wantAddr:   ":7070",
			wantDB:     "/tmp/flag.db",
			wantRoot:   "/tmp/env-root",
			wantNoAuth: true,
		},
		{
			name:      "flags_without_env",
			flagToken: "flag-tok",
			flagAddr:  ":6060",
			flagDB:    "/tmp/flag.db",
			wantToken: "flag-tok",
			wantAddr:  ":6060",
			wantDB:    "/tmp/flag.db",
			wantRoot:  "~/.drem-csuite",
		},
		{
			name:      "disk_root_env_sets_default_db_path",
			envRoot:   "/tmp/csuite-root",
			wantToken: "",
			wantAddr:  ":8080",
			wantDB:    "/tmp/csuite-root/csuite.db",
			wantRoot:  "/tmp/csuite-root",
		},
		{
			name:       "partial_flag_override",
			envToken:   "env-tok",
			envAddr:    ":9090",
			flagToken:  "flag-tok",
			flagNoAuth: true,
			wantToken:  "flag-tok",
			wantAddr:   ":9090",
			wantDB:     "~/.drem-csuite/csuite.db",
			wantRoot:   "~/.drem-csuite",
			wantNoAuth: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DREM_BRIDGE_TOKEN", tc.envToken)
			t.Setenv("DREM_BRIDGE_ADDR", tc.envAddr)
			t.Setenv("CSUITE_DB", tc.envDB)
			t.Setenv("DREM_CSUITE_ROOT", tc.envRoot)
			t.Setenv("DREM_BRIDGE_NO_AUTH", tc.envNoAuth)

			got := resolveConfig(tc.flagToken, tc.flagAddr, tc.flagDB, tc.flagNoAuth)

			if got.Token != tc.wantToken {
				t.Errorf("Token = %q, want %q", got.Token, tc.wantToken)
			}
			if got.Addr != tc.wantAddr {
				t.Errorf("Addr = %q, want %q", got.Addr, tc.wantAddr)
			}
			if got.DBPath != tc.wantDB {
				t.Errorf("DBPath = %q, want %q", got.DBPath, tc.wantDB)
			}
			if got.DiskRoot != tc.wantRoot {
				t.Errorf("DiskRoot = %q, want %q", got.DiskRoot, tc.wantRoot)
			}
			if got.NoAuth != tc.wantNoAuth {
				t.Errorf("NoAuth = %v, want %v", got.NoAuth, tc.wantNoAuth)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Missing token validation
// ---------------------------------------------------------------------------

func TestMissingToken(t *testing.T) {
	t.Setenv("DREM_BRIDGE_TOKEN", "")
	t.Setenv("DREM_BRIDGE_ADDR", "")
	t.Setenv("CSUITE_DB", "")
	t.Setenv("DREM_CSUITE_ROOT", "")
	t.Setenv("DREM_BRIDGE_NO_AUTH", "")

	var stderr bytes.Buffer
	code := run(nil, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("token is required")) {
		t.Errorf("stderr = %q, want token error message", stderr.String())
	}
}

func TestMissingTokenAllowedWithNoAuth(t *testing.T) {
	t.Setenv("DREM_BRIDGE_TOKEN", "")
	t.Setenv("DREM_BRIDGE_ADDR", "")
	t.Setenv("CSUITE_DB", "")
	t.Setenv("DREM_CSUITE_ROOT", "")
	t.Setenv("DREM_BRIDGE_NO_AUTH", "")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "noauth.db")

	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{
			"--no-auth",
			"--listen", "127.0.0.1:0",
			"--db", dbPath,
		}, &stderr)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stderr.contains([]byte("listening on")) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !stderr.contains([]byte("listening on")) {
		t.Fatalf("server did not start in no-auth mode; stderr: %s", stderr.String())
	}
	if !stderr.contains([]byte("authentication disabled")) {
		t.Errorf("stderr = %q, want disabled-auth warning", stderr.String())
	}

	syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not exit within 10 seconds after SIGINT")
	}
}

// ---------------------------------------------------------------------------
// WAL mode verification
// ---------------------------------------------------------------------------

func TestWALMode(t *testing.T) {
	db := testutil.NewTestDBFileWAL(t)

	var mode string
	db.Raw("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

// ---------------------------------------------------------------------------
// Integration smoke test: start server → GET /api/health → 200 → stop
// ---------------------------------------------------------------------------

func TestIntegrationHealth(t *testing.T) {
	db := testutil.NewTestDBWithModels(t, &csuite.CsuiteAgent{}, &csuite.CsuiteInboxMessage{})

	store := csuite.NewStore(db)
	token := "test-secret"
	srv := serve.New(serve.Config{Token: token, Addr: "127.0.0.1:0", Store: store})

	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	url := fmt.Sprintf("http://%s/api/health", srv.ListenAddr())
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestBridgeServerUsesDiskBackedState(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	now := time.Now().UTC().Truncate(time.Second)

	writeDiskFile(t, filepath.Join(root, "kyle", "state.md"), fmt.Sprintf(`---
last_heartbeat: %s
context_percent: 7
current_activity: reviewing
---
`, now.Format(time.RFC3339)))
	writeDiskFile(t, filepath.Join(root, "kyle", "inbox", "20260423T120000Z-operator-to-kyle-aaaaaaaa.md"), fmt.Sprintf(`---
from: operator
to: kyle
topic: mobile bridge
sent_at: %s
correlation_id: aaaaaaaa
---

hello kyle
`, now.Format(time.RFC3339)))

	var stderr bytes.Buffer
	srv, cleanup, err := newBridgeServer(bridgeConfig{
		Token:    "test-secret",
		Addr:     "127.0.0.1:0",
		DBPath:   dbPath,
		DiskRoot: root,
	}, &stderr)
	if err != nil {
		t.Fatalf("newBridgeServer: %v; stderr: %s", err, stderr.String())
	}
	t.Cleanup(cleanup)

	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/agents", srv.ListenAddr()), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/agents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var agents []struct {
		Name            string `json:"name"`
		ContextPercent  int    `json:"context_percent"`
		CurrentActivity string `json:"current_activity"`
		UnreadCount     int    `json:"unread_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		t.Fatalf("decode agents: %v", err)
	}

	byName := make(map[string]struct {
		Name            string `json:"name"`
		ContextPercent  int    `json:"context_percent"`
		CurrentActivity string `json:"current_activity"`
		UnreadCount     int    `json:"unread_count"`
	}, len(agents))
	for _, agent := range agents {
		byName[agent.Name] = agent
	}

	kyle, ok := byName["kyle"]
	if !ok {
		t.Fatalf("kyle missing from disk-backed agent list: %#v", agents)
	}
	if _, ok := byName["ross"]; ok {
		t.Fatalf("ross should not appear in disk-backed persona list: %#v", agents)
	}
	if kyle.ContextPercent != 7 {
		t.Errorf("kyle context_percent = %d, want 7", kyle.ContextPercent)
	}
	if kyle.CurrentActivity != "reviewing" {
		t.Errorf("kyle current_activity = %q, want reviewing", kyle.CurrentActivity)
	}
	if kyle.UnreadCount != 1 {
		t.Errorf("kyle unread_count = %d, want 1", kyle.UnreadCount)
	}

	msgReq, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/messages?agent=kyle", srv.ListenAddr()), nil)
	if err != nil {
		t.Fatalf("new messages request: %v", err)
	}
	msgReq.Header.Set("Authorization", "Bearer test-secret")

	msgResp, err := http.DefaultClient.Do(msgReq)
	if err != nil {
		t.Fatalf("GET /api/messages: %v", err)
	}
	defer msgResp.Body.Close()

	if msgResp.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d, want %d", msgResp.StatusCode, http.StatusOK)
	}

	var messages []struct {
		FromAgent string `json:"from_agent"`
		ToAgent   string `json:"to_agent"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
	}
	if err := json.NewDecoder(msgResp.Body).Decode(&messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1: %#v", len(messages), messages)
	}
	if messages[0].FromAgent != "operator" || messages[0].ToAgent != "kyle" {
		t.Errorf("message route = %s -> %s, want operator -> kyle", messages[0].FromAgent, messages[0].ToAgent)
	}
	if messages[0].Subject != "mobile bridge" {
		t.Errorf("message subject = %q, want mobile bridge", messages[0].Subject)
	}
	if messages[0].Body != "hello kyle\n" {
		t.Errorf("message body = %q, want %q", messages[0].Body, "hello kyle\n")
	}
}

func TestBridgeServerMountsPersonaControl(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	t.Setenv("DREM_PROJECT_COMPOSE", "")
	t.Setenv("DREM_DOCKER_COMPOSE_CMD", "")

	var stderr bytes.Buffer
	srv, cleanup, err := newBridgeServer(bridgeConfig{
		Token:    "test-secret",
		Addr:     "127.0.0.1:0",
		DBPath:   dbPath,
		DiskRoot: root,
	}, &stderr)
	if err != nil {
		t.Fatalf("newBridgeServer: %v; stderr: %s", err, stderr.String())
	}
	t.Cleanup(cleanup)

	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/personas/containers", srv.ListenAddr()), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/personas/containers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body struct {
		Available bool `json:"available"`
		Items     []struct {
			Target string `json:"target"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Available {
		t.Fatal("available = true, want false without DREM_PROJECT_COMPOSE")
	}
	if len(body.Items) != 4 {
		t.Fatalf("items len = %d, want 4", len(body.Items))
	}
}

func writeDiskFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Signal shutdown: start via run() → SIGINT → clean exit (code 0)
// ---------------------------------------------------------------------------

// syncBuffer is a thread-safe buffer for concurrent read/write in tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) contains(sub []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.buf.Bytes(), sub)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestSignalShutdown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "signal.db")
	token := "test-signal"

	t.Setenv("DREM_BRIDGE_TOKEN", "")
	t.Setenv("DREM_BRIDGE_ADDR", "")
	t.Setenv("CSUITE_DB", "")
	t.Setenv("DREM_CSUITE_ROOT", "")
	t.Setenv("DREM_BRIDGE_NO_AUTH", "")

	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{
			"--token", token,
			"--listen", "127.0.0.1:0",
			"--db", dbPath,
		}, &stderr)
	}()

	// Wait for server to be ready.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stderr.contains([]byte("listening on")) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !stderr.contains([]byte("listening on")) {
		t.Fatal("server did not start within 5 seconds")
	}

	// Send SIGINT to trigger graceful shutdown.
	syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not exit within 10 seconds after SIGINT")
	}
}
