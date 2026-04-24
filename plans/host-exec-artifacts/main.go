// drem-host-exec: host-side HTTP exec daemon for container-Kyle.
// Synchronous POST /exec with bearer token; denylist-then-allowlist check.
// NEVER spawns a shell — exec.CommandContext with argv slice only.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultBind    = "172.17.0.1:8091"
	defaultTimeout = 30
	maxTimeout     = 600
	minTimeout     = 1
	outputCap      = 10 * 1024 * 1024
)

type execReq struct {
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type execResp struct {
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	ExitCode     int    `json:"exit_code"`
	DurationMs   int64  `json:"duration_ms"`
	Truncated    bool   `json:"truncated,omitempty"`
	Error        string `json:"error,omitempty"`
	DeniedReason string `json:"denied_reason,omitempty"`
}

type auditLine struct {
	TS           string   `json:"ts"`
	Corrid       string   `json:"corrid"`
	Cmd          string   `json:"cmd"`
	Argv         []string `json:"argv"`
	Exit         int      `json:"exit"`
	DurationMs   int64    `json:"duration_ms"`
	CallerIP     string   `json:"caller_ip"`
	DeniedReason string   `json:"denied_reason,omitempty"`
}

var (
	auditMu sync.Mutex
	auditF  *os.File
)

func writeAudit(l auditLine) {
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	b = append(b, '\n')
	auditMu.Lock()
	defer auditMu.Unlock()
	if auditF != nil {
		_, _ = auditF.Write(b)
	}
}

func genCorrid() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

func loadToken() (string, error) {
	if f := os.Getenv("HOST_EXEC_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if t := os.Getenv("HOST_EXEC_TOKEN"); t != "" {
		return t, nil
	}
	return "", errors.New("no token configured (HOST_EXEC_TOKEN_FILE or HOST_EXEC_TOKEN required)")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	bind := envOr("HOST_EXEC_BIND", defaultBind)
	allowPath := envOr("HOST_EXEC_ALLOWLIST", "/etc/drem/host-exec.allowlist")
	denyPath := envOr("HOST_EXEC_DENYLIST", "/etc/drem/host-exec.denylist")
	logPath := envOr("HOST_EXEC_LOG", "/home/godinj/.drem-csuite/host-exec.log")

	if strings.HasPrefix(bind, "0.0.0.0") {
		log.Fatalf("refusing to bind on 0.0.0.0; use bridge IP")
	}

	token, err := loadToken()
	if err != nil {
		log.Fatalf("token: %v", err)
	}
	tokenBytes := []byte(token)

	allow, err := LoadRuleset(allowPath)
	if err != nil {
		log.Fatalf("allowlist %s: %v (fail-closed)", allowPath, err)
	}
	if len(allow.Patterns) == 0 {
		log.Fatalf("allowlist %s has zero patterns after filtering comments/blanks; refusing to start (fail-closed)", allowPath)
	}
	deny, err := LoadRuleset(denyPath)
	if err != nil {
		log.Fatalf("denylist %s: %v (fail-closed — denylist absence is a safety-net gap, not a config choice)", denyPath, err)
	}

	auditF, err = os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatalf("audit log %s: %v", logPath, err)
	}
	defer auditF.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/exec", func(w http.ResponseWriter, r *http.Request) {
		handleExec(w, r, tokenBytes, allow, deny)
	})

	srv := &http.Server{
		Addr:              bind,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("drem-host-exec listening on %s", bind)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	log.Printf("shutdown signal received")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func handleExec(w http.ResponseWriter, r *http.Request, token []byte, allow, deny *Ruleset) {
	corrid := r.Header.Get("X-Corrid")
	if corrid == "" {
		corrid = genCorrid()
	}
	callerIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if callerIP == "" {
		callerIP = r.RemoteAddr
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authz := r.Header.Get("Authorization")
	presented := strings.TrimPrefix(authz, "Bearer ")
	if presented == authz || subtle.ConstantTimeCompare([]byte(presented), token) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(execResp{ExitCode: -1, Error: "unauthorized"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req execReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Command == "" {
		http.Error(w, "missing command", http.StatusBadRequest)
		return
	}

	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout < minTimeout {
		timeout = minTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	start := time.Now()

	if matched, pat := deny.Match(req.Command, req.Args); matched {
		reason := "denylist: " + pat
		writeAudit(auditLine{
			TS: time.Now().UTC().Format(time.RFC3339), Corrid: corrid,
			Cmd: req.Command, Argv: req.Args, Exit: -1,
			DurationMs: time.Since(start).Milliseconds(),
			CallerIP:   callerIP, DeniedReason: reason,
		})
		writeJSON(w, http.StatusForbidden, execResp{
			ExitCode: -1, Error: "denied", DeniedReason: reason,
			DurationMs: time.Since(start).Milliseconds(),
		})
		return
	}

	matched, pat := allow.Match(req.Command, req.Args)
	if !matched {
		reason := "no allowlist match"
		writeAudit(auditLine{
			TS: time.Now().UTC().Format(time.RFC3339), Corrid: corrid,
			Cmd: req.Command, Argv: req.Args, Exit: -1,
			DurationMs: time.Since(start).Milliseconds(),
			CallerIP:   callerIP, DeniedReason: reason,
		})
		writeJSON(w, http.StatusForbidden, execResp{
			ExitCode: -1, Error: "denied", DeniedReason: reason,
			DurationMs: time.Since(start).Milliseconds(),
		})
		return
	}
	_ = pat

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &cappedWriter{buf: &stdout, cap: outputCap}
	cmd.Stderr = &cappedWriter{buf: &stderr, cap: outputCap}

	runErr := cmd.Run()
	dur := time.Since(start).Milliseconds()

	exitCode := 0
	var errStr string
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
			errStr = "timeout"
		} else if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
			errStr = runErr.Error()
		}
	}

	combined := stdout.Len() + stderr.Len()
	truncated := combined >= outputCap

	writeAudit(auditLine{
		TS: time.Now().UTC().Format(time.RFC3339), Corrid: corrid,
		Cmd: req.Command, Argv: req.Args, Exit: exitCode,
		DurationMs: dur, CallerIP: callerIP,
	})

	writeJSON(w, http.StatusOK, execResp{
		Stdout: stdout.String(), Stderr: stderr.String(),
		ExitCode: exitCode, DurationMs: dur,
		Truncated: truncated, Error: errStr,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// cappedWriter drops bytes past cap but keeps track so combined truncation works.
type cappedWriter struct {
	buf *bytes.Buffer
	cap int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	remaining := c.cap - c.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}
