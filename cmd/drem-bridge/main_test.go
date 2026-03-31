package main

import (
	"bytes"
	"fmt"
	"net/http"
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
		name      string
		envToken  string
		envAddr   string
		envDB     string
		flagToken string
		flagAddr  string
		flagDB    string
		wantToken string
		wantAddr  string
		wantDB    string
	}{
		{
			name:      "all_defaults",
			wantToken: "",
			wantAddr:  ":8080",
			wantDB:    "~/.drem-csuite/csuite.db",
		},
		{
			name:      "env_vars_only",
			envToken:  "env-tok",
			envAddr:   ":9090",
			envDB:     "/tmp/test.db",
			wantToken: "env-tok",
			wantAddr:  ":9090",
			wantDB:    "/tmp/test.db",
		},
		{
			name:      "flags_override_env",
			envToken:  "env-tok",
			envAddr:   ":9090",
			envDB:     "/tmp/env.db",
			flagToken: "flag-tok",
			flagAddr:  ":7070",
			flagDB:    "/tmp/flag.db",
			wantToken: "flag-tok",
			wantAddr:  ":7070",
			wantDB:    "/tmp/flag.db",
		},
		{
			name:      "flags_without_env",
			flagToken: "flag-tok",
			flagAddr:  ":6060",
			flagDB:    "/tmp/flag.db",
			wantToken: "flag-tok",
			wantAddr:  ":6060",
			wantDB:    "/tmp/flag.db",
		},
		{
			name:      "partial_flag_override",
			envToken:  "env-tok",
			envAddr:   ":9090",
			flagToken: "flag-tok",
			wantToken: "flag-tok",
			wantAddr:  ":9090",
			wantDB:    "~/.drem-csuite/csuite.db",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DREM_BRIDGE_TOKEN", tc.envToken)
			t.Setenv("DREM_BRIDGE_ADDR", tc.envAddr)
			t.Setenv("CSUITE_DB", tc.envDB)

			got := resolveConfig(tc.flagToken, tc.flagAddr, tc.flagDB)

			if got.Token != tc.wantToken {
				t.Errorf("Token = %q, want %q", got.Token, tc.wantToken)
			}
			if got.Addr != tc.wantAddr {
				t.Errorf("Addr = %q, want %q", got.Addr, tc.wantAddr)
			}
			if got.DBPath != tc.wantDB {
				t.Errorf("DBPath = %q, want %q", got.DBPath, tc.wantDB)
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

	var stderr bytes.Buffer
	code := run(nil, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("token is required")) {
		t.Errorf("stderr = %q, want token error message", stderr.String())
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
